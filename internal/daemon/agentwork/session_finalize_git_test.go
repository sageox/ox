package agentwork

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sageox/ox/internal/session"
)

// TestProcessResult_RealGit exercises ProcessResult with skipGit=false against
// a real bare git repo. Verifies the full pipeline: artifact generation → git
// add → commit → push.
func TestProcessResult_RealGit(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	barePath, clonePath := setupBareAndCloneLedger(t)
	sessionName := "2026-01-15T12-00-testuser-OxGIT1"
	sessionDir := filepath.Join(clonePath, "sessions", sessionName)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}
	rawContent := `{"_meta":{"schema_version":"1","agent_type":"claude-code"}}
{"type":"user","content":"hello","seq":1}
{"type":"assistant","content":"world","seq":2}
`
	if err := os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte(rawContent), 0644); err != nil {
		t.Fatal(err)
	}

	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipLFS = true // no LFS server
	handler.skipGit = false
	handler.ledgerMu = &sync.Mutex{}

	stored, err := session.ReadSessionFromPath(filepath.Join(sessionDir, "raw.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	item := &WorkItem{
		ID:   "test-git",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir:    sessionDir,
			RawPath:       filepath.Join(sessionDir, "raw.jsonl"),
			Missing:       []string{artifactSummaryMD, artifactSummJSON, artifactSessionMD},
			LedgerPath:    clonePath,
			storedSession: stored,
		},
	}
	result := &RunResult{
		Output: `{"summary":"test summary","quality_score":1.0,"score_reason":"test"}`,
	}

	err = handler.ProcessResult(item, result)
	if err != nil {
		t.Fatalf("ProcessResult failed: %v", err)
	}

	// verify artifacts were generated
	for _, artifact := range []string{artifactSummaryMD, artifactSummJSON, artifactSessionMD} {
		path := filepath.Join(sessionDir, artifact)
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			t.Errorf("artifact %s not generated", artifact)
		}
	}

	// verify push reached the remote
	verifyClone := t.TempDir()
	runGitCmd(t, t.TempDir(), "clone", "file://"+barePath, verifyClone)
	remoteMeta := filepath.Join(verifyClone, "sessions", sessionName, "meta.json")
	if _, statErr := os.Stat(remoteMeta); os.IsNotExist(statErr) {
		t.Error("meta.json not found on remote — push may have failed")
	}
}

// TestProcessResult_PushFailPreservesArtifacts verifies the data-loss safety
// guarantee: when git push fails, all generated artifacts must survive on disk.
func TestProcessResult_PushFailPreservesArtifacts(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	_, clonePath := setupBareAndCloneLedger(t)
	sessionName := "2026-01-15T12-00-testuser-OxFAIL"
	sessionDir := filepath.Join(clonePath, "sessions", sessionName)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}
	rawContent := `{"_meta":{"schema_version":"1","agent_type":"claude-code"}}
{"type":"user","content":"hello","seq":1}
{"type":"assistant","content":"world","seq":2}
`
	if err := os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte(rawContent), 0644); err != nil {
		t.Fatal(err)
	}

	// break the remote so push fails
	runGitCmd(t, clonePath, "remote", "set-url", "origin", "/nonexistent/broken/remote.git")

	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipLFS = true
	handler.skipGit = false
	handler.ledgerMu = &sync.Mutex{}

	stored, err := session.ReadSessionFromPath(filepath.Join(sessionDir, "raw.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	item := &WorkItem{
		ID:   "test-fail",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir:    sessionDir,
			RawPath:       filepath.Join(sessionDir, "raw.jsonl"),
			Missing:       []string{artifactSummaryMD, artifactSummJSON, artifactSessionMD},
			LedgerPath:    clonePath,
			storedSession: stored,
		},
	}
	result := &RunResult{
		Output: `{"summary":"test summary","quality_score":1.0,"score_reason":"test"}`,
	}

	// ProcessResult returns nil even on push failure (push is non-fatal)
	err = handler.ProcessResult(item, result)
	if err != nil {
		t.Fatalf("ProcessResult should not return error on push failure: %v", err)
	}

	// CRITICAL: all artifacts must survive despite failed push
	for _, name := range []string{"raw.jsonl", artifactSummaryMD, artifactSummJSON, artifactSessionMD} {
		path := filepath.Join(sessionDir, name)
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			t.Errorf("DATALOSS: %s missing after failed push — content destroyed", name)
		}
	}
}

// helpers

func setupBareAndCloneLedger(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	barePath := filepath.Join(base, "remote.git")
	clonePath := filepath.Join(base, "ledger")

	runGitCmd(t, base, "init", "--bare", barePath)
	runGitCmd(t, base, "clone", "file://"+barePath, clonePath)
	runGitCmd(t, clonePath, "config", "user.email", "test@test.com")
	runGitCmd(t, clonePath, "config", "user.name", "Test")

	// create ledger structure with initial commit
	sessionsDir := filepath.Join(clonePath, "sessions")
	if err := os.MkdirAll(sessionsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, ".gitkeep"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, clonePath, "add", ".")
	runGitCmd(t, clonePath, "commit", "--no-verify", "-m", "init ledger")
	runGitCmd(t, clonePath, "push")

	return barePath, clonePath
}

func runGitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s failed: %s\n%s", args, dir, err, out)
	}
}

