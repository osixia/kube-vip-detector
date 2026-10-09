package detector_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/osixia/kube-network-detector/detector"
	"k8s.io/client-go/kubernetes/fake"
)

func TestRunReturnsListenError(t *testing.T) {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := listener.Close(); err != nil {
			t.Errorf("close listener: %v", err)
		}
	})

	client := fake.NewClientset()
	service, err := detector.New(client, detector.Identity{
		Node: "node", Namespace: "namespace", PodUID: "instance",
	}, detector.Options{
		VIPs:             []string{"203.0.113.10"},
		Key:              strings.Repeat("a", 64),
		VIPLabelPrefix:   "example.net/vip-",
		Port:             listener.Addr().(*net.TCPAddr).Port,
		Interval:         time.Second,
		Timeout:          time.Second,
		SuccessThreshold: 1,
		FailureThreshold: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = service.Run(ctx)
	var listenError *net.OpError
	if !errors.As(err, &listenError) || listenError.Op != "listen" {
		t.Fatalf("expected listen error, got %v", err)
	}
	if len(client.Actions()) != 0 {
		t.Fatal("workers started despite server startup failure")
	}
}
