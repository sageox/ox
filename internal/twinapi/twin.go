// Package twinapi provides a digital twin of the SageOx cloud API for testing.
//
// Each Twin instance runs two HTTP servers on ephemeral ports: an API server that
// replicates the real SageOx auth surface (device flow, JWT exchange, userinfo,
// token refresh, revocation) and an admin server for test control (user creation,
// fault injection, clock manipulation, call recording).
//
// The ox CLI points at the API port via SAGEOX_ENDPOINT and cannot distinguish
// the twin from the real cloud API.
package twinapi

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

// Twin is a test double for the SageOx cloud API.
type Twin struct {
	APIURL   string // "http://127.0.0.1:PORT" — what ox points at
	AdminURL string // "http://127.0.0.1:PORT2" — test control

	store     *store
	apiServer *http.Server
	admServer *http.Server
}

// Start creates and starts a new Twin. Both servers are shut down via t.Cleanup.
// Each invocation gets isolated state and a fresh JWT signing key.
func Start(t testing.TB) *Twin {
	t.Helper()

	tw := &Twin{
		store: newStore(),
	}

	// bind API listener
	apiLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("twinapi: failed to listen for api: %v", err)
	}
	tw.APIURL = "http://" + apiLn.Addr().String()

	// bind admin listener
	admLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = apiLn.Close()
		t.Fatalf("twinapi: failed to listen for admin: %v", err)
	}
	tw.AdminURL = "http://" + admLn.Addr().String()

	tw.apiServer = &http.Server{Handler: tw.apiMux()}
	tw.admServer = &http.Server{Handler: tw.adminMux()}

	go func() { _ = tw.apiServer.Serve(apiLn) }()
	go func() { _ = tw.admServer.Serve(admLn) }()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tw.apiServer.Shutdown(ctx)
		_ = tw.admServer.Shutdown(ctx)
	})

	return tw
}

// Env returns environment variables that point ox at this twin.
func (tw *Twin) Env() []string {
	return []string{"SAGEOX_ENDPOINT=" + tw.APIURL}
}

// URL is an alias for APIURL.
func (tw *Twin) URL() string {
	return tw.APIURL
}
