package detector

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/osixia/container-baseimage/log"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
)

var (
	vipResource  = schema.GroupVersionResource{Group: "network.osixia.net", Version: "v1alpha1", Resource: "vips"}
	peerResource = schema.GroupVersionResource{Group: "network.osixia.net", Version: "v1alpha1", Resource: "peers"}
)

const crdLabelsAnnotation = "kube-network-detector/crd-labels"

type target struct {
	vip  string
	peer peer
}

type targetWorker struct {
	target target
	cancel context.CancelFunc
	done   chan struct{}
}

// A single goroutine owns this registry. Removed workers are joined before
// labels are cleaned or replacement workers are started.
type targetManager struct {
	detector *Detector
	workers  map[string]targetWorker
	retired  map[string]bool
	reset    map[string]bool
}

func (d *Detector) runCRDTargets(ctx context.Context) error {
	ctx, cancelWatches := context.WithCancel(ctx)
	defer cancelWatches()
	// Fail clearly for missing CRDs or list permissions rather than serving
	// indefinitely with an empty configuration. Later list/watch failures retain
	// the informer cache and therefore never imply target deletion.
	for _, resource := range []schema.GroupVersionResource{vipResource, peerResource} {
		listCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := d.dynamicClient.Resource(resource).List(listCtx, metav1.ListOptions{Limit: 1})
		cancel()
		if err != nil {
			return fmt.Errorf("cannot discover %s: %w", resource.Resource, err)
		}
	}

	factory := dynamicinformer.NewDynamicSharedInformerFactory(d.dynamicClient, 0)
	vips := factory.ForResource(vipResource).Informer()
	peers := factory.ForResource(peerResource).Informer()
	changed := make(chan struct{}, 1)
	notify := func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	}
	for _, informer := range []cache.SharedIndexInformer{vips, peers} {
		_, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    func(any) { notify() },
			UpdateFunc: func(any, any) { notify() },
			DeleteFunc: func(any) { notify() },
		})
		if err != nil {
			return err
		}
	}
	factory.Start(ctx.Done())
	defer func() {
		cancelWatches()
		factory.Shutdown()
	}()
	if !cache.WaitForCacheSync(ctx.Done(), vips.HasSynced, peers.HasSynced) {
		return ctx.Err()
	}

	m := &targetManager{detector: d, workers: map[string]targetWorker{}, retired: map[string]bool{}, reset: map[string]bool{}}
	defer m.stop()
	// Persist ownership on the local Node so deletion while this instance was
	// offline is also reconciled on restart. Only CRD-declared labels are tracked;
	// matching static targets are protected by the desired configuration.
	node, err := d.client.CoreV1().Nodes().Get(ctx, d.node, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("load CRD label inventory: %w", err)
	}
	if value := node.Annotations[crdLabelsAnnotation]; value != "" {
		var labels []string
		if err := json.Unmarshal([]byte(value), &labels); err != nil {
			return fmt.Errorf("invalid CRD label inventory: %w", err)
		}
		for _, key := range labels {
			if problems := validation.IsQualifiedName(key); len(problems) != 0 {
				return fmt.Errorf("invalid inventoried label %q", key)
			}
			m.retired[key] = true
		}
	}

	ticker := time.NewTicker(d.options.Interval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		desired, labels := d.crdTargets(vips.GetStore().List(), peers.GetStore().List())
		if err := m.reconcile(ctx, desired, labels); err != nil && ctx.Err() == nil {
			log.Errorf("CRD target reconciliation failed: %v", err)
		}
		select {
		case <-ctx.Done():
		case <-changed:
		case <-ticker.C:
		}
	}
	return nil
}

func (d *Detector) crdTargets(vips, peers []any) (map[string]target, map[string]bool) {
	desired := map[string]target{}
	labels := map[string]bool{}
	for _, ip := range d.options.VIPs {
		desired[vipLabel(d.options.VIPLabelPrefix, ip)] = target{vip: ip}
	}
	for _, value := range d.options.Peers {
		p, _ := parsePeer(value) // Static options were validated by New.
		desired[d.options.PeerLabelPrefix+p.name] = target{peer: p}
	}
	// Sort to make any label collision deterministic, with static targets taking
	// precedence. Peer names include a resource marker and namespace identity.
	for kind, objects := range [][]any{vips, peers} {
		slices.SortFunc(objects, func(a, b any) int {
			x := a.(*unstructured.Unstructured)
			y := b.(*unstructured.Unstructured)
			return strings.Compare(x.GetNamespace()+"/"+x.GetName(), y.GetNamespace()+"/"+y.GetName())
		})
		for _, object := range objects {
			obj := object.(*unstructured.Unstructured)
			if obj.GetDeletionTimestamp() != nil {
				continue
			}
			t, key, err := d.crdTarget(obj, kind == 0)
			if err != nil {
				log.Warningf("ignoring invalid CRD target namespace=%q name=%q error=%q", obj.GetNamespace(), obj.GetName(), err)
				continue
			}
			if existing, exists := desired[key]; exists {
				if existing != t {
					log.Warningf("ignoring conflicting CRD target namespace=%q name=%q label=%q", obj.GetNamespace(), obj.GetName(), key)
					continue
				}
			}
			desired[key] = t
			labels[key] = true
		}
	}
	return desired, labels
}

