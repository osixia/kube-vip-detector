package detector

import (
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	identity := Identity{Node: "node", Namespace: "namespace", PodUID: "instance"}
	options := validOptions()
	options.VIPs = []string{"203.0.113.10"}
	client := fake.NewClientset()

	for _, test := range []struct {
		name     string
		client   kubernetes.Interface
		identity Identity
		options  Options
	}{
		{"missing client", nil, identity, options},
		{"missing node", client, Identity{Namespace: identity.Namespace, PodUID: identity.PodUID}, options},
		{"missing namespace", client, Identity{Node: identity.Node, PodUID: identity.PodUID}, options},
		{"missing pod UID", client, Identity{Node: identity.Node, Namespace: identity.Namespace}, options},
		{"invalid options", client, identity, Options{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.client, test.identity, test.options); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}

}

func TestNewSnapshotsOptions(t *testing.T) {
	identity := Identity{Node: "node", Namespace: "namespace", PodUID: "instance"}
	options := validOptions()
	options.VIPs = []string{"203.0.113.10"}
	client := fake.NewClientset()

	service, err := New(client, identity, options)
	if err != nil {
		t.Fatal(err)
	}
	options.VIPs[0] = "198.51.100.20"
	options.Port++
	if service.options.VIPs[0] != "203.0.113.10" || service.options.Port == options.Port {
		t.Fatal("detector configuration changed with caller options")
	}
}
