package detector

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func crdObject(kind, namespace, name, address string, port int64) *unstructured.Unstructured {
	spec := map[string]any{"address": address}
	if kind == "Peer" {
		spec["port"] = port
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "network.osixia.net/v1alpha1", "kind": kind,
		"metadata": map[string]any{"namespace": namespace, "name": name}, "spec": spec,
	}}
}

func crdClient(objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		vipResource: "VIPList", peerResource: "PeerList",
	}, objects...)
}

func crdDetector(t *testing.T, objects ...runtime.Object) *Detector {
	t.Helper()
	o := validOptions()
	o.VIPs = nil
	o.WatchCRDs = true
	o.PeerLabelPrefix = "example.net/peer-"
	o.Interval = 10 * time.Millisecond
	o.Timeout = 100 * time.Millisecond
	o.SuccessThreshold = 1
	o.FailureThreshold = 1
	d, err := NewWithDynamicClient(fake.NewClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node"}}),
		crdClient(objects...), Identity{Node: "node", Namespace: "detector", PodUID: "pod"}, o)
	if err != nil {
		t.Fatal(err)
	}
	d.checkLocal = func(string) (bool, error) { return true, nil }
	return d
}

func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition did not become true")
}

func startCRDTargets(t *testing.T, d *Detector) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.runCRDTargets(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("CRD controller stopped: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("CRD controller did not stop")
		}
	})
	return ctx
}

func TestCRDOptionsAndClientValidation(t *testing.T) {
	d := crdDetector(t)
	if err := d.options.Validate(); err != nil {
		t.Fatal("empty initial CRD configuration should be valid:", err)
	}
	if _, err := New(d.client, Identity{Node: "node", Namespace: "ns", PodUID: "id"}, d.options); err == nil {
		t.Fatal("missing dynamic client accepted")
	}
	for _, change := range []func(*Options){
		func(o *Options) { o.Key = "" },
		func(o *Options) { o.PeerLabelPrefix = "" },
		func(o *Options) { o.VIPLabelPrefix = "invalid/label/" },
	} {
		o := d.options
		change(&o)
		if err := o.Validate(); err == nil {
			t.Fatal("invalid CRD configuration accepted")
		}
	}
}

func TestCRDTargetsDeduplicateVIPsAndProtectStaticPeers(t *testing.T) {
	d := crdDetector(t)
	d.options.VIPs = []string{"203.0.113.10"}
	d.options.Peers = []string{"crd.apps.db=10.0.0.1:5432"}
	vips := []any{crdObject("VIP", "apps", "web", "203.0.113.10", 0), crdObject("VIP", "other", "web", "203.0.113.10", 0)}
	peers := []any{crdObject("Peer", "apps", "db", "10.0.0.2", 5432), crdObject("Peer", "other", "db", "10.0.0.3", 5432)}
	targets, labels := d.crdTargets(vips, peers)
	if len(targets) != 3 || len(labels) != 2 {
		t.Fatalf("unexpected union: targets=%v labels=%v", targets, labels)
	}
	if targets["example.net/peer-crd.apps.db"].peer.address != "10.0.0.1:5432" {
		t.Fatal("CRD replaced a conflicting static target")
	}
	if labels["example.net/peer-crd.apps.db"] {
		t.Fatal("rejected CRD claimed ownership")
	}
	targets, _ = d.crdTargets(nil, nil)
	if len(targets) != 2 {
		t.Fatal("removing CRDs removed static targets")
	}
}

func TestCRDTargetValidationAndPeerIdentities(t *testing.T) {
	d := crdDetector(t)
	for _, obj := range []*unstructured.Unstructured{
		crdObject("VIP", "ns", "v", "not-an-ip", 0),
		crdObject("VIP", "ns", "v", "127.0.0.1", 0),
		crdObject("Peer", "ns", "p", "10.0.0.1", 0),
		crdObject("Peer", "ns", "p", "10.0.0.1", 65536),
	} {
		if _, _, err := d.crdTarget(obj, obj.GetKind() == "VIP"); err == nil {
			t.Fatalf("invalid target accepted: %v", obj.Object)
		}
	}
	for _, address := range []string{"203.0.113.10", "2001:db8::1"} {
		if _, _, err := d.crdTarget(crdObject("VIP", "ns", "vip", address, 0), true); err != nil {
			t.Fatal(err)
		}
		if _, _, err := d.crdTarget(crdObject("Peer", "ns", "peer", address, 443), false); err != nil {
			t.Fatal(err)
		}
	}
	a := crdPeerName(d.options.PeerLabelPrefix, "a-b", "c")
	b := crdPeerName(d.options.PeerLabelPrefix, "a", "b-c")
	if a == b {
		t.Fatal("namespace/name pairs collided")
	}
	longName := strings.Repeat("a", 100)
	a = crdPeerName(d.options.PeerLabelPrefix, "ns", longName)
	b = crdPeerName(d.options.PeerLabelPrefix, "other", longName)
	if a == b || len("peer-"+a) > 63 {
		t.Fatal("long names must be distinct valid label names")
	}
}

