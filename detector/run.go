package detector

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/osixia/container-baseimage/log"
)

// Detector lifecycle
// =============================

// Run serves authenticated probes and observes configured VIPs until ctx is canceled.
// The caller owns signal handling and the Kubernetes client's configuration.
func (d *Detector) Run(parent context.Context) error {

	ctx, stop := context.WithCancel(parent)
	defer stop()

	options := d.options
	vips := options.VIPs
	key := d.key
	node := d.node

	// Authenticated probe server
	server := &http.Server{
		Addr:              net.JoinHostPort("", strconv.Itoa(options.Port)),
		Handler:           &responder{key: key, node: node, seen: map[string]time.Time{}},
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       3 * time.Second,
		WriteTimeout:      3 * time.Second,
		IdleTimeout:       5 * time.Second,
		MaxHeaderBytes:    4096,
	}

	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return err
	}

	defer func() {
		_ = listener.Close()
	}()

	// Start servers and VIP workers
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(listener)
	}()

	var workers sync.WaitGroup
	for _, ip := range vips {
		workers.Add(1)
		go func(ip string) {
			defer workers.Done()
			d.run(ctx, ip)
		}(ip)
	}

	log.Infof("started node=%q targets=%d port=%d dryRun=%t", node, len(vips), options.Port, options.DryRun)
	select {
	case <-ctx.Done():
	case err = <-serveErr:
	}

	// Stop servers and wait for workers
	stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)

	workers.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}

	return err
}
