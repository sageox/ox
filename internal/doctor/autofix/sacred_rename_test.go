package autofix

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func srGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}

func srWrite(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
}

// srLedger stages a ledger with n plans, each carrying a meta.json.
func srLedger(t *testing.T, n int) string {
	t.Helper()
	dir := t.TempDir()
	srGit(t, dir, "init", "-q")
	srGit(t, dir, "config", "user.email", "t@test.sageox.ai")
	srGit(t, dir, "config", "user.name", "t")
	for i := 0; i < n; i++ {
		name := string(rune('a' + i))
		srWrite(t, dir, "data/plans/plan-"+name+"/meta.json", `{"title":"old `+name+`"}`)
		srWrite(t, dir, "data/plans/plan-"+name+"/plan.md", "body "+name+"\n")
	}
	srGit(t, dir, "add", "-A")
	srGit(t, dir, "commit", "-q", "-m", "seed")
	return dir
}

// TestDetector_RenamingPlansIsNotAWipe is the false positive this repository's
// own ledger produced for two months.
//
// `ox plan backfill` retitles plans, which RENAMES their directories and
// rewrites meta.json in the same commit. Git's rename detection cannot pair
// them — the content changed too much — so the old paths look like unpaired
// deletions. The detector counted them and alerted on every 15-minute scan
// while the plan population was unchanged (27 -> 27 on the real ledger).
//
// Failure prevented: an alert that cries wolf until the operator stops reading
// it, which is how a real wipe gets missed.
func TestDetector_RenamingPlansIsNotAWipe(t *testing.T) {
	dir := srLedger(t, 6)

	// Retitle every plan: directory renamed AND meta.json rewritten, exactly
	// what the backfill does.
	for i := 0; i < 6; i++ {
		name := string(rune('a' + i))
		srGit(t, dir, "mv", "data/plans/plan-"+name, "data/plans/retitled-"+name)
		srWrite(t, dir, "data/plans/retitled-"+name+"/meta.json",
			`{"title":"a completely different title for `+name+`","backfilled":true}`)
	}
	srGit(t, dir, "add", "-A")
	srGit(t, dir, "commit", "-q", "-m", "plan: backfill 6 title(s)")

	res := scanLedgerSacredDeletions(context.Background(), dir, dir)
	require.Equal(t, StatusClean, res.Status,
		"renaming plans must not report a wipe: the population is unchanged, nothing was lost")
}

// TestDetector_ActuallyDeletingPlansStillAlerts is the other half. Making the
// detector quieter is only correct if it still fires on the thing it exists for.
func TestDetector_ActuallyDeletingPlansStillAlerts(t *testing.T) {
	dir := srLedger(t, 6)

	for i := 0; i < 5; i++ {
		name := string(rune('a' + i))
		srGit(t, dir, "rm", "-r", "-q", "data/plans/plan-"+name)
	}
	srGit(t, dir, "commit", "-q", "-m", "remove plans")

	res := scanLedgerSacredDeletions(context.Background(), dir, dir)
	require.NotEqual(t, StatusClean, res.Status,
		"deleting 5 of 6 plans is a real wipe and must still be reported")
}
