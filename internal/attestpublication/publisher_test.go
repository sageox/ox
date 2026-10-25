package attestpublication

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testExport(t *testing.T) string {
	t.Helper()
	export := t.TempDir()
	raw, err := os.ReadFile("contract/testdata/conformance/valid/run-all-outcomes.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(export, runFilename), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(export, "report"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(export, "report", "index.html"), []byte("<h1>frozen</h1>"), 0o600); err != nil {
		t.Fatal(err)
	}
	return export
}

func testPackage(t *testing.T) Package {
	t.Helper()
	pkg, err := BuildPackage(testExport(t), filepath.Join(t.TempDir(), "package.zip"))
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

func TestBuildPackageIsDeterministicAndRejectsBadRun(t *testing.T) {
	export := testExport(t)
	pkg, err := BuildPackage(export, filepath.Join(t.TempDir(), "first.zip"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildPackage(export, filepath.Join(t.TempDir(), "second.zip"))
	if err != nil {
		t.Fatal(err)
	}
	if pkg.ManifestSHA256 != second.ManifestSHA256 || pkg.ArchiveSHA256 != second.ArchiveSHA256 {
		t.Fatal("equivalent frozen export changed package identity")
	}
	if pkg.ManifestSHA256 == pkg.ArchiveSHA256 {
		t.Fatal("manifest and archive identities must remain distinct")
	}
	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, runFilename), []byte(`{"schema_version":"not-supported"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildPackage(bad, filepath.Join(t.TempDir(), "bad.zip")); err == nil {
		t.Fatal("invalid required run metadata packaged")
	}
}

func TestVendoredContractMatchesPinnedManifest(t *testing.T) {
	if !contractSourceDirty {
		t.Fatal("the current contract did not come from the named commit; provenance must remain explicit until the contract is committed")
	}
	raw, err := os.ReadFile("contract/contract-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if got := digest(raw); got != contractManifestSHA {
		t.Fatalf("contract manifest digest = %s, want %s; re-export and update the authoritative content digest", got, contractManifestSHA)
	}
	var manifest struct {
		Files []struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		} `json:"files"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, file := range manifest.Files {
		content, err := os.ReadFile(filepath.Join("contract", filepath.FromSlash(file.Path)))
		if err != nil {
			t.Fatal(err)
		}
		if got := digest(content); got != file.SHA256 {
			t.Fatalf("vendored contract file %s has digest %s, want %s", file.Path, got, file.SHA256)
		}
	}
}

type fakeMultipart struct {
	mu        sync.Mutex
	parts     map[int32]CompletedPart
	completed bool
}

func (f *fakeMultipart) Create(context.Context, Grant) (string, error) { return "upload-1", nil }
func (f *fakeMultipart) List(context.Context, Grant, string) ([]CompletedPart, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]CompletedPart, 0, len(f.parts))
	for _, part := range f.parts {
		result = append(result, part)
	}
	return result, nil
}
func (f *fakeMultipart) Upload(_ context.Context, _ Grant, _ string, number int32, body io.Reader, size int64) (CompletedPart, error) {
	if _, err := io.ReadAll(body); err != nil {
		return CompletedPart{}, err
	}
	part := CompletedPart{Number: number, ETag: "etag", Size: size}
	f.mu.Lock()
	if f.parts == nil {
		f.parts = map[int32]CompletedPart{}
	}
	f.parts[number] = part
	f.mu.Unlock()
	return part, nil
}
func (f *fakeMultipart) Complete(context.Context, Grant, string, []CompletedPart) error {
	f.completed = true
	return nil
}
func (f *fakeMultipart) Completed(context.Context, Grant) (bool, error) { return f.completed, nil }

func TestPublisherUsesInitialGrantWithoutRenewal(t *testing.T) {
	pkg := testPackage(t)
	var creates, renewals, completions int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/repos/repo_test/attest/runs":
			creates++
			if got := r.Header.Get("Idempotency-Key"); len(got) != 32 || got == pkg.ArchiveSHA256 {
				t.Errorf("idempotency key is not an independent persisted nonce")
			}
			var request CreateRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if request.ArchiveSHA256 != pkg.ArchiveSHA256 {
				t.Error("wrong archive binding")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"run_id": "bdd_00000000-0000-7000-8000-000000000001", "status": "uploading", "source_run_id": pkg.Run.SourceRunID, "corpus_key": pkg.Run.Corpus.Key, "created_at": "2026-01-01T00:00:00Z", "diagnostics": []any{}, "upload": map[string]any{"bucket": "bucket", "key": "staging/key", "region": "us-west-2", "credentials": map[string]string{"access_key_id": "id", "secret_access_key": "secret", "session_token": "token"}, "expires_at": "2099-01-01T00:15:00Z", "intent_expires_at": "2099-01-01T01:00:00Z", "binding": map[string]any{"manifest_sha256": pkg.ManifestSHA256, "archive_sha256": pkg.ArchiveSHA256, "archive_size_bytes": pkg.ArchiveSizeBytes}}}})
		case "/api/v1/repos/repo_test/attest/runs/bdd_00000000-0000-7000-8000-000000000001/completions":
			completions++
			if r.ContentLength != 0 || r.Header.Get("Content-Type") != "" {
				t.Errorf("bodyless completion had length %d and Content-Type %q", r.ContentLength, r.Header.Get("Content-Type"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"run_id": "bdd_00000000-0000-7000-8000-000000000001", "status": "published", "source_run_id": pkg.Run.SourceRunID, "corpus_key": pkg.Run.Corpus.Key, "created_at": "2026-01-01T00:00:00Z", "diagnostics": []any{}, "url": "https://example.test/runs/1"}})
		default:
			if bytes.Contains([]byte(r.URL.Path), []byte("upload-grants")) {
				renewals++
			}
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	transfer := &fakeMultipart{}
	publisher := Publisher{Control: &ControlClient{BaseURL: server.URL, Token: "token", HTTP: server.Client()}, Transfer: transfer, Sleep: func(time.Duration) {}}
	result, err := publisher.Publish(context.Background(), "repo_test", filepath.Join(t.TempDir(), "journal.json"), pkg)
	if err != nil {
		t.Fatal(err)
	}
	if result.Run.Status != "published" || creates != 1 || renewals != 0 || completions != 1 || !transfer.completed {
		t.Fatalf("unexpected publication state: %+v creates=%d renewals=%d completions=%d", result.Run, creates, renewals, completions)
	}
}

