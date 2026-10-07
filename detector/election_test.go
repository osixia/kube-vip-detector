package detector

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/osixia/kube-vip-detector/config"
)

func TestEligibilityTransitions(t *testing.T) {
	var state atomic.Int32 // 0 local, 1 remote, 2 unreadable
	checked := make(chan struct{}, 1)
	started := make(chan struct{}, 4)
	stopped := make(chan struct{}, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	defer func() {
		cancel()
		<-done
	}()
	go func() {
		defer close(done)
		runWhenRemote(ctx, "203.0.113.10", 5*time.Millisecond,
			func(string) (bool, error) {
				select {
				case checked <- struct{}{}:
				default:
				}
				switch state.Load() {
				case 0:
					return true, nil
				case 1:
					return false, nil
				default:
					return false, fmt.Errorf("blocked")
				}
			},
			func(workerCtx context.Context) {
				started <- struct{}{}
				<-workerCtx.Done()
				stopped <- struct{}{}
			},
		)
	}()
	await := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatal("transition timed out")
		}
	}
	await(checked)
	select {
	case <-started:
		t.Fatal("local VIP entered election")
	default:
	}
	state.Store(1)
	await(started)
	state.Store(0)
	await(stopped)
	state.Store(1)
	await(started)
	state.Store(2)
	await(stopped)
	state.Store(1)
	await(started)
	cancel()
	await(stopped)
}

func TestDryRunWorkerNoLeaseOrNodeWrites(t *testing.T) {

	cycleCompleted := make(chan struct{})
	var requests atomic.Int32
	handler := &responder{key: testKey, node: "identified-node", seen: map[string]time.Time{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		w.WriteHeader(response.Code)
		_, _ = w.Write(response.Body.Bytes())
		// A second authenticated probe proves the first observation cycle has completed.
		if response.Code == http.StatusOK && requests.Add(1) == 2 {
			close(cycleCompleted)
		}
	}))
	defer server.Close()
	host, port := testServerAddress(t, server.URL)
	client := fake.NewClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "identified-node"}})
	c := &Detector{
		client: client,
		key:    testKey,
		options: Options{
			Port:             port,
			Interval:         5 * time.Millisecond,
			Timeout:          time.Second,
			SuccessThreshold: 1,
			FailureThreshold: 1,
			DryRun:           true,
			VIPLabelPrefix:   config.DefaultVIPLabelPrefix,
		},
		checkLocal: func(string) (bool, error) {
			return false, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.run(ctx, host)
	}()
	defer func() {
		cancel()
		<-done
	}()
	select {
	case <-cycleCompleted:
	case <-time.After(3 * time.Second):
		t.Fatal("probe cycle never completed")
	}
	cancel()
	<-done
	for _, action := range client.Actions() {
		if action.GetVerb() != "list" {
			t.Fatalf("unexpected Kubernetes action: %s %s", action.GetVerb(), action.GetResource())
		}
	}
}
