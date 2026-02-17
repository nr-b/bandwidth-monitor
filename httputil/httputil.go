// Package httputil provides a shared User-Agent transport wrapper so every
// outgoing HTTP request identifies itself as bandwidth-monitor.
package httputil

import (
	"net/http"

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
