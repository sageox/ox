package lfs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReconcile_WalksPlanPointers pins the GH #810 fix: ReconcileUnpushedPointers
// must scan data/plans/, not only sessions/.
//
// Failure prevented: a poisoned plan.html pointer (blob never uploaded) was
// invisible to the reconcile that pushLedger auto-runs on a rejected push, so it
// never got neutralized — which is exactly how one ledger sat unpushable for 43
// commits. Before the walk was extended, a plan-only pointer counted 0 here.
//
// The reconcile can't reach the remote in this fixture (no configured LFS
// endpoint), so it returns an error at the batch-check step — but ScannedPointers
// is set by the WALK, before that call, and the walk is what this test pins.
func TestReconcile_WalksPlanPointers(t *testing.T) {
	dir := initLedgerRepo(t)

	// one session pointer (sessions/ — always walked)
	sess := filepath.Join(dir, "sessions", "2026-08-24-s")
	require.NoError(t, os.MkdirAll(sess, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sess, "raw.jsonl"),
		[]byte(lfsPointerContent(strings.Repeat("a", 64), 10)), 0o644))

	// one plan pointer (data/plans/ — the newly-walked tree)
	planDir := filepath.Join(dir, "data", "plans", "2026-08-24-p")
	require.NoError(t, os.MkdirAll(planDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(planDir, "plan.html"),
		[]byte(lfsPointerContent(strings.Repeat("b", 64), 20)), 0o644))

	result, _ := ReconcileUnpushedPointers(context.Background(), dir, "", nil)
	assert.Equal(t, 2, result.ScannedPointers,
		"reconcile must scan BOTH sessions/ and data/plans/ (a plan pointer was invisible before)")
}

// TestReconcile_PlanOnlyPointerIsScanned isolates the plan tree: a ledger with a
// pointer ONLY under data/plans/ must still see it. Guards against a regression
// that drops the plan root from the walk while keeping sessions/.
func TestReconcile_PlanOnlyPointerIsScanned(t *testing.T) {
	dir := initLedgerRepo(t)

	planDir := filepath.Join(dir, "data", "plans", "2026-08-24-only")
	require.NoError(t, os.MkdirAll(planDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(planDir, "plan.html"),
		[]byte(lfsPointerContent(strings.Repeat("c", 64), 30)), 0o644))

	result, _ := ReconcileUnpushedPointers(context.Background(), dir, "", nil)
	assert.Equal(t, 1, result.ScannedPointers, "a plan-only pointer must be scanned")
}
