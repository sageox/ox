// Package httpisolate stops one test's httptest server from breaking
// another test's HTTP requests.
//
// Why this exists: httptest.Server.Close calls CloseIdleConnections on
// http.DefaultTransport, which every http.Client without its own Transport
// shares. That closes pooled connections and cancels dials belonging to
// other parallel tests, whose requests then fail with "http:
// CloseIdleConnections called" and surface as a network error.
//
// How to apply: a test package whose parallel tests hit httptest servers
// through clients built on the default transport adds a TestMain that calls
// DefaultTransport().
package httpisolate

import "net/http"

// roundTripperOnly hides CloseIdleConnections so httptest skips it.
type roundTripperOnly struct{ http.RoundTripper }

// DefaultTransport replaces http.DefaultTransport with a copy that httptest
// servers cannot close. Call it from TestMain before m.Run.
func DefaultTransport() {
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		http.DefaultTransport = roundTripperOnly{t.Clone()}
	}
}