func (d *Detector) crdTarget(obj *unstructured.Unstructured, vip bool) (target, string, error) {
	address, found, err := unstructured.NestedString(obj.Object, "spec", "address")
	if err != nil || !found || address == "" {
		return target{}, "", fmt.Errorf("spec.address must be a nonempty string")
	}
	options := d.options
	options.VIPs = nil
	options.Peers = nil
	if vip {
		options.VIPs = []string{address}
		if err := options.Validate(); err != nil {
			return target{}, "", err
		}
		return target{vip: address}, vipLabel(options.VIPLabelPrefix, address), nil
	}
	port, found, err := unstructured.NestedInt64(obj.Object, "spec", "port")
	if err != nil || !found || port < 1 || port > 65535 {
		return target{}, "", fmt.Errorf("spec.port must be an integer between 1 and 65535")
	}
	name := crdPeerName(options.PeerLabelPrefix, obj.GetNamespace(), obj.GetName())
	value := name + "=" + net.JoinHostPort(address, strconv.FormatInt(port, 10))
	options.Peers = []string{value}
	if err := options.Validate(); err != nil {
		return target{}, "", err
	}
	p, err := parsePeer(value)
	return target{peer: p}, options.PeerLabelPrefix + name, err
}

// The first dot separates the namespace (a DNS label) from the object name.
// Hash long identities rather than truncating away their uniqueness. The "crd."
// marker also separates these names from conventional static peer names.
func crdPeerName(prefix, namespace, name string) string {
	identity := namespace + "." + name
	result := "crd." + identity
	labelPrefix := prefix[strings.LastIndex(prefix, "/")+1:]
	if len(labelPrefix)+len(result) > 63 {
		result = fmt.Sprintf("crd.%x", sha256.Sum256([]byte(identity)))[:36]
	}
	return result
}

func (m *targetManager) reconcile(ctx context.Context, desired map[string]target, crdLabels map[string]bool) error {
	for key, worker := range m.workers {
		if next, exists := desired[key]; exists && next == worker.target {
			continue
		}
		worker.cancel()
		<-worker.done
		delete(m.workers, key)
		m.retired[key] = true
		m.reset[key] = true
	}
	for key := range crdLabels {
		m.retired[key] = true
	}
	if err := m.reconcileLabels(ctx, desired, crdLabels); err != nil {
		return err
	}
	clear(m.reset)
	for key, t := range desired {
		if _, exists := m.workers[key]; exists {
			continue
		}
		workerCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		m.workers[key] = targetWorker{target: t, cancel: cancel, done: done}
		go func() {
			defer close(done)
			if t.vip != "" {
				m.detector.run(workerCtx, t.vip)
			} else {
				m.detector.observePeer(workerCtx, t.peer)
			}
		}()
	}
	return nil
}

// Each DaemonSet instance cleans its own node. Retired keys remain in memory
// and are rechecked, so a lagging VIP observer cannot leave a stale label behind.
// Inventory and label removals are one resourceVersion-guarded patch; conflicts
// retry on the next event/tick, and new workers start only after ownership saves.
func (m *targetManager) reconcileLabels(ctx context.Context, desired map[string]target, crdLabels map[string]bool) error {
	d := m.detector
	node, err := d.client.CoreV1().Nodes().Get(ctx, d.node, metav1.GetOptions{})
	if err != nil {
		return err
	}
	removals := map[string]any{}
	for key := range m.retired {
		if _, active := desired[key]; active && !m.reset[key] {
			continue
		}
		if _, exists := node.Labels[key]; exists {
			removals[key] = nil
		}
	}
	labels := make([]string, 0, len(crdLabels))
	for key := range crdLabels {
		labels = append(labels, key)
	}
	slices.Sort(labels)
	value, err := json.Marshal(labels)
	if err != nil {
		return err
	}
	if len(removals) == 0 && node.Annotations[crdLabelsAnnotation] == string(value) {
		return nil
	}
	if d.options.DryRun {
		for key := range removals {
			log.Infof("dry-run: proposed CRD label cleanup node=%q label=%q", d.node, key)
		}
		return nil
	}
	body, err := json.Marshal(map[string]any{"metadata": map[string]any{
		"resourceVersion": node.ResourceVersion,
		"labels":          removals,
		"annotations":     map[string]any{crdLabelsAnnotation: string(value)},
	}})
	if err != nil {
		return err
	}
	_, err = d.client.CoreV1().Nodes().Patch(ctx, d.node, types.MergePatchType, body, metav1.PatchOptions{})
	return err
}

func (m *targetManager) stop() {
	for _, worker := range m.workers {
		worker.cancel()
	}
	for _, worker := range m.workers {
		<-worker.done
	}
}
