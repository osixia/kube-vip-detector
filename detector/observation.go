package detector

import (
	"context"
	"fmt"
	"net"
	"net/http"

	"github.com/osixia/container-baseimage/log"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// VIP observations
// =============================

type observation struct {
	candidate string
	successes int
	failures  int
}

func (state *observation) update(node string, err error, successThreshold, failureThreshold int) (bool, string) {

	if err != nil {
		state.candidate = ""
		state.successes = 0
		if state.failures < failureThreshold {
			state.failures++
		}
		return state.failures >= failureThreshold, ""
	}

	state.failures = 0
	if node != state.candidate {
		state.candidate = node
		state.successes = 0
	}

	if state.successes < successThreshold {
		state.successes++
	}
	return state.successes >= successThreshold, node
}

func (d *Detector) observe(ctx context.Context, ip string) {

	log.Infof("observing VIP vip=%q probeNode=%q dryRun=%t", ip, d.node, d.options.DryRun)

	transport := &http.Transport{
		Proxy:             nil,
		DisableKeepAlives: true,
		DialContext:       (&net.Dialer{Timeout: d.options.Timeout}).DialContext,
	}
	defer transport.CloseIdleConnections()

	probeClient := &http.Client{
		Transport: transport,
		Timeout:   d.options.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	var state observation
	for ctx.Err() == nil {
		// A locally configured VIP never measures the provider's inbound routing.
		local, err := d.isLocal(ip)
		if err != nil || local {
			log.Infof("stopping observation: VIP local or interfaces unreadable vip=%q error=%q", ip, fmt.Sprint(err))
			return
		}

		if !d.observeOnce(ctx, probeClient, ip, &state) {
			return
		}
		if !pause(ctx, d.options.Interval) {
			return
		}
	}
}

func (d *Detector) observeOnce(ctx context.Context, probeClient *http.Client, ip string, state *observation) bool {

	nodes, err := d.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		// API failures are not evidence that a VIP stopped working.
		log.Errorf("cannot list nodes: %v", err)
		*state = observation{}
		return true
	}

	names := make([]string, 0, len(nodes.Items))
	for _, node := range nodes.Items {
		names = append(names, node.Name)
	}

	node, probeErr := probe(ctx, probeClient, d.key, ip, d.options.Port, names)
	if ctx.Err() != nil {
		return false
	}
	if probeErr != nil {
		log.Warningf("VIP probe failed vip=%q error=%q", ip, probeErr)
	}

	// Eligibility can change while a probe is in flight.
	if local, err := d.isLocal(ip); err != nil || local {
		return false
	}

	apply, winner := state.update(node, probeErr, d.options.SuccessThreshold, d.options.FailureThreshold)
	if !apply || ctx.Err() != nil {
		return true
	}
	if err := d.reconcile(ctx, nodes.Items, ip, winner); err != nil {
		log.Errorf("label update failed vip=%q error=%q", ip, err)
	}
	return true
}
