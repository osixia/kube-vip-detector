// Package detector identifies nodes receiving IPv4 VIP traffic and maintains
// Kubernetes node labels using authenticated probes and per-VIP leader election.
package detector

import (
	"encoding/hex"
	"errors"
	"slices"

	"k8s.io/client-go/kubernetes"
)

// Identity identifies the responding node and its leader-election participant.
type Identity struct {
	Node      string
	Namespace string
	PodUID    string
}

// Detector serves probes and manages VIP observations. Construct it with New.
type Detector struct {
	client kubernetes.Interface
	key    []byte

	node      string
	namespace string
	identity  string

	options Options

	checkLocal func(string) (bool, error)
}

// New validates the configuration and snapshots it for a detector.
// client is supplied by the caller and is not created or reconfigured here.
func New(client kubernetes.Interface, identity Identity, options Options) (*Detector, error) {

	if client == nil {
		return nil, errors.New("kubernetes client is required")
	}
	if identity.Node == "" || identity.Namespace == "" || identity.PodUID == "" {
		return nil, errors.New("node, namespace and pod UID are required")
	}
	if err := options.Validate(); err != nil {
		return nil, err
	}

	key, err := hex.DecodeString(options.Key)
	if err != nil {
		return nil, err
	}
	options.VIPs = slices.Clone(options.VIPs)

	return &Detector{
		client:    client,
		key:       key,
		node:      identity.Node,
		namespace: identity.Namespace,
		identity:  identity.Node + "/" + identity.PodUID,
		options:   options,
	}, nil
}

func (d *Detector) isLocal(ip string) (bool, error) {

	if d.checkLocal != nil {
		return d.checkLocal(ip)
	}

	return localIP(ip)
}
