package main

// Tests for the knowledge-bubble repo-health doctor checks
// (doctor_kb_repo_health.go): missing-clone, wedged, sparse-checkout. These
// give kb parity with the ledger/team-context git-repo doctoring. Each test
// uses a tmpdir-backed kb root via kbDoctorHooks — no network, no daemon.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gitInitKBDir creates <root>/<kbID> as a real (minimal) git repo so isGitRepo
// and `git status` work. Returns the repo path.
func gitInitKBDir(t *testing.T, root, kbID string) string {
	t.Helper()
	dir := filepath.Join(root, kbID)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		require.NoError(t, cmd.Run(), "git %v", args)
	}
	return dir
}

// ---- missing-clone ----

// TestCheckKBMissingClone_FlagsUncloned verifies a bubble the API lists with no
// local .git is reported and the daemon kick fires on --fix.
// Failure prevented: a subscribed bubble never reaches disk and the user has no
// signal that sync hasn't happened.
func TestCheckKBMissingClone_FlagsUncloned(t *testing.T) {
	root, h := kbTestSetup(t)
	gitInitKBDir(t, root, "kb_have") // cloned
	h.List = func(_ context.Context) ([]api.KB, error) {
		return []api.KB{
			{KBID: "kb_have", Slug: "have"},
			{KBID: "kb_missing", Slug: "missing"},
		}, nil
	}
	applyHooks(h)

	res := checkKBMissingClone(false)
	assert.True(t, res.passed && res.warning, "missing clone must warn; got %+v", res)
	assert.Contains(t, res.message, "missing")
	assert.NotContains(t, res.message, "have")

	synced := 0
	h.Sync = func(_ context.Context) error { synced++; return nil }
	applyHooks(h)
	res = checkKBMissingClone(true)
	assert.Equal(t, 1, synced, "fix must kick exactly one daemon sync")
	assert.True(t, res.passed && !res.warning, "post-fix kick should pass; got %+v", res)
}

// TestCheckKBMissingClone_IgnoresProvisionFailed verifies a provision-failed
// bubble (no repo yet) is not mistaken for a missing clone — that's the
// provisioning check's job.
func TestCheckKBMissingClone_IgnoresProvisionFailed(t *testing.T) {
	root, h := kbTestSetup(t)
	gitInitKBDir(t, root, "kb_ok")
	h.List = func(_ context.Context) ([]api.KB, error) {
		return []api.KB{
			{KBID: "kb_ok", Slug: "ok"},
			{KBID: "kb_bad", Slug: "bad", LifecycleState: "provision-failed"},
		}, nil
	}
	applyHooks(h)

	res := checkKBMissingClone(false)
	assert.True(t, res.passed && !res.warning, "provision-failed must not count as missing; got %+v", res)
}

// TestCheckKBMissingClone_APIUnavailableSkips verifies the check skips (never
// flags) when the kb API can't be reached.
func TestCheckKBMissingClone_APIUnavailableSkips(t *testing.T) {
	_, h := kbTestSetup(t)
	h.List = func(_ context.Context) ([]api.KB, error) { return nil, api.ErrKBAPIUnavailable }
	applyHooks(h)

	res := checkKBMissingClone(false)
	assert.True(t, res.skipped, "API-unavailable must skip; got %+v", res)
}

// ---- wedged ----

// TestCheckKBWedged_DetectsRebaseInProgress verifies a bubble stuck mid-rebase
// is reported Critical with the manual abort hint.
// Failure prevented: a wedged bubble silently blocks the global-sync owner's
// syncBubbles pass for every project, with no doctor signal.
func TestCheckKBWedged_DetectsRebaseInProgress(t *testing.T) {
	root, h := kbTestSetup(t)
	dir := gitInitKBDir(t, root, "kb_wedged")
	gitInitKBDir(t, root, "kb_clean")
	// simulate an in-progress rebase the way gitutil.IsRebaseInProgress detects.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git", "rebase-merge"), 0o755))
	applyHooks(h)

	res := checkKBWedged(false)
	assert.False(t, res.passed, "wedged repo must fail the check; got %+v", res)
	assert.Equal(t, "critical", res.priority, "wedge must be critical")
	assert.Contains(t, res.message, "kb_wedged")
	assert.Contains(t, res.detail, "rebase --abort")
}

// TestCheckKBWedged_CleanPasses verifies healthy bubbles pass.
func TestCheckKBWedged_CleanPasses(t *testing.T) {
	root, h := kbTestSetup(t)
	gitInitKBDir(t, root, "kb_a")
	gitInitKBDir(t, root, "kb_b")
	applyHooks(h)

	res := checkKBWedged(false)
	assert.True(t, res.passed && !res.warning, "clean repos must pass; got %+v", res)
}

// TestCheckKBWedged_FixKicksThenRechecks verifies --fix kicks a daemon sync and,
// when the wedge persists (test never clears it), still reports critical.
func TestCheckKBWedged_FixKicksThenRechecks(t *testing.T) {
	root, h := kbTestSetup(t)
	dir := gitInitKBDir(t, root, "kb_wedged")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git", "rebase-merge"), 0o755))
	synced := 0
	h.Sync = func(_ context.Context) error { synced++; return nil }
	applyHooks(h)

	res := checkKBWedged(true)
	assert.Equal(t, 1, synced, "fix must attempt a daemon sync")
	assert.Equal(t, "critical", res.priority, "still-wedged must stay critical; got %+v", res)
}

// ---- sparse-checkout ----

// TestCheckKBSparseCheckout_FlagsMissingSageox verifies a sparse cone without
// .sageox is flagged, while a cone with it passes.
// Failure prevented: .sageox drops from the cone, hiding kb.yaml/sync.manifest
// and silently breaking the next pull's sparse reapply.
func TestCheckKBSparseCheckout_FlagsMissingSageox(t *testing.T) {
	root, h := kbTestSetup(t)
	bad := gitInitKBDir(t, root, "kb_bad")
	good := gitInitKBDir(t, root, "kb_good")
	writeSparse(t, bad, "/*\n!/*/\nsrc\n")           // no .sageox
	writeSparse(t, good, "/*\n!/*/\n.sageox\nsrc\n") // includes .sageox
	applyHooks(h)

	res := checkKBSparseCheckout(false)
	assert.True(t, res.passed && res.warning, "missing .sageox must warn; got %+v", res)
	assert.Contains(t, res.message, "kb_bad")
	assert.NotContains(t, res.message, "kb_good")
}

// TestCheckKBSparseCheckout_NoSparseFileSkips verifies bubbles with full
// checkout (no sparse-checkout file) don't trigger the check.
func TestCheckKBSparseCheckout_NoSparseFileSkips(t *testing.T) {
	root, h := kbTestSetup(t)
	gitInitKBDir(t, root, "kb_full") // no sparse-checkout file
	applyHooks(h)

	res := checkKBSparseCheckout(false)
	assert.True(t, res.skipped, "no sparse bubbles must skip; got %+v", res)
}

func writeSparse(t *testing.T, repoDir, content string) {
	t.Helper()
	infoDir := filepath.Join(repoDir, ".git", "info")
	require.NoError(t, os.MkdirAll(infoDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(infoDir, "sparse-checkout"), []byte(content), 0o644))
}
