package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// strandedGit runs git in dir with a hermetic identity and fails on error.
func strandedGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// newStrandedLedger builds a ledger whose HEAD carries commits no branch or
// remote can reach — the bd ox-akab shape. wedged additionally leaves a rebase
// state directory in place.
func newStrandedLedger(t *testing.T, strandedCommits int, wedged bool) string {
	t.Helper()
	dir := t.TempDir()
	strandedGit(t, dir, "init", "--initial-branch=main")

	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		strandedGit(t, dir, "add", name)
		strandedGit(t, dir, "commit", "-m", "add "+name)
	}
	write("base.txt", "base")

	if strandedCommits > 0 {
		strandedGit(t, dir, "checkout", "--detach")
		for i := 0; i < strandedCommits; i++ {
			write("session-"+string(rune('a'+i))+".txt", "session data")
		}
	}

	if wedged {
		// A COMPLETE rebase-merge state (head-name + orig-head + onto), not the
		// "zombie" autostash-only shape.
		//
		// This distinction is load-bearing and cost two rounds of red-first to
		// find. On a zombie state AbortOrClearRebase REFUSES to escalate (quitting
		// a detached HEAD would strand the replay), so the state directory
		// survives no matter what the caller does — which means a test built on it
		// passes even with the fail-closed guard deleted. Only an ABORTABLE state
		// can distinguish "we declined to clear the wedge" from "we tried and
		// git wouldn't".
		stateDir := filepath.Join(dir, ".git", "rebase-merge")
		if err := os.MkdirAll(stateDir, 0o755); err != nil {
			t.Fatal(err)
		}
		origHead := strandedGit(t, dir, "rev-list", "--max-parents=0", "HEAD")
		for name, body := range map[string]string{
			"head-name": "refs/heads/main",
			"orig-head": origHead,
			"onto":      origHead,
		} {
			if err := os.WriteFile(filepath.Join(stateDir, name), []byte(body+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return dir
}

// TestCheckLedgerStrandedCommits_ReportsWithoutFix pins the read-only contract:
// a check run with fix=false must never mutate the repo, no matter how alarming
// the finding. Doctor reports; --fix repairs.
func TestCheckLedgerStrandedCommits_ReportsWithoutFix(t *testing.T) {
	dir := newStrandedLedger(t, 2, false)
	before := strandedGit(t, dir, "branch", "--list")

	result := strandedCommitsCheck(dir, false, false)
	if result.passed || result.skipped {
		t.Fatalf("expected a failed check for 2 stranded commits, got passed=%v skipped=%v (%s)",
			result.passed, result.skipped, result.message)
	}
	if !strings.Contains(result.message, "2 commit") {
		t.Errorf("message should name the count, got %q", result.message)
	}

	if after := strandedGit(t, dir, "branch", "--list"); after != before {
		t.Errorf("check without --fix mutated refs: %q -> %q", before, after)
	}
}

// TestCheckLedgerStrandedCommits_FixCreatesRescueBranch covers the additive
// path: creating a branch adds a ref and mutates nothing else, so it is safe
// even unattended. An automated pass must always be able to make data safe.
func TestCheckLedgerStrandedCommits_FixCreatesRescueBranch(t *testing.T) {
	dir := newStrandedLedger(t, 3, false)
	headBefore := strandedGit(t, dir, "rev-parse", "HEAD")

	result := strandedCommitsCheck(dir, true, false)
	if result.skipped {
		t.Fatalf("check skipped unexpectedly: %s", result.message)
	}

	branches := strandedGit(t, dir, "branch", "--list", "rescue-wedge-*")
	if branches == "" {
		t.Fatalf("no rescue branch created; check said %q / %q", result.message, result.detail)
	}
	ref := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(branches), "*"))
	if got := strandedGit(t, dir, "rev-parse", ref); got != headBefore {
		t.Errorf("rescue branch %s points at %s, want pre-fix HEAD %s", ref, got, headBefore)
	}

	// Nothing is stranded any more, because a branch now reaches the commits.
	count := strandedGit(t, dir, "rev-list", "--count", "HEAD", "--not", "--branches", "--remotes")
	if count != "0" {
		t.Errorf("still %s stranded commits after the fix", count)
	}
}

// TestCheckLedgerStrandedCommits_FixFailsClosedOnWedge is the fail-closed
// assertion. In a non-interactive context (which is every agent and CI run) the
// destructive half must NOT run: the data gets rescued, the wedge is left alone,
// and the check stays a failure so it remains visible.
func TestCheckLedgerStrandedCommits_FixFailsClosedOnWedge(t *testing.T) {
	dir := newStrandedLedger(t, 2, true)
	stateDir := filepath.Join(dir, ".git", "rebase-merge")
	if _, err := os.Stat(stateDir); err != nil {
		t.Fatalf("fixture: expected a rebase state dir: %v", err)
	}

	// interactive=false is the agent / CI case, which is the one that matters.
	result := strandedCommitsCheck(dir, true, false)

	// The data must be safe.
	if branches := strandedGit(t, dir, "branch", "--list", "rescue-wedge-*"); branches == "" {
		t.Error("fail-closed must still rescue the data: no rescue branch created")
	}
	// The wedge must NOT have been cleared without a human.
	if _, err := os.Stat(stateDir); err != nil {
		t.Error("non-interactive run cleared the rebase wedge; the destructive half must require a TTY")
	}
	// And it must stay loud.
	if result.passed && !result.warning {
		t.Errorf("expected the check to remain a visible failure, got passed=%v: %s", result.passed, result.message)
	}
	if !strings.Contains(result.message, "NOT cleared") {
		t.Errorf("message should say the wedge was left, got %q", result.message)
	}
}

// TestCheckLedgerStrandedCommits_CleanLedgerPasses keeps the check quiet on a
// healthy repo — a noisy check is one people learn to ignore.
func TestCheckLedgerStrandedCommits_CleanLedgerPasses(t *testing.T) {
	dir := newStrandedLedger(t, 0, false)
	result := strandedCommitsCheck(dir, true, false)
	if !result.passed || result.warning {
		t.Errorf("clean ledger should pass quietly, got passed=%v warning=%v (%s)",
			result.passed, result.warning, result.message)
	}
	if branches := strandedGit(t, dir, "branch", "--list", "rescue-wedge-*"); branches != "" {
		t.Errorf("rescue branch created on a clean ledger: %q", branches)
	}
}
