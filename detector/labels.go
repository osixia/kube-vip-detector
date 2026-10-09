package detector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"

	"github.com/osixia/container-baseimage/log"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// Node labels
// =============================

func patchLabel(ctx context.Context, client kubernetes.Interface, node corev1.Node, key string, value any) error {

	if ctx.Err() != nil {
		return ctx.Err()
	}

	body, _ := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"resourceVersion": node.ResourceVersion,
			"labels":          map[string]any{key: value},
		},
	})
	_, err := client.CoreV1().Nodes().Patch(ctx, node.Name, types.MergePatchType, body, metav1.PatchOptions{})
	return err
}

func (d *Detector) reconcile(ctx context.Context, nodes []corev1.Node, ip, winner string) error {
	return reconcileLabelsUsing(nodes, d.options.VIPLabelPrefix, ip, winner, func(node corev1.Node, value any) error {
		action := "set"
		if value == nil {
			action = "remove"
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		if d.options.DryRun {
			log.Infof("dry-run: proposed label change vip=%q node=%q action=%q label=%q", ip, node.Name, action, vipLabel(d.options.VIPLabelPrefix, ip))
			return nil
		}

		err := patchLabel(ctx, d.client, node, vipLabel(d.options.VIPLabelPrefix, ip), value)
		if err == nil {
			log.Infof("label changed vip=%q node=%q action=%q", ip, node.Name, action)
		}

		return err
	})
}

// Remove old winners before adding the new winner; resourceVersion prevents
// overwriting concurrent changes. Conflicts are retried after a fresh probe/list.
func reconcileLabelsUsing(nodes []corev1.Node, prefix, ip, winner string, patch func(corev1.Node, any) error) error {

	key := vipLabel(prefix, ip)
	var target *corev1.Node
	for i := range nodes {
		node := nodes[i]
		if node.Name == winner {
			target = &nodes[i]
			continue
		}

		if _, ok := node.Labels[key]; ok {
			if err := patch(node, nil); err != nil {
				return err
			}
		}
	}

	if winner != "" {
		if target == nil {
			return fmt.Errorf("winner %q no longer exists", winner)
		}

		if target.Labels[key] != "true" {
			if err := patch(*target, "true"); err != nil {
				return err
			}
		}
	}

	return nil
}

// IPv4 keeps its historical label. IPv6 uses all 128 bits as 32 hexadecimal
// digits: colons are forbidden in Kubernetes label keys.
func vipLabel(prefix, ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err == nil && addr.Is6() {
		return fmt.Sprintf("%sipv6-%x", prefix, addr.As16())
	}
	return prefix + ip
}
