package attestpublication

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublishExportKeepsJournalInLockedDirectoryAfterAncestorReplacement(t *testing.T) {
	parent := t.TempDir()
	original := filepath.Join(parent, "original")
	output := filepath.Join(original, "output")
	moved := filepath.Join(parent, "moved")
	replacement := filepath.Join(parent, "replacement")
	if err := os.MkdirAll(filepath.Join(replacement, "output"), 0700); err != nil {
		t.Fatal(err)
	}
	sentinelPath := filepath.Join(replacement, "output", "attest-upload.json")
	const sentinel = "private unrelated file"
	if err := os.WriteFile(sentinelPath, []byte(sentinel), 0600); err != nil {
		t.Fatal(err)
	}
	var request CreateRequest
	response, _ := controlResponseFixture(t)
	data := response["data"].(map[string]any)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/completions") {
			delete(data, "upload")
			data["status"] = "published"
			_ = json.NewEncoder(w).Encode(response)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if err := os.Rename(original, moved); err != nil {
			t.Error(err)
			return
		}
		if err := os.Symlink(replacement, original); err != nil {
			t.Error(err)
			return
		}
		raw, err := os.ReadFile(filepath.Join(moved, "output", "attest-run.zip"))
		if err != nil {
			t.Error(err)
			return
		}
		raw[0] ^= 1
		if err := os.WriteFile(filepath.Join(replacement, "output", "attest-run.zip"), raw, 0600); err != nil {
			t.Error(err)
			return
		}
		data["source_run_id"], data["corpus_key"] = request.SourceRunID, request.CorpusKey
		upload := data["upload"].(map[string]any)
		upload["expires_at"], upload["intent_expires_at"] = "2099-01-01T00:00:00Z", "2099-01-01T01:00:00Z"
		upload["binding"] = Binding{ManifestSHA256: request.ManifestSHA256, ArchiveSHA256: request.ArchiveSHA256, ArchiveSizeBytes: request.ArchiveSizeBytes}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	publisher := Publisher{Control: &ControlClient{BaseURL: server.URL, HTTP: server.Client()}, Transfer: &bindingCheckingMultipart{}}
	if _, err := publisher.PublishExport(context.Background(), "repo_test", testExport(t), output); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != sentinel {
		t.Fatal("publication journal escaped locked directory and overwrote replacement")
	}
	journal, err := LoadJournal(filepath.Join(moved, "output", "attest-upload.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !journal.Completed {
		t.Fatal("original locked journal was not completed")
	}
}

// The replaced directory contains a same-sized archive with different bytes.
// Uploads must read the handle opened under the original publication lock.
type bindingCheckingMultipart struct{ fakeMultipart }

func (f *bindingCheckingMultipart) Upload(_ context.Context, grant Grant, _ string, number int32, body io.Reader, size int64) (CompletedPart, error) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return CompletedPart{}, err
	}
	if digest(raw) != grant.Binding.ArchiveSHA256 {
		return CompletedPart{}, fmt.Errorf("upload followed replaced archive path")
	}
	return CompletedPart{Number: number, ETag: "verified", Size: size}, nil
}

func TestPublishExportRejectsSymlinkedOutputComponent(t *testing.T) {
	parent := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(parent, "link")); err != nil {
		t.Skip(err)
	}
	_, err := (Publisher{}).PublishExport(context.Background(), "repo_test", testExport(t), filepath.Join(parent, "link", "output"))
	if err == nil || !strings.Contains(err.Error(), "not a real directory") {
		t.Fatalf("error=%v, want output symlink rejection", err)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("symlink target mutated before output validation")
	}
}
