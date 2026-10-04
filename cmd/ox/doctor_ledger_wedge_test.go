//go:build !short

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeAndCommitAt commits content at an arbitrary ledger path, including paths
// no auto-resolve tier is allowed to touch.
func writeAndCommitAt(t *testing.T, repo, relPath, content, msg string) {
	t.Helper()
	full := filepath.Join(repo, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	runGit(t, repo, "add", relPath)
	runGit(t, repo, "commit", "-m", msg)
}

// A behind-only pull whose conflict no tier can resolve must abort the rebase
// and say why — never leave the rebase in progress.
//
// ledgerLLMResolveHook deliberately converts automerge's ErrLLMUnavailable into
// (resolved=false, err=nil) — the LLM tier is opt-in via OX_LLM_MERGE_BIN, so
// on a default install that is the ORDINARY outcome, not an edge case. A caller
// that checks only the error reads "I could not resolve this" as success.
// internal/gitutil/push.go states the contract every other caller honors:
// "If it returns (false, nil) ... PushWithRetry aborts the rebase."
//
// Before the fix, this check set PassedCheck and skipped the abort. A later
// autostash check then overwrote the verdict, so the operator saw a failure —
// but attributed to the wrong thing, and with the rebase still on disk:
//
//	passed=false  message="autostash recovery failed"
//	detail="... cannot recover autostash while rebase-merge is present"
//
// That is terminal, not cosmetic. The surviving rebase makes every later run
// see hadRebaseBefore == true and decline to touch a rebase it did not start
// (ADR-030 D3), so `ox status` keeps advising `ox doctor --fix` and doctor keeps
// refusing. The ledger stays wedged until a human does git surgery.
func TestFixLedgerBranchBehind_UnresolvableConflictNeverReportsSuccess(t *testing.T) {
	// The default operator setup: no LLM merge binary configured.
	t.Setenv("OX_LLM_MERGE_BIN", "")

	barePath, machineA := createBareAndClone(t)
	machineB := cloneBare(t, barePath)

	// docs/ is on the hard-deny list in internal/manifest/auto_resolve.go, so
	// accept-theirs may not claim it and the union tier finds conflict markers.
	writeAndCommitAt(t, machineB, "docs/notes.md", "written on B\n", "docs: from B")
	writeAndCommitAt(t, machineA, "docs/notes.md", "written on A\n", "docs: from A")
	runGit(t, machineA, "push")
	runGit(t, machineB, "fetch")

	result := fixLedgerBranchBehind(machineB, 1)
	t.Logf("doctor verdict: passed=%v message=%q detail=%q", result.passed, result.message, result.detail)

	assert.False(t, result.passed,
		"doctor reported a successful pull for a conflict no tier could resolve: %s — %s",
		result.message, result.detail)

	assert.False(t, gitutil.IsRebaseInProgress(machineB),
		"doctor left a rebase in progress; ADR-030 D3 makes every later run refuse to touch it, so the ledger stays wedged until a human intervenes")
}
