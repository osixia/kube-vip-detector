package detector

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/osixia/container-baseimage/log"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

// VIP workers and leader election
// =============================

// One Lease per VIP, shared by all eligible nodes.
func leaseName(ip string) string {
	return fmt.Sprintf("kube-network-detector-%x", sha256.Sum256([]byte(ip)))[:43]
}

func (d *Detector) run(ctx context.Context, ip string) {

	runWhenRemote(ctx, ip, d.options.Interval, d.isLocal, func(workerCtx context.Context) {
		if d.options.DryRun {
			d.observe(workerCtx, ip)
			return
		}

		d.elect(workerCtx, ip, func(leaderCtx context.Context) {
			d.observe(leaderCtx, ip)
		})
	})
}

// Cancel and join a worker before it can be restarted. Interface errors are
// treated as ineligibility, never as evidence that the VIP is remote.
func runWhenRemote(ctx context.Context, ip string, interval time.Duration, check func(string) (bool, error), work func(context.Context)) {

	var cancel context.CancelFunc
	var done chan struct{}
	stop := func() {
		if cancel != nil {
			cancel()
			<-done
			cancel = nil
			done = nil
		}
	}

	defer stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		local, err := check(ip)
		if err != nil {
			log.Warningf("cannot inspect interfaces; election suspended vip=%q error=%q", ip, err)
		}

		if local || err != nil {
			stop()
		} else if cancel == nil {
			workerCtx, workerCancel := context.WithCancel(ctx)
			cancel = workerCancel
			done = make(chan struct{})
			finished := done
			go func() {
				defer close(finished)
				work(workerCtx)
			}()
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-done:
			cancel()
			cancel = nil
			done = nil
		}
	}
}

func (d *Detector) elect(ctx context.Context, ip string, work func(context.Context)) {
	for ctx.Err() == nil {
		leading := make(chan context.Context, 1)
		stopped := make(chan struct{})
		lock := &resourcelock.LeaseLock{
			LeaseMeta:  metav1.ObjectMeta{Name: leaseName(ip), Namespace: d.namespace},
			Client:     d.client.CoordinationV1(),
			LockConfig: resourcelock.ResourceLockConfig{Identity: d.identity},
		}

		elector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
			Lock:          lock,
			LeaseDuration: 30 * time.Second,
			RenewDeadline: 20 * time.Second,
			RetryPeriod:   5 * time.Second,
			// Do not release before outstanding work has stopped. Expiry is the handover mechanism.
			ReleaseOnCancel: false,
			Callbacks: leaderelection.LeaderCallbacks{
				OnStartedLeading: func(leaderCtx context.Context) {
					leading <- leaderCtx
				},
				OnStoppedLeading: func() {},
			},
		})
		if err != nil {
			log.Errorf("leader election configuration: %v", err)
			return
		}

		electionCtx, cancelElection := context.WithCancel(ctx)
		go func() {
			defer close(stopped)
			elector.Run(electionCtx)
		}()
		select {
		case leaderCtx := <-leading:
			work(leaderCtx)
		case <-stopped:
		case <-ctx.Done():
		}

		cancelElection()
		<-stopped
		if !pause(ctx, 5*time.Second) {
			return
		}
	}
}
