package detector

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestPeerValidation(t *testing.T) {
	for _, value := range []string{"db=10.0.0.20:5432", "api=[2001:db8::1]:443"} {
		o := validOptions()
		o.Peers = []string{value}
		o.PeerLabelPrefix = "example.net/peer-"
		if err := o.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, peers := range [][]string{{"db=example.net:443"}, {"db=10.0.0.20:0"}, {"db=10.0.0.20:65536"}, {"db=0.0.0.0:443"}, {"db=[ff02::1]:443"}, {"=10.0.0.20:443"}, {"db=10.0.0.20:443", "db=10.0.0.21:443"}} {
		o := validOptions()
		o.Peers = peers
		o.PeerLabelPrefix = "example.net/peer-"
		if err := o.Validate(); err == nil {
			t.Fatalf("accepted %v", peers)
		}
	}
	o := validOptions()
	o.VIPs = nil
	o.Key = ""
	o.Peers = []string{"db=10.0.0.20:5432"}
	o.PeerLabelPrefix = "example.net/peer-"
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	o.VIPs = []string{"203.0.113.10"}
	if err := o.Validate(); err == nil {
		t.Fatal("VIPs without a key accepted")
	}
}

func TestPeerLabelsLocalAndDryRun(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "local", Labels: map[string]string{"unrelated": "keep"}}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "other", Labels: map[string]string{"example.net/peer-db": "true"}}},
	)
	o := validOptions()
	o.PeerLabelPrefix = "example.net/peer-"
	d, err := New(client, Identity{Node: "local", Namespace: "ns", PodUID: "uid"}, o)
	if err != nil {
		t.Fatal(err)
	}
	p := peer{name: "db"}
	for _, reachable := range []bool{true, false} {
		if err := d.reconcilePeer(ctx, p, reachable); err != nil {
			t.Fatal(err)
		}
		node, err := client.CoreV1().Nodes().Get(ctx, "local", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		_, present := node.Labels["example.net/peer-db"]
		if present != reachable || node.Labels["unrelated"] != "keep" {
			t.Fatal(node.Labels)
		}
	}
	other, err := client.CoreV1().Nodes().Get(ctx, "other", metav1.GetOptions{})
	if err != nil || other.Labels["example.net/peer-db"] != "true" {
		t.Fatal("other node changed")
	}
	d.options.DryRun = true
	client.ClearActions()
	if err := d.reconcilePeer(ctx, p, true); err != nil {
		t.Fatal(err)
	}
	for _, action := range client.Actions() {
		if action.GetVerb() != "get" {
			t.Fatal("dry-run wrote to API")
		}
	}
}

func TestPeerThresholds(t *testing.T) {
	o := validOptions()
	var state observation
	for i, err := range []error{nil, nil, errors.New("failed"), nil, errors.New("failed"), errors.New("failed"), errors.New("failed")} {
		apply, winner := state.update("local", err, o.SuccessThreshold, o.FailureThreshold)
		if apply != (i == 1 || i == 6) {
			t.Fatalf("cycle %d: apply=%t", i, apply)
		}
		if i == 6 && winner != "" {
			t.Fatal("failed peer has winner")
		}
	}
}

func TestPeersOnlyLifecycle(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := listener.Close(); err != nil {
			t.Errorf("close listener: %v", err)
		}
	})
	client := fake.NewClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "local"}})
	o := validOptions()
	o.VIPs = nil
	o.Key = "ignored-invalid-key"
	o.Port = 0
	o.VIPLabelPrefix = "invalid prefix"
	o.Peers = []string{"db=" + listener.Addr().String()}
	o.PeerLabelPrefix = "example.net/peer-"
	o.Interval = time.Millisecond
	o.SuccessThreshold = 1
	d, err := New(client, Identity{Node: "local"}, o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	deadline := time.After(3 * time.Second)
	for {
		node, err := client.CoreV1().Nodes().Get(ctx, "local", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if node.Labels["example.net/peer-db"] == "true" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("peer was not labeled")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not finish")
	}
	for _, action := range client.Actions() {
		if action.GetResource().Resource != "nodes" {
			t.Fatal("peers-only used election")
		}
	}
}

func TestModesDerivedFromTargets(t *testing.T) {
	o := validOptions()
	o.PeerLabelPrefix = "invalid unused prefix"
	if err := o.Validate(); err != nil {
		t.Fatalf("VIP-only validates unused peer settings: %v", err)
	}
	o.VIPs = nil
	if err := o.Validate(); err == nil {
		t.Fatal("empty target lists accepted")
	}
	o.Peers = []string{"db=10.0.0.20:5432"}
	o.PeerLabelPrefix = "example.net/peer-"
	o.Key = "ignored-invalid-key"
	o.Port = 0
	o.VIPLabelPrefix = "ignored invalid prefix"
	d, err := New(fake.NewClientset(), Identity{Node: "node"}, o)
	if err != nil {
		t.Fatalf("peer-only requires VIP settings: %v", err)
	}
	if len(d.key) != 0 {
		t.Fatal("peer-only decoded VIP key")
	}
	o.Interval = 0
	if err := o.Validate(); err == nil {
		t.Fatal("peer-only accepted zero interval")
	}
}
