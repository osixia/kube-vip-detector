package detector

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"

	"github.com/osixia/container-baseimage/log"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

// Peers use name=IP:port; names provide stable labels when endpoints change.
type peer struct {
	name    string
	address string
}

func parsePeer(value string) (peer, error) {
	name, address, ok := strings.Cut(value, "=")
	if !ok || name == "" {
		return peer{}, fmt.Errorf("invalid peer %q: expected name=IP:port", value)
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return peer{}, fmt.Errorf("invalid peer %q: %w", value, err)
	}
	ip, err := netip.ParseAddr(host)
	port, portErr := strconv.Atoi(portText)
	if err != nil || ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() || portErr != nil || port < 1 || port > 65535 {
		return peer{}, fmt.Errorf("invalid peer %q: unicast IP and port 1–65535 required", value)
	}
	return peer{name: name, address: net.JoinHostPort(ip.String(), strconv.Itoa(port))}, nil
}

func (o Options) validatePeers() error {
	seen := map[string]bool{}
	for _, value := range o.Peers {
		p, err := parsePeer(value)
		if err != nil {
			return err
		}
		if o.PeerLabelPrefix == "" {
			return fmt.Errorf("--peer-label-prefix must not be empty")
		}
		if problems := validation.IsQualifiedName(o.PeerLabelPrefix + p.name); len(problems) != 0 {
			return fmt.Errorf("invalid peer label: %s", strings.Join(problems, "; "))
		}
		if seen[p.name] {
			return fmt.Errorf("duplicate peer name %q", p.name)
		}
		seen[p.name] = true
		for _, ip := range o.VIPs {
			if o.PeerLabelPrefix+p.name == vipLabel(o.VIPLabelPrefix, ip) {
				return fmt.Errorf("peer and VIP labels must be distinct")
			}
		}
	}
	return nil
}

// Every node observes every peer, independently of VIP eligibility and election.
func (d *Detector) runPeers(ctx context.Context) {
	var workers sync.WaitGroup
	for _, value := range d.options.Peers {
		p, _ := parsePeer(value) // New has validated and cloned options.
		workers.Add(1)
		go func() { defer workers.Done(); d.observePeer(ctx, p) }()
	}
	workers.Wait()
}

func (d *Detector) observePeer(ctx context.Context, p peer) {
	var state observation
	dialer := net.Dialer{Timeout: d.options.Timeout}
	for ctx.Err() == nil {
		conn, err := dialer.DialContext(ctx, "tcp", p.address)
		if conn != nil {
			_ = conn.Close()
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Warningf("peer probe failed peer=%q address=%q error=%q", p.name, p.address, err)
		}
		apply, winner := state.update(d.node, err, d.options.SuccessThreshold, d.options.FailureThreshold)
		if apply {
			if err := d.reconcilePeer(ctx, p, winner != ""); err != nil && ctx.Err() == nil {
				log.Errorf("peer label update failed peer=%q error=%q", p.name, err)
			}
		}
		if !pause(ctx, d.options.Interval) {
			return
		}
	}
}

func (d *Detector) reconcilePeer(ctx context.Context, p peer, reachable bool) error {
	node, err := d.client.CoreV1().Nodes().Get(ctx, d.node, metav1.GetOptions{})
	if err != nil {
		return err
	}
	key := d.options.PeerLabelPrefix + p.name
	current, exists := node.Labels[key]
	if (reachable && current == "true") || (!reachable && !exists) {
		return nil
	}
	var value any
	action := "remove"
	if reachable {
		value = "true"
		action = "set"
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if d.options.DryRun {
		log.Infof("dry-run: proposed peer label change peer=%q node=%q action=%q label=%q", p.name, d.node, action, key)
		return nil
	}
	if err := patchLabel(ctx, d.client, *node, key, value); err != nil {
		return err
	}
	log.Infof("peer label changed peer=%q node=%q action=%q label=%q", p.name, d.node, action, key)
	return nil
}