func TestPublisherResumesCompletedObjectByCompletingUploadingRun(t *testing.T) {
	pkg := testPackage(t)
	initial := Grant{Bucket: "bucket", Key: "staging/key", Region: "us-west-2", Credentials: Credentials{AccessKeyID: "id", SecretAccessKey: "secret", SessionToken: "token"}, ExpiresAt: time.Date(2099, 1, 1, 0, 15, 0, 0, time.UTC), Binding: Binding{
		ManifestSHA256:   pkg.ManifestSHA256,
		ArchiveSHA256:    pkg.ArchiveSHA256,
		ArchiveSizeBytes: pkg.ArchiveSizeBytes,
	}, IntentExpiresAt: time.Date(2099, 1, 1, 1, 0, 0, 0, time.UTC)}
	journal, err := NewJournal(Run{
		RunID:       "bdd_00000000-0000-7000-8000-000000000001",
		SourceRunID: pkg.Run.SourceRunID,
		Upload:      &initial,
	}, pkg)
	if err != nil {
		t.Fatal(err)
	}
	journal.Completed = true // S3 persisted completion before the control completion request.
	journalPath := filepath.Join(t.TempDir(), "journal.json")
	if err := SaveJournal(journalPath, journal); err != nil {
		t.Fatal(err)
	}

	var gets, completions int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/repos/repo_test/attest/runs/" + journal.RunID:
			gets++
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"run_id": journal.RunID, "status": "uploading", "source_run_id": pkg.Run.SourceRunID, "corpus_key": pkg.Run.Corpus.Key, "created_at": "2026-01-01T00:00:00Z", "diagnostics": []any{},
			}})
		case "/api/v1/repos/repo_test/attest/runs/" + journal.RunID + "/completions":
			completions++
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"run_id": journal.RunID, "status": "published", "source_run_id": pkg.Run.SourceRunID, "corpus_key": pkg.Run.Corpus.Key, "created_at": "2026-01-01T00:00:00Z", "diagnostics": []any{},
			}})
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	publisher := Publisher{
		Control:  &ControlClient{BaseURL: server.URL, HTTP: server.Client()},
		Transfer: &fakeMultipart{},
		Sleep:    func(time.Duration) {},
	}
	result, err := publisher.Publish(context.Background(), "repo_test", journalPath, pkg)
	if err != nil {
		t.Fatal(err)
	}
	if result.Run.Status != "published" || gets != 1 || completions != 1 {
		t.Fatalf("resume status=%q gets=%d completions=%d, want published, 1, 1", result.Run.Status, gets, completions)
	}
}

