package detector

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/osixia/kube-network-detector/config"
)

func TestLabelsMoveAndClear(t *testing.T) {
	ctx := context.Background()
	key := config.DefaultVIPLabelPrefix + "203.0.113.10"
	a := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "old", Labels: map[string]string{key: "true", "infra.example.net/provider": "ovh", "other": "kept"}}}
	b := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "new"}}
	client := fake.NewClientset(&a, &b)
	detector := &Detector{client: client, options: Options{VIPLabelPrefix: config.DefaultVIPLabelPrefix}}
	if err := detector.reconcile(ctx, []corev1.Node{a, b}, "203.0.113.10", "new"); err != nil {
		t.Fatal(err)
	}
	old, err := client.CoreV1().Nodes().Get(ctx, "old", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	newNode, err := client.CoreV1().Nodes().Get(ctx, "new", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := old.Labels[key]; ok {
		t.Fatal("old label retained")
	}
	if old.Labels["other"] != "kept" || old.Labels["infra.example.net/provider"] != "ovh" || newNode.Labels[key] != "true" {
		t.Fatal("incorrect merge")
	}
	actions := client.Actions()
	var order []string
	for _, a := range actions {
		if p, ok := a.(ktesting.PatchAction); ok {
			order = append(order, p.GetName())
		}
	}
	if fmt.Sprint(order) != "[old new]" {
		t.Fatal(order)
	}
	if err := detector.reconcile(ctx, []corev1.Node{*old, *newNode}, "203.0.113.10", ""); err != nil {
		t.Fatal(err)
	}
	newNode, err = client.CoreV1().Nodes().Get(ctx, "new", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := newNode.Labels[key]; ok {
		t.Fatal("label not removed on failure")
	}
}

func TestConflictDoesNotAddWinner(t *testing.T) {
	key := config.DefaultVIPLabelPrefix + "203.0.113.10"
	a := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "old", Labels: map[string]string{key: "true"}}}
	b := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "new"}}
	client := fake.NewClientset(&a, &b)
	detector := &Detector{client: client, options: Options{VIPLabelPrefix: config.DefaultVIPLabelPrefix}}
	client.PrependReactor("patch", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "nodes"}, "old", fmt.Errorf("changed"))
	})
	if err := detector.reconcile(context.Background(), []corev1.Node{a, b}, "203.0.113.10", "new"); err == nil {
		t.Fatal("conflict ignored")
	}
	if len(client.Actions()) != 1 {
		t.Fatal("winner patched despite failed removal")
	}
}

func TestCustomVIPLabelPrefix(t *testing.T) {
	ctx := context.Background()
	prefix := "network.example.net/vip-"
	oldKey := config.DefaultVIPLabelPrefix + "203.0.113.10"
	n := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "winner", Labels: map[string]string{oldKey: "kept"}}}
	client := fake.NewClientset(&n)
	detector := &Detector{client: client, options: Options{VIPLabelPrefix: prefix}}
	if err := detector.reconcile(ctx, []corev1.Node{n}, "203.0.113.10", "winner"); err != nil {
		t.Fatal(err)
	}
	got, err := client.CoreV1().Nodes().Get(ctx, "winner", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Labels[prefix+"203.0.113.10"] != "true" || got.Labels[oldKey] != "kept" {
		t.Fatal(got.Labels)
	}
	if err := detector.reconcile(ctx, []corev1.Node{*got}, "203.0.113.10", ""); err != nil {
		t.Fatal(err)
	}
	got, err = client.CoreV1().Nodes().Get(ctx, "winner", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Labels[prefix+"203.0.113.10"]; ok {
		t.Fatal("custom label not removed")
	}
	if got.Labels[oldKey] != "kept" {
		t.Fatal("unrelated prefix modified")
	}
}

func TestDryRunReconcileDoesNotWriteNodes(t *testing.T) {

	ip := "203.0.113.10"
	key := config.DefaultVIPLabelPrefix + ip
	old := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "old", Labels: map[string]string{key: "true"}}}
	winner := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "new"}}
	client := fake.NewClientset(&old, &winner)
	c := &Detector{
		client:  client,
		options: Options{DryRun: true, VIPLabelPrefix: config.DefaultVIPLabelPrefix},
	}
	if err := c.reconcile(context.Background(), []corev1.Node{old, winner}, ip, "new"); err != nil {
		t.Fatal(err)
	}
	if len(client.Actions()) != 0 {
		t.Fatal("dry-run mutated Kubernetes")
	}
}

func TestReconcileLabelRemovalWithDryRun(t *testing.T) {
	for _, dry := range []bool{false, true} {
		t.Run(strconv.FormatBool(dry), func(t *testing.T) {
			ip := "203.0.113.10"
			key := config.DefaultVIPLabelPrefix + ip
			n := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", Labels: map[string]string{key: "true", "other": "kept"}}}
			client := fake.NewClientset(&n)
			c := &Detector{
				client:  client,
				options: Options{VIPLabelPrefix: config.DefaultVIPLabelPrefix, DryRun: dry},
			}
			if err := c.reconcile(context.Background(), []corev1.Node{n}, ip, ""); err != nil {
				t.Fatal(err)
			}
			got, err := client.CoreV1().Nodes().Get(context.Background(), "node", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			_, has := got.Labels[key]
			if has != dry || got.Labels["other"] != "kept" {
				t.Fatal(got.Labels)
			}
		})
	}
}
