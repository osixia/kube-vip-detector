package detector_test

import (
	"context"
	"strings"
	"time"

	"github.com/osixia/kube-network-detector/detector"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func ExampleNew() {
	// Supply your application's Kubernetes configuration and credentials.
	client, err := kubernetes.NewForConfig(&rest.Config{Host: "https://kubernetes.example.net"})
	if err != nil {
		return
	}
	service, err := detector.New(client, detector.Identity{
		Node:      "node-1",
		Namespace: "networking",
		PodUID:    "unique-instance-id",
	}, detector.Options{
		VIPs:             []string{"203.0.113.10"},
		Key:              strings.Repeat("a", 64), // Use a random shared key in your application.
		VIPLabelPrefix:   "example.net/vip-",
		Port:             9876,
		Interval:         5 * time.Second,
		Timeout:          2 * time.Second,
		SuccessThreshold: 2,
		FailureThreshold: 3,
	})
	if err != nil {
		return
	}

	// Cancel this context when your application shuts down.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = service.Run(ctx)
}
