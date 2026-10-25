package attestpublication

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/sageox/ox/internal/testguard"
)

// Host MIME databases must not change an immutable package's hash.
func TestPackageContentTypesIgnoreHostMappings(t *testing.T) {
	const extension = ".html"
	original := mime.TypeByExtension(extension)
	t.Cleanup(func() { _ = mime.AddExtensionType(extension, original) })
	export := testExport(t)
	before, err := BuildPackage(export, filepath.Join(t.TempDir(), "before.zip"))
	if err != nil {
		t.Fatal(err)
	}
	if err := mime.AddExtensionType(extension, "application/x-host-specific"); err != nil {
		t.Fatal(err)
	}
	after, err := BuildPackage(export, filepath.Join(t.TempDir(), "after.zip"))
	if err != nil {
		t.Fatal(err)
	}
	if before.ManifestSHA256 != after.ManifestSHA256 || before.ArchiveSHA256 != after.ArchiveSHA256 {
		t.Fatal("host MIME override changed frozen package identity")
	}
}

// Predictable temporary names must never give another process a write primitive.
func TestPublicationWritesDoNotFollowTemporarySymlinks(t *testing.T) {
	for _, kind := range []string{"archive", "journal"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "output")
			victim := filepath.Join(dir, "victim")
			want := []byte("preserve private existing data")
			if err := os.WriteFile(victim, want, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(victim, target+".tmp"); err != nil {
				t.Skipf("symlink creation unavailable: %v", err)
			}
			if kind == "archive" {
				if _, err := BuildPackage(testExport(t), target); err != nil {
					t.Fatal(err)
				}
			} else if err := SaveJournal(target, Journal{Version: 1}); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(victim)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Fatal("predictable temporary symlink overwrote victim")
			}
		})
	}
}

type failingJoinedMultipart struct {
	fakeMultipart
	started  chan struct{}
	canceled chan struct{}
	release  chan struct{}
	failed   error
	once     sync.Once
}

func (f *failingJoinedMultipart) Upload(ctx context.Context, _ Grant, _ string, number int32, _ io.Reader, _ int64) (CompletedPart, error) {
	if number == 1 {
		<-f.started
		return CompletedPart{}, f.failed
	}
	f.once.Do(func() { close(f.started) })
	<-ctx.Done()
	close(f.canceled)
	<-f.release
	return CompletedPart{}, ctx.Err()
}

// A failed part must stop and join in-flight readers before the archive is closed.
func TestMultipartFailureCancelsAndJoinsBeforeReturning(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "archive")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	transfer := &failingJoinedMultipart{started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}), failed: errors.New("part credential failure")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer close(transfer.release)
	result := make(chan error, 1)
	go func() {
		result <- uploadMissing(ctx, transfer, Grant{}, file, []missingPart{{number: 1}, {number: 2}}, &Journal{}, filepath.Join(t.TempDir(), "journal"))
	}()
	select {
	case err := <-result:
		t.Fatalf("returned before canceling and joining in-flight worker: %v", err)
	case <-transfer.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("failed upload did not cancel the other worker")
	}
	select {
	case err := <-result:
		t.Fatalf("returned before in-flight worker exited: %v", err)
	default:
	}
	// The release channel is closed by defer after verifying the join barrier.
	cancel()
	transfer.release <- struct{}{}
	if err := <-result; !errors.Is(err, transfer.failed) {
		t.Fatalf("error = %v, want original worker failure", err)
	}
}

func TestAttestPublicationLockHelper(t *testing.T) {
	target := os.Getenv("OX_TEST_ATTEST_LOCK_TARGET")
	if target == "" {
		return
	}
	lock := flock.New(target + ".lock")
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Unlock()
	fmt.Fprintln(os.Stdout, "locked")
	_, _ = io.Copy(io.Discard, os.Stdin)
}