func TestPublisherResumesAfterControlCompletionFailure(t *testing.T) {
	pkg := testPackage(t)
	journalPath := filepath.Join(t.TempDir(), "journal.json")
	var creates, gets, completions int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/repos/repo_test/attest/runs":
			creates++
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"run_id": "bdd_00000000-0000-7000-8000-000000000001", "status": "uploading", "source_run_id": pkg.Run.SourceRunID, "corpus_key": pkg.Run.Corpus.Key, "created_at": "2026-01-01T00:00:00Z", "diagnostics": []any{},
				"upload": map[string]any{
					"bucket": "bucket", "key": "staging/key", "region": "us-west-2",
					"credentials": map[string]string{"access_key_id": "id", "secret_access_key": "secret", "session_token": "token"},
					"expires_at":  "2099-01-01T00:15:00Z", "intent_expires_at": "2099-01-01T01:00:00Z",
					"binding": map[string]any{"manifest_sha256": pkg.ManifestSHA256, "archive_sha256": pkg.ArchiveSHA256, "archive_size_bytes": pkg.ArchiveSizeBytes},
				},
			}})
		case "/api/v1/repos/repo_test/attest/runs/bdd_00000000-0000-7000-8000-000000000001":
			gets++
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"run_id": "bdd_00000000-0000-7000-8000-000000000001", "status": "uploading", "source_run_id": pkg.Run.SourceRunID, "corpus_key": pkg.Run.Corpus.Key, "created_at": "2026-01-01T00:00:00Z", "diagnostics": []any{},
			}})
		case "/api/v1/repos/repo_test/attest/runs/bdd_00000000-0000-7000-8000-000000000001/completions":
			completions++
			if completions == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"run_id": "bdd_00000000-0000-7000-8000-000000000001", "status": "published", "source_run_id": pkg.Run.SourceRunID, "corpus_key": pkg.Run.Corpus.Key, "created_at": "2026-01-01T00:00:00Z", "diagnostics": []any{},
			}})
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	publisher := Publisher{
		Control:  &ControlClient{BaseURL: server.URL, HTTP: server.Client()},
		Transfer: &fakeMultipart{},
		Sleep:    func(time.Duration) {},
	}
	if _, err := publisher.Publish(context.Background(), "repo_test", journalPath, pkg); err == nil {
		t.Fatal("initial publication unexpectedly survived the control completion failure")
	}
	stored, err := LoadJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.Completed {
		t.Fatal("S3 completion was not retained for recovery")
	}
	result, err := publisher.Publish(context.Background(), "repo_test", journalPath, pkg)
	if err != nil {
		t.Fatal(err)
	}
	if result.Run.Status != "published" || creates != 1 || gets != 1 || completions != 2 {
		t.Fatalf("resume status=%q creates=%d gets=%d completions=%d, want published, 1, 1, 2", result.Run.Status, creates, gets, completions)
	}
}

func TestPublisherCompletedJournalDoesNotRecompleteTerminalRun(t *testing.T) {
	pkg := testPackage(t)
	for _, status := range []string{"published", "rejected"} {
		t.Run(status, func(t *testing.T) {
			journal := Journal{
				Version:     1,
				RunID:       "bdd_00000000-0000-7000-8000-000000000001",
				SourceRunID: pkg.Run.SourceRunID,
				ArchivePath: pkg.ArchivePath,
				Binding: Binding{
					ManifestSHA256:   pkg.ManifestSHA256,
					ArchiveSHA256:    pkg.ArchiveSHA256,
					ArchiveSizeBytes: pkg.ArchiveSizeBytes,
				},
				Completed: true,
			}
			journalPath := filepath.Join(t.TempDir(), "journal.json")
			if err := SaveJournal(journalPath, journal); err != nil {
				t.Fatal(err)
			}

			var completions int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/repos/repo_test/attest/runs/" + journal.RunID:
					_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
						"run_id": journal.RunID, "status": status, "source_run_id": pkg.Run.SourceRunID, "corpus_key": pkg.Run.Corpus.Key, "created_at": "2026-01-01T00:00:00Z", "diagnostics": []any{},
					}})
				case "/api/v1/repos/repo_test/attest/runs/" + journal.RunID + "/completions":
					completions++
					w.WriteHeader(http.StatusInternalServerError)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			publisher := Publisher{Control: &ControlClient{BaseURL: server.URL, HTTP: server.Client()}, Transfer: &fakeMultipart{}}
			result, err := publisher.Publish(context.Background(), "repo_test", journalPath, pkg)
			if err != nil {
				t.Fatal(err)
			}
			if result.Run.Status != status || completions != 0 {
				t.Fatalf("resume status=%q completions=%d, want %q and 0", result.Run.Status, completions, status)
			}
		})
	}
}

