package detector

import (
	"context"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestVIPIPv6Validation(t *testing.T) {
	for _, ip := range []string{"2001:db8::1", "2001:db8::", "2001:db8:1:2:3:4:5:6"} {
		o := validOptions()
		o.VIPs = append(o.VIPs, ip)
		if err := o.Validate(); err != nil {
			t.Fatalf("%s: %v", ip, err)
		}
	}
	for _, ip := range []string{"::", "::1", "ff02::1", "fe80::1", "fe80::1%eth0", "::ffff:192.0.2.1", "2001:0db8::1", "2001:DB8::1", "[2001:db8::1]"} {
		o := validOptions()
		o.VIPs = []string{ip}
		if err := o.Validate(); err == nil {
			t.Fatalf("accepted %s", ip)
		}
	}
	o := validOptions()
	o.VIPs = []string{"2001:db8::1"}
	o.VIPLabelPrefix = "example.net/" + strings.Repeat("x", 27)
	if err := o.Validate(); err == nil {
		t.Fatal("oversized IPv6 label accepted")
	}
	o.VIPLabelPrefix = "example.net/" + strings.Repeat("x", 26)
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	o.Peers = []string{"ipv6-20010db8000000000000000000000001=192.0.2.1:443"}
	o.PeerLabelPrefix = o.VIPLabelPrefix
	if err := o.Validate(); err == nil {
		t.Fatal("peer/VIP collision accepted")
	}
}

func TestVIPIPv6Labels(t *testing.T) {
	ctx := context.Background()
	ip := "2001:db8::1"
	key := "example.net/ipv6-20010db8000000000000000000000001"
	if vipLabel("example.net/", ip) != key {
		t.Fatal(vipLabel("example.net/", ip))
	}
	if vipLabel("example.net/", "203.0.113.10") != "example.net/203.0.113.10" {
		t.Fatal("IPv4 label changed")
	}
	client := fake.NewClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", Labels: map[string]string{"unrelated": "keep"}}})
	d := &Detector{client: client, options: Options{VIPLabelPrefix: "example.net/"}}
	for _, winner := range []string{"node", ""} {
		nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err := d.reconcile(ctx, nodes.Items, ip, winner); err != nil {
			t.Fatal(err)
		}
		node, err := client.CoreV1().Nodes().Get(ctx, "node", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		_, exists := node.Labels[key]
		if exists != (winner != "") || node.Labels["unrelated"] != "keep" {
			t.Fatal(node.Labels)
		}
	}
}

func TestLocalIPv6(t *testing.T) {
	list := func() ([]net.Addr, error) {
		return []net.Addr{&net.IPNet{IP: net.ParseIP("2001:db8::1"), Mask: net.CIDRMask(64, 128)}}, nil
	}
	for _, ip := range []string{"2001:db8::1", "2001:db8::2"} {
		local, err := localIPUsing(ip, list)
		if err != nil || local != (ip == "2001:db8::1") {
			t.Fatalf("%s: %t %v", ip, local, err)
		}
	}
}

func TestIPv6VIPAndPeerProbes(t *testing.T) {
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	server := httptest.NewUnstartedServer(&responder{key: testKey, node: "node", seen: map[string]time.Time{}})
	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()
	defer server.Close()
	host, port := testServerAddress(t, server.URL)
	node, err := probe(context.Background(), server.Client(), testKey, host, port, []string{"node"})
	if err != nil || node != "node" {
		t.Fatalf("%q %v", node, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := fake.NewClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node"}})
	o := validOptions()
	o.VIPs = nil
	o.Key = ""
	o.Peers = []string{"api=" + listener.Addr().String()}
	o.PeerLabelPrefix = "example.net/peer-"
	o.SuccessThreshold = 1
	o.Interval = time.Millisecond
	d, err := New(client, Identity{Node: "node"}, o)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	defer func() { cancel(); <-done }()
	deadline := time.After(3 * time.Second)
	for {
		node, err := client.CoreV1().Nodes().Get(ctx, "node", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if node.Labels["example.net/peer-api"] == "true" {
			return
		}
		select {
		case <-deadline:
			t.Fatal("IPv6 peer label missing")
		case <-time.After(time.Millisecond):
		}
	}
}
