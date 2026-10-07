package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/lfs"
)

// Failure prevented: with no content store reachable, a nil LFS client made
// dehydration a silent no-op, so a large plain plan.html left by a failed upload
// was committed as plain HTML on the next pass (#1236 review).
func TestPlanHTMLAwaitingUpload_NoStoreKeepsThePagePending(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	initPlanTestLedger(t, root)
	planDir := filepath.Join(t.TempDir(), "2026-10-07-pending")
	require.NoError(t, os.MkdirAll(planDir, 0o755))
	writeLargeAuthoredPage(t, filepath.Join(planDir, "plan.html"), "Pending Upload")

	prev := planLFSClientFn
	planLFSClientFn = func(string) *lfs.Client { return nil }
	t.Cleanup(func() { planLFSClientFn = prev })

	assert.True(t, planHTMLAwaitingUpload(planDir), "no store reachable: the page stays pending, never 'safe to commit'")
	assert.False(t, lfs.IsPointerFile(filepath.Join(planDir, "plan.html")), "nothing was dehydrated")
}

func TestSnapshotPriorRevision_RefusesWhenNoStoreAndPageAwaitsUpload(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	ledger := initPlanTestLedger(t, root)
	planDir := filepath.Join(ledger, "data", "plans", "2026-10-07-pending")
	require.NoError(t, os.MkdirAll(planDir, 0o755))
	writeLargeAuthoredPage(t, filepath.Join(planDir, "plan.html"), "Pending Upload")

	prev := planLFSClientFn
	planLFSClientFn = func(string) *lfs.Client { return nil }
	t.Cleanup(func() { planLFSClientFn = prev })

	err := snapshotPriorRevision(root, planDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no content store is reachable")
}

// Failure prevented: a reconcile that reported uploads while the re-verification
// of the plan pointers could not reach the store read as a pass.
func TestEvaluatePlanPointers_RecheckInspectionErrorIsInconclusive(t *testing.T) {
	ledger := t.TempDir()
	planDir := filepath.Join(ledger, "data", "plans", "2026-08-24-p")
	require.NoError(t, os.MkdirAll(planDir, 0o755))
	oid := lfs.ComputeOID([]byte("gone-render"))
	require.NoError(t, os.WriteFile(filepath.Join(planDir, "plan.html"), []byte(lfs.FormatPointer("sha256:"+oid, 11)), 0o644))
	pointers := collectPlanHTMLPointers(filepath.Join(ledger, "data", "plans"), ledger)
	require.Len(t, pointers, 1)

	var storeDown atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if storeDown.Load() {
			http.Error(w, "store unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
		_, _ = w.Write([]byte(`{"transfer":"basic","objects":[{"oid":"` + oid + `","size":11,"error":{"code":404,"message":"x"}}]}`))
	}))
	t.Cleanup(srv.Close)
	client := lfs.NewClient(srv.URL, "oauth2", "token")

	res := evaluatePlanPointers(client, pointers, true, func() (*lfs.ReconcileResult, error) {
		storeDown.Store(true) // the store goes away between reconcile and the recheck
		return &lfs.ReconcileResult{RecoveredUploads: 1}, nil
	})
	assert.True(t, res.warning, "an unverifiable recheck is inconclusive, never a pass")
	assert.Contains(t, res.message, "could not be re-verified")
}