func TestPublisherResumeRejectsChangedRenewalBeforeMultipart(t *testing.T) {
	pkg := testPackage(t)
	for _, tc := range []struct {
		name   string
		change func(*Grant)
	}{
		{"binding", func(grant *Grant) { grant.Binding.ArchiveSizeBytes++ }},
		{"deadline", func(grant *Grant) { grant.IntentExpiresAt = grant.IntentExpiresAt.Add(time.Hour) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initial := Grant{Bucket: "bucket", Key: "staging/key", Region: "us-west-2", Credentials: Credentials{AccessKeyID: "id", SecretAccessKey: "secret", SessionToken: "token"}, ExpiresAt: time.Date(2099, 1, 1, 0, 15, 0, 0, time.UTC), Binding: Binding{
				ManifestSHA256:   pkg.ManifestSHA256,
				ArchiveSHA256:    pkg.ArchiveSHA256,
				ArchiveSizeBytes: pkg.ArchiveSizeBytes,
			}, IntentExpiresAt: time.Date(2099, 1, 1, 1, 0, 0, 0, time.UTC)}
			journal, err := NewJournal(Run{
				RunID:       "bdd_00000000-0000-7000-8000-000000000001",
				SourceRunID: pkg.Run.SourceRunID,
				Upload:      &initial,
			}, pkg)
			if err != nil {
				t.Fatal(err)
			}
			journalPath := filepath.Join(t.TempDir(), "journal.json")
			if err := SaveJournal(journalPath, journal); err != nil {
				t.Fatal(err)
			}

			changed := initial
			tc.change(&changed)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/repos/repo_test/attest/runs/"+journal.RunID+"/upload-grants" {
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": changed})
			}))
			defer server.Close()
			transfer := &fakeMultipart{}
			publisher := Publisher{
				Control:  &ControlClient{BaseURL: server.URL, HTTP: server.Client()},
				Transfer: transfer,
			}
			if _, err := publisher.Publish(context.Background(), "repo_test", journalPath, pkg); err == nil {
				t.Fatal("resume accepted a renewal with a changed immutable invariant")
			}
			if transfer.parts != nil || transfer.completed {
				t.Fatal("multipart transfer started before renewal invariants were checked")
			}
		})
	}
}

func TestPublisherPersistsCreateIdempotencyKeyBeforeRequest(t *testing.T) {
	pkg := testPackage(t)
	journalPath := filepath.Join(t.TempDir(), "journal.json")
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		if len(keys) <= 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"retry","message":"retry"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"run_id": "bdd_00000000-0000-7000-8000-000000000001", "status": "rejected", "source_run_id": pkg.Run.SourceRunID,
			"corpus_key": pkg.Run.Corpus.Key, "created_at": "2026-01-01T00:00:00Z", "diagnostics": []any{},
		}})
	}))
	defer server.Close()
	publisher := Publisher{Control: &ControlClient{BaseURL: server.URL, Token: "secret-not-journaled", HTTP: server.Client()}, Transfer: &fakeMultipart{}, Sleep: func(time.Duration) {}}
	if _, err := publisher.Publish(context.Background(), "repo_test", journalPath, pkg); err == nil {
		t.Fatal("first publication unexpectedly survived all create failures")
	}
	if _, err := publisher.Publish(context.Background(), "repo_test", journalPath, pkg); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 4 || keys[0] == "" {
		t.Fatalf("create attempts = %d, want four with a key", len(keys))
	}
	for _, key := range keys[1:] {
		if key != keys[0] {
			t.Fatal("create retry changed its persisted idempotency key")
		}
	}
	raw, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("secret-not-journaled")) {
		t.Fatal("journal persisted bearer credentials")
	}
}
