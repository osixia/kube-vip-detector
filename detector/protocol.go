package detector

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Probe protocol
// =============================

const (
	authWindow          = 30 * time.Second
	probePath           = "/v1/probe"
	nonceBytes          = 32
	maxBodyBytes        = 2048
	maxTargetBytes      = 64
	maxRememberedNonces = 10000
)

type challenge struct {
	Timestamp int64  `json:"timestamp"`
	Nonce     string `json:"nonce"`
	Target    string `json:"target"`
	MAC       string `json:"mac"`
}

type answer struct {
	MAC string `json:"mac"`
}

// Authentication and decoding
// =============================

// JSON array framing prevents ambiguous concatenations; domain separation prevents reflection.
func signature(key []byte, fields ...string) string {

	payload, _ := json.Marshal(fields)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func requestMAC(key []byte, requestChallenge challenge) string {
	return signature(key, "v1/request", http.MethodPost, probePath, strconv.FormatInt(requestChallenge.Timestamp, 10), requestChallenge.Nonce, requestChallenge.Target)
}

func responseMAC(key []byte, requestChallenge challenge, node string) string {
	return signature(key, "v1/response", strconv.FormatInt(requestChallenge.Timestamp, 10), requestChallenge.Nonce, requestChallenge.Target, node)
}

func equalMAC(left, right string) bool {

	leftBytes, err := hex.DecodeString(left)
	if err != nil || len(leftBytes) != sha256.Size {
		return false
	}

	rightBytes, err := hex.DecodeString(right)
	return err == nil && hmac.Equal(leftBytes, rightBytes)
}

func decodeJSON(reader io.Reader, value any) error {

	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}

	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("unexpected trailing data")
	}

	return nil
}

// Probe responder
// =============================

type responder struct {
	key  []byte
	node string
	mu   sync.Mutex
	seen map[string]time.Time
}

func (responder *responder) ServeHTTP(w http.ResponseWriter, r *http.Request) {

	w.Header().Set("Cache-Control", "no-store")
	deny := func() {
		http.Error(w, "forbidden", http.StatusForbidden)
	}

	if r.Method != http.MethodPost || r.URL.Path != probePath || r.URL.RawQuery != "" {
		deny()
		return
	}

	var requestChallenge challenge
	if err := decodeJSON(http.MaxBytesReader(w, r.Body, maxBodyBytes), &requestChallenge); err != nil {
		deny()
		return
	}

	now := time.Now()
	// Compare seconds without overflowing a duration supplied by an untrusted client.
	if requestChallenge.Timestamp < now.Unix()-int64(authWindow/time.Second) || requestChallenge.Timestamp > now.Unix()+int64(authWindow/time.Second) {
		deny()
		return
	}

	nonce, err := hex.DecodeString(requestChallenge.Nonce)
	if err != nil || len(nonce) != nonceBytes || len(requestChallenge.Target) > maxTargetBytes || !equalMAC(requestChallenge.MAC, requestMAC(responder.key, requestChallenge)) {
		deny()
		return
	}

	if !responder.rememberNonce(requestChallenge, now) {
		deny()
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(answer{MAC: responseMAC(responder.key, requestChallenge, responder.node)})
}

// Accept each nonce once and retain it until its entire authentication window expires.
func (responder *responder) rememberNonce(requestChallenge challenge, now time.Time) bool {

	responder.mu.Lock()
	defer responder.mu.Unlock()

	for nonce, expiresAt := range responder.seen {
		if !now.Before(expiresAt) {
			delete(responder.seen, nonce)
		}
	}

	if _, replay := responder.seen[requestChallenge.Nonce]; replay {
		return false
	}
	if len(responder.seen) >= maxRememberedNonces {
		return false
	}

	responder.seen[requestChallenge.Nonce] = time.Unix(requestChallenge.Timestamp, 0).Add(authWindow + time.Second)
	return true
}

// VIP probe
// =============================

func probe(ctx context.Context, client *http.Client, key []byte, ip string, port int, nodes []string) (string, error) {

	nonce := make([]byte, nonceBytes)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}

	requestChallenge := challenge{
		Timestamp: time.Now().Unix(),
		Nonce:     hex.EncodeToString(nonce),
		Target:    net.JoinHostPort(ip, strconv.Itoa(port)),
	}

	requestChallenge.MAC = requestMAC(key, requestChallenge)
	payload, _ := json.Marshal(requestChallenge)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+requestChallenge.Target+probePath, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}

	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}

	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("probe HTTP status %d", response.StatusCode)
	}

	var responseAnswer answer
	if err := decodeJSON(io.LimitReader(response.Body, maxBodyBytes+1), &responseAnswer); err != nil {
		return "", err
	}

	for _, node := range nodes {
		if equalMAC(responseAnswer.MAC, responseMAC(key, requestChallenge, node)) {
			return node, nil
		}
	}

	return "", errors.New("unrecognized response signature")
}
