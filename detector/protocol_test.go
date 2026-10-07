package detector

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var testKey = bytes.Repeat([]byte{42}, 32)

func TestProbeIdentity(t *testing.T) {
	s := httptest.NewServer(&responder{key: testKey, node: "ovh-02", seen: map[string]time.Time{}})
	defer s.Close()
	host, port := testServerAddress(t, s.URL)
	got, err := probe(context.Background(), s.Client(), testKey, host, port, []string{"ovh-01", "ovh-02"})
	if err != nil || got != "ovh-02" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err = probe(context.Background(), s.Client(), bytes.Repeat([]byte{43}, 32), host, port, []string{"ovh-02"}); err == nil {
		t.Fatal("wrong key accepted")
	}
	if _, err = probe(context.Background(), s.Client(), testKey, host, port, []string{"unknown"}); err == nil {
		t.Fatal("unknown node accepted")
	}
}

func TestAuthenticationReplayAndPrivacy(t *testing.T) {
	s := &responder{key: testKey, node: "private-node-name", seen: map[string]time.Time{}}
	c := challenge{Timestamp: time.Now().Unix(), Nonce: strings.Repeat("a", 64), Target: "203.0.113.10:9876"}
	c.MAC = requestMAC(testKey, c)
	call := func(c challenge) *httptest.ResponseRecorder {
		body, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRecorder()
		s.ServeHTTP(r, httptest.NewRequest("POST", "/v1/probe", bytes.NewReader(body)))
		return r
	}
	r := call(c)
	if r.Code != 200 {
		t.Fatal(r.Code)
	}
	if strings.Contains(r.Body.String(), s.node) {
		t.Fatal("node name leaked")
	}
	if call(c).Code != 403 {
		t.Fatal("replay accepted")
	}
	c.Nonce = strings.Repeat("b", 64)
	if call(c).Code != 403 {
		t.Fatal("tampered nonce accepted")
	}
	c.Timestamp = time.Now().Add(-time.Minute).Unix()
	c.MAC = requestMAC(testKey, c)
	if call(c).Code != 403 {
		t.Fatal("expired challenge accepted")
	}
	c.Timestamp = time.Now().Add(time.Minute).Unix()
	c.MAC = requestMAC(testKey, c)
	if call(c).Code != 403 {
		t.Fatal("future challenge accepted")
	}
	c.Timestamp = time.Now().Unix()
	c.MAC = responseMAC(testKey, c, s.node)
	if call(c).Code != 403 {
		t.Fatal("response reflected as request")
	}
}

func TestConcurrentReplay(t *testing.T) {
	s := &responder{key: testKey, node: "node", seen: map[string]time.Time{}}
	c := challenge{Timestamp: time.Now().Unix(), Nonce: strings.Repeat("c", 64), Target: "203.0.113.10:9876"}
	c.MAC = requestMAC(testKey, c)
	body, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan int, 20)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRecorder()
			s.ServeHTTP(r, httptest.NewRequest("POST", "/v1/probe", bytes.NewReader(body)))
			results <- r.Code
		}()
	}
	wg.Wait()
	close(results)
	accepted := 0
	for code := range results {
		if code == 200 {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d copies", accepted)
	}
}

func TestMalformedAndOversized(t *testing.T) {
	s := &responder{key: testKey, node: "node", seen: map[string]time.Time{}}
	for _, body := range []string{"{} {}", strings.Repeat("x", 4096), `{"timestamp":9223372036854775807}`, `{"unknown":true}`} {
		r := httptest.NewRecorder()
		s.ServeHTTP(r, httptest.NewRequest("POST", "/v1/probe", strings.NewReader(body)))
		if r.Code != 403 {
			t.Fatal(r.Code)
		}
	}
}

func TestResponseBoundToChallenge(t *testing.T) {
	old := challenge{Timestamp: time.Now().Unix(), Nonce: strings.Repeat("d", 64), Target: "203.0.113.10:9876"}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = json.NewEncoder(w).Encode(answer{MAC: responseMAC(testKey, old, "node")})
	}))
	defer s.Close()
	host, port := testServerAddress(t, s.URL)
	if _, err := probe(context.Background(), s.Client(), testKey, host, port, []string{"node"}); err == nil {
		t.Fatal("stale response accepted")
	}
}

func TestRememberNonceExpiryAndCapacity(t *testing.T) {
	now := time.Unix(1700000000, 0)
	handler := &responder{seen: map[string]time.Time{}}
	requestChallenge := challenge{Timestamp: now.Unix(), Nonce: "first"}

	if !handler.rememberNonce(requestChallenge, now) {
		t.Fatal("fresh nonce rejected")
	}
	if handler.rememberNonce(requestChallenge, now.Add(authWindow)) {
		t.Fatal("nonce expired before the final valid timestamp second ended")
	}

	expiresAt := now.Add(authWindow + time.Second)
	requestChallenge.Timestamp = expiresAt.Unix()
	if !handler.rememberNonce(requestChallenge, expiresAt) {
		t.Fatal("expired nonce not removed")
	}

	for i := 0; len(handler.seen) < maxRememberedNonces; i++ {
		handler.seen[strconv.Itoa(i)] = expiresAt.Add(authWindow + time.Second)
	}
	requestChallenge.Nonce = "next"
	if handler.rememberNonce(requestChallenge, expiresAt) {
		t.Fatal("nonce accepted with a full replay cache")
	}
	if len(handler.seen) != maxRememberedNonces {
		t.Fatal("replay cache exceeded capacity")
	}
}

func testServerAddress(t *testing.T, serverURL string) (string, int) {
	t.Helper()
	host, portString, err := net.SplitHostPort(strings.TrimPrefix(serverURL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}
