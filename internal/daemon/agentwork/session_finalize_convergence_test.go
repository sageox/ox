package agentwork

// Convergence tests for the session-finalize anti-entropy loop.
//
// WHY THIS FILE EXISTS
//
// The daemon wedged for months while the suite stayed green, because the suite
// tested Detect() and ProcessResult() as separate units and never asserted the
// property that actually matters: after processing a session, the next Detect()
// must not find it again. A handler can return the right value on every call and
// still never converge — that is exactly what happened, ~897 times a day.
//
// Two further blind spots made it invisible:
//
//   - Fixture bias. Every upload fixture wrote meta.json by hand, but nothing on
//     the upload path creates one, and Detect() uses meta.json as its completion
//     marker. Production sessions therefore never satisfied the detector, while
//     the tests always did.
//   - No outcome assertion. ProcessResult returning nil was read as success even
//     when the commit had failed, so a total failure logged "status=success".
//
// The rule these tests encode: assert the SYSTEM settled, not that a function
// returned.

import (
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sageox/ox/internal/lfs"
)

// writeProductionShapedSession creates a cache session in the shape the daemon
// actually encounters: fully summarized artifacts, and NO meta.json. Fixtures
// that pre-write meta.json cannot observe the wedge.
func writeProductionShapedSession(t *testing.T, ledgerPath, sessionName string) string {
	t.Helper()
	cacheDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", sessionName)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "raw.jsonl"), []byte(testRawContent), 0644); err != nil {
		t.Fatal(err)
	}
	for _, name := range requiredArtifacts {
		if err := os.WriteFile(filepath.Join(cacheDir, name), []byte("artifact content"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return cacheDir
}

func newGitBackedHandler() *SessionFinalizeHandler {
	h := NewSessionFinalizeHandler(slog.Default())
	h.skipLFS = true
	h.skipGit = false
	h.ledgerMu = &sync.Mutex{}
	return h
}

// TestAntiEntropy_ConvergesAfterTransientPushFailure is the test whose absence
// let the wedge run for months. It asserts the loop's defining property across
// TWO cycles: once a session's content is in git, a later pass must settle it
// rather than re-detecting it forever.
//
// The two cycles matter, and a single-pass test cannot substitute. Pass 1 with a
// broken remote is how the wedged state is actually born: `git commit` succeeds,
// so the content lands in git, but the push fails, so the cache is deliberately
// preserved (that preservation is correct — it is the only copy). From then on
// every pass stages nothing, `git commit` exits 1, and the old code read that as
// failure and never pruned. Pass 2 with a working remote is the cycle that must
// converge and, before the fix, never did.
func TestAntiEntropy_ConvergesAfterTransientPushFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	barePath, clonePath := setupBareAndCloneLedger(t)
	sessionName := "2026-01-15T15-00-testuser-OxCONV"
	writeProductionShapedSession(t, clonePath, sessionName)

	handler := newGitBackedHandler()

	// --- cycle 1: the remote is down. Commit lands locally, push does not.
	runGitCmd(t, clonePath, "remote", "set-url", "origin", "/nonexistent/broken/remote.git")

	first, err := handler.Detect(clonePath)
	if err != nil {
		t.Fatalf("cycle 1 Detect: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("cycle 1 Detect found %d items, want 1 — fixture is not reaching the upload path", len(first))
	}
	if err := handler.ProcessResult(first[0], &RunResult{}); err == nil {
		t.Fatal("cycle 1 reported success while the push was failing")
	}

	cacheDir := filepath.Join(clonePath, ".sageox", "cache", "sessions", sessionName)
	if _, err := os.Stat(cacheDir); err != nil {
		t.Fatalf("DATALOSS: cache pruned after a failed push — it is the only copy: %v", err)
	}

	// --- cycle 2: the remote is back. Content is already at HEAD, so nothing
	// stages. This is the cycle that repeated ~114 times a day per session.
	runGitCmd(t, clonePath, "remote", "set-url", "origin", "file://"+barePath)

	second, err := handler.Detect(clonePath)
	if err != nil {
		t.Fatalf("cycle 2 Detect: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("cycle 2 Detect found %d items, want 1 — the session should still be pending", len(second))
	}
	if err := handler.ProcessResult(second[0], &RunResult{}); err != nil {
		t.Fatalf("cycle 2 ProcessResult: %v", err)
	}

	// --- cycle 3: the loop must be over.
	third, err := handler.Detect(clonePath)
	if err != nil {
		t.Fatalf("cycle 3 Detect: %v", err)
	}
	if len(third) != 0 {
		t.Errorf("session still detected after its content reached git (%d items) — the daemon "+
			"will re-queue it every 5 minutes forever", len(third))
	}
}

// TestProcessUploadOnly_WritesMetaWhenAbsent pins the specific reason
// convergence failed: meta.json is Detect()'s completion marker, but the upload
// path only refreshed one that already existed.
func TestProcessUploadOnly_WritesMetaWhenAbsent(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	_, clonePath := setupBareAndCloneLedger(t)
	sessionName := "2026-01-15T16-00-testuser-OxMETA"
	cacheDir := writeProductionShapedSession(t, clonePath, sessionName)

	handler := newGitBackedHandler()
	item := &WorkItem{
		ID:   "test-meta-synthesis",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: cacheDir,
			RawPath:    filepath.Join(cacheDir, "raw.jsonl"),
			LedgerPath: clonePath,
			UploadOnly: true,
		},
	}

	if err := handler.ProcessResult(item, &RunResult{}); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	metaPath := filepath.Join(clonePath, "sessions", sessionName, "meta.json")
	if _, err := os.Stat(metaPath); err != nil {
		t.Fatalf("meta.json not written to the ledger: %v", err)
	}

	meta, err := readMetaForTest(metaPath)
	if err != nil {
		t.Fatalf("meta.json unreadable: %v", err)
	}
	if meta.SessionName != sessionName {
		t.Errorf("meta.session_name = %q, want %q", meta.SessionName, sessionName)
	}
	// Identity is recoverable from the session name; a summary is not, and must
	// not be invented. An absent summary is honest — a fabricated one would be
	// indistinguishable from a real one downstream.
	if meta.Summary != "" || meta.Title != "" {
		t.Errorf("synthesized meta fabricated user-visible content: title=%q summary=%q", meta.Title, meta.Summary)
	}
}

// TestProcessUploadOnly_FailedPushIsReportedAsFailure guards the honesty
// property. ProcessResult returning nil on failure is what let 897 failed
// commits per day surface as "agent work complete status=success".
func TestProcessUploadOnly_FailedPushIsReportedAsFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	_, clonePath := setupBareAndCloneLedger(t)
	runGitCmd(t, clonePath, "remote", "set-url", "origin", "/nonexistent/broken/remote.git")

	sessionName := "2026-01-15T17-00-testuser-OxHONEST"
	cacheDir := writeProductionShapedSession(t, clonePath, sessionName)

	handler := newGitBackedHandler()
	item := &WorkItem{
		ID:   "test-failure-is-reported",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: cacheDir,
			RawPath:    filepath.Join(cacheDir, "raw.jsonl"),
			LedgerPath: clonePath,
			UploadOnly: true,
		},
	}

	if err := handler.ProcessResult(item, &RunResult{}); err == nil {
		t.Error("ProcessResult returned nil after a failed push — the manager records this as a success")
	}
}

