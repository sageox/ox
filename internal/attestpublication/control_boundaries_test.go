package attestpublication

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestControlRejectsOversizedRequestBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	_, request := controlResponseFixture(t)
	request.SourceRunID = strings.Repeat("x", maxControlBytes)
	client := &ControlClient{BaseURL: server.URL, HTTP: server.Client()}
	_, err := client.Create(context.Background(), "repo_test", "key", request)
	if err == nil || !strings.Contains(err.Error(), "exceeds 7168-byte limit") {
		t.Fatalf("oversized request returned %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("oversized control body reached the network")
	}
}

func TestControlRejectsBrokenHTTPResponses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		serve http.HandlerFunc
		check func(error) bool
	}{
		{
			name: "oversized",
			serve: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, strings.Repeat("x", maxControlBytes+1))
			},
			check: func(err error) bool { return strings.Contains(err.Error(), "response exceeds 7168-byte limit") },
		},
		{
			name: "truncated",
			serve: func(w http.ResponseWriter, _ *http.Request) {
				// Advertise more bytes than arrive to exercise a broken HTTP body,
				// rather than an otherwise complete but invalid JSON document.
				w.Header().Set("Content-Length", "100")
				_, _ = io.WriteString(w, `{"data":`)
			},
			check: func(err error) bool { return errors.Is(err, io.ErrUnexpectedEOF) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(tc.serve)
			t.Cleanup(server.Close)
			client := &ControlClient{BaseURL: server.URL, HTTP: server.Client()}
			run, err := client.Get(context.Background(), "repo_test", "bdd_test")
			if err == nil || !tc.check(err) {
				t.Fatalf("broken response returned %v", err)
			}
			if run.RunID != "" || run.Status != "" {
				t.Fatalf("broken response returned usable run: %+v", run)
			}
		})
	}
}

func TestControlRejectsMalformedEndpoint(t *testing.T) {
	client := &ControlClient{BaseURL: "://bad-endpoint"}
	if _, err := client.Get(context.Background(), "repo_test", "bdd_test"); err == nil {
		t.Fatal("invalid endpoint accepted")
	}
}