// Independent CLI processes must not create two remote runs for one journal.
func TestPublisherHonorsCrossProcessOwnership(t *testing.T) {
	if testing.Short() {
		t.Skip("short: separate lock-holder test process")
	}
	pkg := testPackage(t)
	journalPath := filepath.Join(t.TempDir(), "journal.json")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helper := exec.Command(executable, "-test.run=^TestAttestPublicationLockHelper$")
	helper.Env = testguard.MinimalEnv([]string{"OX_TEST_ATTEST_LOCK_TARGET=" + journalPath})
	input, err := helper.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close(); _ = helper.Wait() }()
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "locked" {
		t.Fatalf("lock helper readiness = %q, %v", line, err)
	}
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"run_id": "bdd_00000000-0000-7000-8000-000000000001", "status": "rejected", "source_run_id": pkg.Run.SourceRunID, "corpus_key": pkg.Run.Corpus.Key, "created_at": "2026-01-01T00:00:00Z", "diagnostics": []any{}}})
	}))
	defer server.Close()
	publisher := Publisher{Control: &ControlClient{BaseURL: server.URL}}
	_, err = publisher.Publish(context.Background(), "repo_test", journalPath, pkg)
	if err == nil || !strings.Contains(err.Error(), "another publication") {
		t.Errorf("concurrent publish error = %v, want ownership conflict", err)
	}
	if requests != 0 {
		t.Errorf("concurrent publisher sent %d requests", requests)
	}
	if _, err := os.Stat(journalPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("concurrent publisher modified journal: %v", err)
	}
}

// Corrupt local state must fail before retrying any remote publication operation.
func TestPublishExportRejectsChangedJournalArchive(t *testing.T) {
	export := testExport(t)
	output := t.TempDir()
	pkg, err := BuildPackage(export, filepath.Join(output, "attest-run.zip"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := NewPendingJournal(pkg)
	if err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(output, "attest-upload.json")
	if err := SaveJournal(journalPath, journal); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(pkg.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 1 // Same size; a size-only resume check would miss corruption.
	if err := os.WriteFile(pkg.ArchivePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	publisher := Publisher{Control: &ControlClient{BaseURL: "http://127.0.0.1:1"}}
	if _, err := publisher.PublishExport(context.Background(), "repo_test", export, output); err == nil || !strings.Contains(err.Error(), "journaled archive changed") {
		t.Fatalf("error = %v, want immutable archive refusal", err)
	}
	after, err := os.ReadFile(pkg.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(raw) {
		t.Fatal("failed resume replaced journal-bound archive")
	}
}

// An idempotent create replay can already be processing, but that is not a final result.
func TestPublisherPollsProcessingCreateReplay(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprintf("pending=%v", pending), func(t *testing.T) {
			pkg := testPackage(t)
			journalPath := filepath.Join(t.TempDir(), "journal.json")
			if pending {
				journal, err := NewPendingJournal(pkg)
				if err != nil {
					t.Fatal(err)
				}
				if err := SaveJournal(journalPath, journal); err != nil {
					t.Fatal(err)
				}
			}
			var gets int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				status := "processing"
				if r.Method == http.MethodGet {
					gets++
					status = "published"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"run_id": "bdd_00000000-0000-7000-8000-000000000001", "status": status, "source_run_id": pkg.Run.SourceRunID, "corpus_key": pkg.Run.Corpus.Key, "created_at": "2026-01-01T00:00:00Z", "diagnostics": []any{}}})
			}))
			defer server.Close()
			publisher := Publisher{Control: &ControlClient{BaseURL: server.URL}, Sleep: func(time.Duration) {}}
			result, err := publisher.Publish(context.Background(), "repo_test", journalPath, pkg)
			if err != nil {
				t.Fatal(err)
			}
			if result.Run.Status != "published" || gets != 1 {
				t.Fatalf("status = %s, gets = %d; want published and one status read", result.Run.Status, gets)
			}
		})
	}
}

// Fast successful workers must all be persisted even when they finish before the caller receives results.
func TestMultipartPersistsAllCompletedParts(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "archive")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var work []missingPart
	for i := int32(1); i <= 12; i++ {
		work = append(work, missingPart{number: i})
	}
	journal := Journal{Version: 1, UploadID: "upload"}
	path := filepath.Join(t.TempDir(), "journal.json")
	if err := uploadMissing(context.Background(), &fakeMultipart{}, Grant{}, file, work, &journal, path); err != nil {
		t.Fatal(err)
	}
	stored, err := LoadJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Parts) != len(work) {
		t.Fatalf("persisted %d of %d completed parts", len(stored.Parts), len(work))
	}
}