// readMetaForTest reads a meta.json without going through the lfs package's
// validation, so a malformed write still surfaces as a field assertion rather
// than a loader error.
func readMetaForTest(path string) (*lfs.SessionMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var meta lfs.SessionMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

// TestGitCommitAndPush_IgnoresOtherSessionsStagedFiles pins the pathspec scoping
// of the staged-changes check.
//
// The git index is shared across every session this process finalizes. If an
// earlier finalize staged another session's files and then failed to commit for
// any reason other than an empty stage, those files are still staged. An
// unscoped `git diff --cached` would see them, conclude this session has work to
// commit, and fold the other session's files into a commit titled
// "finalize session <this one>" — attributing files to the wrong session.
func TestGitCommitAndPush_IgnoresOtherSessionsStagedFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	_, clonePath := setupBareAndCloneLedger(t)

	// the session under test: already committed, so its own stage is empty
	target := "2026-01-15T19-00-testuser-OxSCOPE"
	targetDir := filepath.Join(clonePath, "sessions", target)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range append([]string{"raw.jsonl", "meta.json"}, requiredArtifacts...) {
		if err := os.WriteFile(filepath.Join(targetDir, name), []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	runGitCmd(t, clonePath, "add", "sessions/"+target)
	runGitCmd(t, clonePath, "commit", "--no-verify", "-m", "finalize session "+target)

	// a DIFFERENT session's file, left staged by an earlier failed finalize
	other := "2026-01-15T19-30-testuser-OxOTHER"
	otherDir := filepath.Join(clonePath, "sessions", other)
	if err := os.MkdirAll(otherDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherDir, "raw.jsonl"), []byte("other session"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, clonePath, "add", "sessions/"+other)

	handler := newGitBackedHandler()
	handler.gitCommitAndPush(&SessionFinalizePayload{
		SessionDir: targetDir,
		LedgerPath: clonePath,
	})

	// the other session's file must still be staged and uncommitted
	committed := gitOutput(t, clonePath, "log", "-1", "--name-only", "--pretty=format:")
	if strings.Contains(committed, other) {
		t.Errorf("finalizing %s committed %s's files:\n%s", target, other, committed)
	}
	if staged := gitOutput(t, clonePath, "diff", "--cached", "--name-only"); !strings.Contains(staged, other) {
		t.Errorf("%s's staged file went missing; still expected in the index, got %q", other, staged)
	}
}

// TestGitCommitAndPush_CommitExcludesOtherSessionsWhenStaged covers the case the
// scoping test above does not: the target session has real staged changes, so
// the commit actually runs.
//
// Scoping only the hasStagedChanges CHECK is not enough — `git commit -m` writes
// the whole index, so another session's staged files ride along under this
// session's commit message. The commit itself must carry the pathspec.
func TestGitCommitAndPush_CommitExcludesOtherSessionsWhenStaged(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	_, clonePath := setupBareAndCloneLedger(t)

	// target session: NOT yet committed, so finalizing it stages a real diff
	target := "2026-01-15T20-00-testuser-OxBOTH"
	targetDir := filepath.Join(clonePath, "sessions", target)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range append([]string{"raw.jsonl", "meta.json"}, requiredArtifacts...) {
		if err := os.WriteFile(filepath.Join(targetDir, name), []byte("target content"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// another session's file, left staged by an earlier failed finalize
	other := "2026-01-15T20-30-testuser-OxSTRAY"
	otherDir := filepath.Join(clonePath, "sessions", other)
	if err := os.MkdirAll(otherDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherDir, "raw.jsonl"), []byte("stray"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, clonePath, "add", "sessions/"+other)

	handler := newGitBackedHandler()
	handler.gitCommitAndPush(&SessionFinalizePayload{
		SessionDir: targetDir,
		LedgerPath: clonePath,
	})

	committed := gitOutput(t, clonePath, "log", "-1", "--name-only", "--pretty=format:")
	if !strings.Contains(committed, target) {
		t.Errorf("the target session's own files were not committed:\n%s", committed)
	}
	if strings.Contains(committed, other) {
		t.Errorf("%s's files rode along in %s's commit — `git commit` wrote the whole index:\n%s",
			other, target, committed)
	}
}
