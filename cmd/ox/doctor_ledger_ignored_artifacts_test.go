//go:build !short

package main

// doctor_ledger_ignored_artifacts_test.go: untracking ignored artifacts is not
// a sacred mass deletion (#1443).

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/sacred"
)

// TestPlainDoctor_UntracksIgnoredArtifactsUnderSessions: a Ledger tracks a
// patch reject and a summary request in more sessions than the mass-deletion
// threshold. A plain `ox doctor` untracks them, and a session upload afterwards
// commits and pushes. Failure prevented: the sacred guard counted the
// untracking as deletions and refused both (#1443).
func TestPlainDoctor_UntracksIgnoredArtifactsUnderSessions(t *testing.T) {
	ledger := reviewTestLedger(t)
	origin := strings.TrimSpace(runGitInDir(t, ledger, "remote", "get-url", "origin"))
	sessionsDir := filepath.Join(ledger, "sessions")
	for i := 0; i <= sacred.MassDeleteThreshold; i++ {
		name := fmt.Sprintf("2026-10-%02dT00-00-test-Ox%04d", i+1, i)
		writeTestSessionMeta(t, sessionsDir, name, "")
		writeLedgerFile(t, ledger, "sessions/"+name+"/meta.json.rej", "@@ rejected hunk\n")
		writeLedgerFile(t, ledger, "sessions/"+name+"/.needs-summary", "{}\n")
	}
	runGitInDir(t, ledger, "add", "-A")
	runGitInDir(t, ledger, "commit", "-q", "-m", "seed")
	// on a real Ledger, sessions/.gitignore came after these were committed
	if err := ensureSessionsGitignore(sessionsDir); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, ledger, "add", "sessions/.gitignore")
	runGitInDir(t, ledger, "commit", "-q", "-m", "ignore summary requests")
	runGitInDir(t, ledger, "push", "-q")

	if rej := doctorLine(t, runDoctor(t, ledger, plainDoctor), "Ledger .rej files untracked"); rej.message != "untracked .rej files from ledger" {
		t.Errorf("Ledger .rej files untracked: %q: %s", rej.message, rej.detail)
	}
	const session = "2026-10-20T00-00-test-OxNew1"
	writeSessionFiles(t, ledger, session)
	if err := commitAndPushLedger(ledger, session); err != nil {
		t.Fatalf("the session upload was refused: %v", err)
	}
	remote := remoteFiles(t, origin)
	if !strings.Contains(remote, "sessions/"+session+"/meta.json") {
		t.Error("the remote lacks the uploaded session")
	}
	if strings.Contains(remote, ".rej") || strings.Contains(remote, ".needs-summary") {
		t.Errorf("the remote still tracks patch rejects or summary requests:\n%s", remote)
	}
}
