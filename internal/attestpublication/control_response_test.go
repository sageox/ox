package attestpublication

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func controlResponseFixture(t *testing.T) (map[string]any, CreateRequest) {
	t.Helper()
	raw, err := os.ReadFile("contract/testdata/conformance/valid/create-run-response-initial-grant.json")
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	data := envelope["data"].(map[string]any)
	binding := data["upload"].(map[string]any)["binding"].(map[string]any)
	return envelope, CreateRequest{SourceRunID: data["source_run_id"].(string), CorpusKey: data["corpus_key"].(string), PackageVersion: "1", ManifestSHA256: binding["manifest_sha256"].(string), ArchiveSHA256: binding["archive_sha256"].(string), ArchiveSizeBytes: int64(binding["archive_size_bytes"].(float64))}
}

func controlResponseClient(t *testing.T, raw []byte) *ControlClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(raw)
	}))
	t.Cleanup(server.Close)
	return &ControlClient{BaseURL: server.URL, HTTP: server.Client()}
}

func TestControlRejectsIncompleteSuccessfulResponses(t *testing.T) {
	cases := []struct{ name, raw string }{
		{"empty", ""}, {"whitespace", " \n"}, {"null", "null"}, {"missing-data", `{}`}, {"null-data", `{"data":null}`}, {"empty-data", `{"data":{}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := controlResponseClient(t, []byte(tc.raw))
			_, request := controlResponseFixture(t)
			if _, err := client.Create(context.Background(), "repo_test", "key", request); err == nil {
				t.Fatal("malformed successful create response accepted")
			}
			if _, err := client.Renew(context.Background(), "repo_test", "bdd_test"); err == nil {
				t.Fatal("malformed successful renewal response accepted")
			}
			if _, err := client.Complete(context.Background(), "repo_test", "bdd_test"); err == nil {
				t.Fatal("malformed successful completion response accepted")
			}
			if _, err := client.Get(context.Background(), "repo_test", "bdd_test"); err == nil {
				t.Fatal("malformed successful status response accepted")
			}
		})
	}
}

func TestControlCreateValidatesRunIdentityAndGrant(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing-id", func(d map[string]any) { delete(d, "run_id") }},
		{"malformed-id", func(d map[string]any) { d["run_id"] = "bdd_------------------------------------" }},
		{"non-v7-id", func(d map[string]any) { d["run_id"] = "bdd_01990000-0000-4000-8000-000000000001" }},
		{"missing-status", func(d map[string]any) { delete(d, "status") }},
		{"unknown-status", func(d map[string]any) { d["status"] = "complete" }},
		{"different-source", func(d map[string]any) { d["source_run_id"] = "another-run" }},
		{"different-corpus", func(d map[string]any) { d["corpus_key"] = "another-corpus" }},
		{"missing-upload", func(d map[string]any) { delete(d, "upload") }},
		{"empty-upload", func(d map[string]any) { d["upload"] = map[string]any{} }},
		{"unexpected-upload", func(d map[string]any) { d["status"] = "published" }},
		{"wrong-binding", func(d map[string]any) {
			d["upload"].(map[string]any)["binding"].(map[string]any)["archive_sha256"] = strings.Repeat("f", 64)
		}},
		{"empty-credential", func(d map[string]any) {
			d["upload"].(map[string]any)["credentials"].(map[string]any)["session_token"] = ""
		}},
		{"missing-created-at", func(d map[string]any) { delete(d, "created_at") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response, request := controlResponseFixture(t)
			tc.mutate(response["data"].(map[string]any))
			raw, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := controlResponseClient(t, raw).Create(context.Background(), "repo_test", "key", request); err == nil {
				t.Fatal("inconsistent create response accepted")
			}
		})
	}
}

func TestControlAcceptsValidStatusSpecificResponses(t *testing.T) {
	for _, status := range []string{"uploading", "processing", "published", "rejected", "deleted"} {
		t.Run(status, func(t *testing.T) {
			response, request := controlResponseFixture(t)
			data := response["data"].(map[string]any)
			data["status"] = status
			if status != "uploading" {
				delete(data, "upload")
			}
			raw, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			run, err := controlResponseClient(t, raw).Create(context.Background(), "repo_test", "key", request)
			if err != nil {
				t.Fatal(err)
			}
			if run.Status != status || run.SourceRunID != request.SourceRunID {
				t.Fatal("valid create response changed identity")
			}
		})
	}
}

func TestControlRunResponseMatchesRequestedRun(t *testing.T) {
	response, _ := controlResponseFixture(t)
	data := response["data"].(map[string]any)
	delete(data, "upload")
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	client := controlResponseClient(t, raw)
	for _, method := range []struct {
		name string
		call func(context.Context, string, string) (Run, error)
	}{{"get", client.Get}, {"complete", client.Complete}} {
		t.Run(method.name, func(t *testing.T) {
			if _, err := method.call(context.Background(), "repo_test", "bdd_01990000-0000-7000-8000-000000000002"); err == nil {
				t.Fatal("response for a different run accepted")
			}
			if _, err := method.call(context.Background(), "repo_test", data["run_id"].(string)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPublisherDoesNotCompleteJournalForMalformedCreate(t *testing.T) {
	pkg := testPackage(t)
	journalPath := filepath.Join(t.TempDir(), "journal.json")
	publisher := Publisher{Control: controlResponseClient(t, []byte(`{"data":{}}`)), Transfer: &fakeMultipart{}, Sleep: func(time.Duration) {}}
	if _, err := publisher.Publish(context.Background(), "repo_test", journalPath, pkg); err == nil {
		t.Fatal("publisher reported success without a hosted run")
	}
	journal, err := LoadJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Completed || journal.RunID != "" {
		t.Fatal("malformed response committed completed journal state")
	}
}
