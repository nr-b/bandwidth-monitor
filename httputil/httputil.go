// Package httputil provides shared HTTP helpers: User-Agent injection,
// TLS-skipping client factory, JSON response helpers, and SSE streaming.
package httputil

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"sync"
	"time"

	"bandwidth-monitor/version"
)

// Transport wraps an existing http.RoundTripper and injects the User-Agent
// header on every outgoing request.
type Transport struct {
	// Base is the underlying RoundTripper. If nil, http.DefaultTransport is used.
	Base http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("User-Agent", version.UserAgent())
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// WrapTransport is a convenience that wraps an existing RoundTripper (which
// may be nil) in a Transport that sets the User-Agent header.
func WrapTransport(base http.RoundTripper) *Transport {
	return &Transport{Base: base}
}

// NewInsecureClient creates an http.Client with TLS verification disabled,
// a cookie jar, and the User-Agent transport wrapper. Used by controller
// integrations (UniFi, Omada, Pi-hole) that talk to self-signed endpoints.
func NewInsecureClient(timeout time.Duration) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{
		Timeout: timeout,
		Jar:     jar,
		Transport: WrapTransport(&http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}),
	}
}

// NewClient creates a standard http.Client with User-Agent injection.
func NewClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: WrapTransport(nil),
	}
}

// ── JSON response helpers ─────────────────────────────────────────

// WriteJSON writes v as JSON to w with the appropriate Content-Type header.
func WriteJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// WriteJSONOrNull writes v as JSON, or the literal "null" if v is nil.
func WriteJSONOrNull(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if v == nil {
		w.Write([]byte("null"))
		return
	}
	json.NewEncoder(w).Encode(v)
}

// ── SSE streaming helper ──────────────────────────────────────────

// SSEStream sets Server-Sent Events headers on w and drains ch,
// marshalling each value as a "data: {...}\n\n" frame.
// Returns when ch is closed.
func SSEStream(w http.ResponseWriter, ch interface{ recv() ([]byte, bool) }) {
	// This is a type-agnostic version; see StreamChannel below.
}

// StreamChannel sets SSE headers, checks for Flusher support, and streams
// JSON-encoded values from ch until it closes.
func StreamChannel[T any](w http.ResponseWriter, ch <-chan T) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	for p := range ch {
		data, _ := json.Marshal(p)
		w.Write([]byte("data: "))
		w.Write(data)
		w.Write([]byte("\n\n"))
		flusher.Flush()
	}
}

// ── Single-flight guard ───────────────────────────────────────────

// SingleFlight is a simple mutex-based guard that ensures only one operation
// runs at a time. Returns false (and writes a 409 Conflict) if busy.
type SingleFlight struct {
	mu      sync.Mutex
	running bool
}

// TryAcquire attempts to acquire the lock. If already running, writes a
// 409 Conflict JSON response and returns false. On success, returns true
// and the caller must call Release() when done (typically via defer).
func (sf *SingleFlight) TryAcquire(w http.ResponseWriter, label string) bool {
	sf.mu.Lock()
	if sf.running {
		sf.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"error": label + " already running"})
		return false
	}
	sf.running = true
	sf.mu.Unlock()
	return true
}

// Release marks the operation as complete.
func (sf *SingleFlight) Release() {
	sf.mu.Lock()
	sf.running = false
	sf.mu.Unlock()
}
