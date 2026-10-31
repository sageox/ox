package attestpublication

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sageox/ox/internal/useragent"
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

// Failure prevented: authenticated attest control requests must carry actor identity while preserving their request and response contract.
func TestControlRequestCarriesActorHeaders(t *testing.T) {
	useragent.ResetForTesting()
	t.Cleanup(useragent.ResetForTesting)
	useragent.SetActor("agent", "codex")

	response, input := controlResponseFixture(t)
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/repos/repo_test/attest/runs" {
			t.Errorf("unexpected control route: %s %s", r.Method, r.URL.Path)
		}
		for header, want := range map[string]string{
			useragent.HeaderActor:     "agent",
			useragent.HeaderAgentType: "codex",
			"Authorization":           "Bearer test-token",
			"Content-Type":            "application/json",
			"Idempotency-Key":         "test-key",
		} {
			if got := r.Header.Get(header); got != want {
				t.Errorf("%s = %q, want %q", header, got, want)
			}
		}
		var received CreateRequest
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode control request: %v", err)
		} else if received != input {
			t.Errorf("control request changed: got %+v, want %+v", received, input)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(raw)
	}))
	t.Cleanup(server.Close)

	client := &ControlClient{BaseURL: server.URL, Token: "test-token", HTTP: server.Client()}
	run, err := client.Create(context.Background(), "repo_test", "test-key", input)
	if err != nil {
		t.Fatal(err)
	}
	if run.RunID != response["data"].(map[string]any)["run_id"] || run.Status != "uploading" || run.SourceRunID != input.SourceRunID || run.CorpusKey != input.CorpusKey || run.Upload == nil {
		t.Fatalf("control response changed: %+v", run)
	}
	if run.Upload.Binding != (Binding{ManifestSHA256: input.ManifestSHA256, ArchiveSHA256: input.ArchiveSHA256, ArchiveSizeBytes: input.ArchiveSizeBytes}) {
		t.Fatalf("control response changed package binding: %+v", run.Upload.Binding)
	}
}