func TestCRDPeerWatchAddUpdateDelete(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	d := crdDetector(t)
	ctx := startCRDTargets(t, d)
	key := d.options.PeerLabelPrefix + "crd.apps.database"
	hasLabel := func() bool {
		node, err := d.client.CoreV1().Nodes().Get(ctx, "node", metav1.GetOptions{})
		return err == nil && node.Labels[key] == "true"
	}
	obj := crdObject("Peer", "apps", "database", "127.0.0.1", int64(listener.Addr().(*net.TCPAddr).Port))
	resource := d.dynamicClient.Resource(peerResource).Namespace("apps")
	if _, err := resource.Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, hasLabel)
	// Change to an invalid port: validation must stop the old successful worker
	// and remove its label instead of continuing to probe the old endpoint.
	if err := unstructured.SetNestedField(obj.Object, int64(0), "spec", "port"); err != nil {
		t.Fatal(err)
	}
	if _, err := resource.Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return !hasLabel() })
	if err := unstructured.SetNestedField(obj.Object, int64(listener.Addr().(*net.TCPAddr).Port), "spec", "port"); err != nil {
		t.Fatal(err)
	}
	if _, err := resource.Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, hasLabel)
	if err := resource.Delete(ctx, obj.GetName(), metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return !hasLabel() })
}

func TestCRDDuplicateVIPDeletionAndOfflineInventory(t *testing.T) {
	a := crdObject("VIP", "a", "web", "203.0.113.10", 0)
	b := crdObject("VIP", "b", "web", "203.0.113.10", 0)
	d := crdDetector(t, a, b)
	key := vipLabel(d.options.VIPLabelPrefix, "203.0.113.10")
	stale := "example.net/peer-crd.deleted.db"
	journal, _ := json.Marshal([]string{stale})
	ctx := context.Background()
	node, err := d.client.CoreV1().Nodes().Get(ctx, "node", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	node.Labels = map[string]string{key: "true", stale: "true", "unrelated": "keep"}
	node.Annotations = map[string]string{crdLabelsAnnotation: string(journal), "unrelated": "keep"}
	if _, err := d.client.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	startCRDTargets(t, d)
	eventually(t, func() bool {
		node, err := d.client.CoreV1().Nodes().Get(ctx, "node", metav1.GetOptions{})
		return err == nil && node.Labels[stale] == "" && strings.Contains(node.Annotations[crdLabelsAnnotation], key)
	})
	if err := d.dynamicClient.Resource(vipResource).Namespace("a").Delete(ctx, "web", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	// Wait until the cache has processed deletion before checking preservation.
	time.Sleep(100 * time.Millisecond)
	node, err = d.client.CoreV1().Nodes().Get(ctx, "node", metav1.GetOptions{})
	if err != nil || node.Labels[key] != "true" {
		t.Fatalf("removing one VIP declaration removed a shared label: %v", err)
	}
	if err := d.dynamicClient.Resource(vipResource).Namespace("b").Delete(ctx, "web", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		node, err := d.client.CoreV1().Nodes().Get(ctx, "node", metav1.GetOptions{})
		return err == nil && node.Labels[key] == "" && node.Labels["unrelated"] == "keep" && node.Annotations["unrelated"] == "keep"
	})
}

func TestCRDCleanupRetriesAPIConflicts(t *testing.T) {
	d := crdDetector(t)
	ctx := context.Background()
	key := "example.net/peer-crd.deleted.db"
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", Labels: map[string]string{key: "true"}}}
	client := fake.NewClientset(node)
	d.client = client
	conflict := true
	client.PrependReactor("patch", "nodes", func(clienttesting.Action) (bool, runtime.Object, error) {
		if conflict {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "nodes"}, "node", errors.New("changed"))
		}
		return false, nil, nil
	})
	m := &targetManager{detector: d, workers: map[string]targetWorker{}, retired: map[string]bool{key: true}, reset: map[string]bool{}}
	if err := m.reconcile(ctx, map[string]target{}, map[string]bool{}); !apierrors.IsConflict(err) {
		t.Fatal("expected conflict:", err)
	}
	conflict = false
	if err := m.reconcile(ctx, map[string]target{}, map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	updated, err := client.CoreV1().Nodes().Get(ctx, "node", metav1.GetOptions{})
	if err != nil || updated.Labels[key] != "" {
		t.Fatal("cleanup did not retry:", err)
	}
}

func TestCRDDryRunDoesNotWriteInventoryOrLabels(t *testing.T) {
	d := crdDetector(t, crdObject("VIP", "ns", "vip", "203.0.113.10", 0))
	d.options.DryRun = true
	ctx := startCRDTargets(t, d)
	eventually(t, func() bool { return len(d.client.(*fake.Clientset).Actions()) >= 3 })
	node, err := d.client.CoreV1().Nodes().Get(ctx, "node", metav1.GetOptions{})
	if err != nil || len(node.Annotations) != 0 || len(node.Labels) != 0 {
		t.Fatalf("dry-run mutated node: %v", err)
	}
	for _, action := range d.client.(*fake.Clientset).Actions() {
		if action.GetVerb() != "get" && action.GetVerb() != "list" {
			t.Fatalf("dry-run wrote to API: %v", action)
		}
	}
}

func TestCRDDiscoveryFailureIsReturned(t *testing.T) {
	d := crdDetector(t)
	d.dynamicClient.(*dynamicfake.FakeDynamicClient).PrependReactor("list", "vips", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(vipResource.GroupResource(), "", errors.New("denied"))
	})
	if err := d.runCRDTargets(context.Background()); !apierrors.IsForbidden(err) {
		t.Fatal("list failure was not returned:", err)
	}
	for _, action := range d.client.(*fake.Clientset).Actions() {
		if action.GetVerb() == "patch" {
			t.Fatal("discovery failure mutated node")
		}
	}
}

func TestCRDInventoryFailureStopsInformers(t *testing.T) {
	d := crdDetector(t)
	d.client.(*fake.Clientset).PrependReactor("get", "nodes", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, "node", errors.New("denied"))
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.runCRDTargets(ctx) }()
	select {
	case err := <-done:
		if !apierrors.IsForbidden(err) {
			t.Fatal("inventory failure was not returned:", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("controller blocked shutting down informers after inventory failure")
	}
}
