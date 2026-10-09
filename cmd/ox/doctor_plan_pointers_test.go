package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPlanPointersMissingOnRemote_Only404IsMissing pins the guard that keeps the
// doctor check honest: ONLY a 404 proves a blob is absent. A transient 401 (token
// expired) or 5xx says nothing, and treating it as "missing" would report a live
// plan as unrecoverable — and, if the same logic drove the reconcile, blank it to
// zero bytes. Mirrors the reconcile's 404-only rule.
func TestPlanPointersMissingOnRemote_Only404IsMissing(t *testing.T) {
	oidPresent := lfs.ComputeOID([]byte("present-blob"))
	oidMissing := lfs.ComputeOID([]byte("missing-blob"))
	oidFlaky := lfs.ComputeOID([]byte("flaky-blob"))

	// per-OID batch-download outcome: 0 == present (no error)
	codes := map[string]int{oidPresent: 0, oidMissing: http.StatusNotFound, oidFlaky: http.StatusUnauthorized}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Objects []struct {
				OID  string `json:"oid"`
				Size int64  `json:"size"`
			} `json:"objects"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		type oerr struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		type obj struct {
			OID   string `json:"oid"`
			Size  int64  `json:"size"`
			Error *oerr  `json:"error,omitempty"`
		}
		resp := struct {
			Transfer string `json:"transfer"`
			Objects  []obj  `json:"objects"`
		}{Transfer: "basic"}
		for _, o := range req.Objects {
			ob := obj{OID: o.OID, Size: o.Size}
			if code := codes[o.OID]; code != 0 {
				ob.Error = &oerr{Code: code, Message: "x"}
			}
			resp.Objects = append(resp.Objects, ob)
		}
		w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()
	client := lfs.NewClient(srv.URL, "oauth2", "token")

	pointers := []planPointer{
		{Name: "p-present", ref: lfs.FileRef{Storage: "lfs", OID: "sha256:" + oidPresent, Size: 10}},
		{Name: "p-missing", ref: lfs.FileRef{Storage: "lfs", OID: "sha256:" + oidMissing, Size: 20}},
		{Name: "p-flaky", ref: lfs.FileRef{Storage: "lfs", OID: "sha256:" + oidFlaky, Size: 30}},
	}

	missing, err := planPointersMissingOnRemote(client, pointers)
	require.NoError(t, err)
	require.Len(t, missing, 1, "only the 404 pointer is genuinely missing; the 401 is inconclusive")
	assert.Equal(t, "p-missing", missing[0].Name)
}

// TestCollectPlanHTMLPointers_SkipsPlainAndNonPlans verifies the walk only
// collects data/plans/<dir>/plan.html files that are actually LFS pointers — a
// plain plan.html (the healthy common case) and other files are ignored.
func TestCollectPlanHTMLPointers_SkipsPlainAndNonPlans(t *testing.T) {
	ledger := t.TempDir()
	plansDir := filepath.Join(ledger, "data", "plans")

	// a pointer plan.html — collected
	ptrDir := filepath.Join(plansDir, "2026-08-24-ptr")
	require.NoError(t, os.MkdirAll(ptrDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ptrDir, "plan.html"),
		[]byte(lfs.FormatPointer("sha256:"+lfs.ComputeOID([]byte("big")), 999)), 0o644))

	// a plain plan.html — ignored
	plainDir := filepath.Join(plansDir, "2026-08-24-plain")
	require.NoError(t, os.MkdirAll(plainDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(plainDir, "plan.html"),
		[]byte("<html>real content</html>"), 0o644))
	// its meta.json must never be mistaken for a pointer
	require.NoError(t, os.WriteFile(filepath.Join(plainDir, "meta.json"), []byte(`{"topic":"x"}`), 0o644))

	got := collectPlanHTMLPointers(plansDir, ledger)
	require.Len(t, got, 1)
	assert.Equal(t, "2026-08-24-ptr", got[0].Name)
}

// newFakeDownloadServer serves the batch DOWNLOAD op with per-OID HTTP codes
// (0 == present).
func newFakeDownloadServer(t *testing.T, codeByOID map[string]int) *lfs.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Objects []struct {
				OID  string `json:"oid"`
				Size int64  `json:"size"`
			} `json:"objects"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		type oerr struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		type obj struct {
			OID   string `json:"oid"`
			Size  int64  `json:"size"`
			Error *oerr  `json:"error,omitempty"`
		}
		resp := struct {
			Transfer string `json:"transfer"`
			Objects  []obj  `json:"objects"`
		}{Transfer: "basic"}
		for _, o := range req.Objects {
			ob := obj{OID: o.OID, Size: o.Size}
			if code := codeByOID[o.OID]; code != 0 {
				ob.Error = &oerr{Code: code, Message: "x"}
			}
			resp.Objects = append(resp.Objects, ob)
		}
		w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return lfs.NewClient(srv.URL, "oauth2", "token")
}

// TestEvaluatePlanPointers_WarnThenFix pins the doctor warn/fix wiring: the
// non-fix path warns and never reconciles; the fix path invokes the reconcile and
// reports its result. Guards against a regression that reconciles on a bare
// `ox doctor` (destructive without --fix) or that swallows the reconcile outcome.
func TestEvaluatePlanPointers_WarnThenFix(t *testing.T) {
	ledger := t.TempDir()
	planDir := filepath.Join(ledger, "data", "plans", "2026-08-24-p")
	require.NoError(t, os.MkdirAll(planDir, 0o755))
	htmlPath := filepath.Join(planDir, "plan.html")
	oid := lfs.ComputeOID([]byte("gone-render"))
	require.NoError(t, os.WriteFile(htmlPath, []byte(lfs.FormatPointer("sha256:"+oid, 999)), 0o644))

	pointers := collectPlanHTMLPointers(filepath.Join(ledger, "data", "plans"), ledger)
	require.Len(t, pointers, 1)

	statuses := map[string]int{oid: http.StatusNotFound}
	client := newFakeDownloadServer(t, statuses)
	pointerBytes, err := os.ReadFile(htmlPath)
	require.NoError(t, err)

	// warn path (no --fix): reconcile must NOT be called.
	warn := evaluatePlanPointers(client, pointers, false, func() (*lfs.ReconcileResult, error) {
		t.Fatal("reconcile invoked on the non-fix warn path")
		return nil, nil
	})
	assert.True(t, warn.warning, "missing blobs must warn")
	assert.Contains(t, warn.message, "1")

	// fix path: reconcile is invoked and its result is reported as a pass.
	called := false
	fix := evaluatePlanPointers(client, pointers, true, func() (*lfs.ReconcileResult, error) {
		called = true
		statuses[oid] = http.StatusOK // the blob is back on the store; the pointer is untouched
		return &lfs.ReconcileResult{RecoveredUploads: 1}, nil
	})
	assert.True(t, called, "the --fix path must invoke the reconcile")
	assert.False(t, fix.warning, "a successful reconcile is a passed result")
	assert.Contains(t, fix.message, "restored")
	after, err := os.ReadFile(htmlPath)
	require.NoError(t, err)
	assert.Equal(t, pointerBytes, after, "a repair never rewrites the plan pointer")
	// an aggregate upload count is not proof: with the blob still absent the check must not pass
	statuses[oid] = http.StatusNotFound
	notProven := evaluatePlanPointers(client, pointers, true, func() (*lfs.ReconcileResult, error) {
		return &lfs.ReconcileResult{RecoveredUploads: 1}, nil
	})
	assert.True(t, notProven.warning, "uploads elsewhere must not pass a still-missing plan pointer")
	assert.Contains(t, notProven.message, "still missing")

	// nothing recovered and the blob is still absent: a warning, never a pass.
	none := evaluatePlanPointers(client, pointers, true, func() (*lfs.ReconcileResult, error) {
		return &lfs.ReconcileResult{}, nil
	})
	assert.True(t, none.warning, "zero recovered uploads with the blob still missing must warn")
	assert.Contains(t, none.message, "still missing")
	assert.NotContains(t, none.message, "unblocked")

	// reconcile-failure path: a reconcile error must surface as a warning (with
	// the count preserved), never be swallowed into a passing result.
	failed := evaluatePlanPointers(client, pointers, true, func() (*lfs.ReconcileResult, error) {
		return nil, fmt.Errorf("batch check inconclusive: HTTP 401")
	})
	assert.True(t, failed.warning, "a reconcile error must warn, not pass")
	assert.Contains(t, failed.message, "reconcile failed")
	assert.Contains(t, failed.message, "1", "the missing count is preserved in the failure message")
}

// Failure prevented: a store that cannot be queried reading as "all pointers
// backed by the store", which skipped reconciliation and hid a wedge.
func TestEvaluatePlanPointers_InspectionErrorIsInconclusive(t *testing.T) {
	ledger := t.TempDir()
	planDir := filepath.Join(ledger, "data", "plans", "2026-08-24-p")
	require.NoError(t, os.MkdirAll(planDir, 0o755))
	oid := lfs.ComputeOID([]byte("render"))
	require.NoError(t, os.WriteFile(filepath.Join(planDir, "plan.html"), []byte(lfs.FormatPointer("sha256:"+oid, 6)), 0o644))
	pointers := collectPlanHTMLPointers(filepath.Join(ledger, "data", "plans"), ledger)
	require.Len(t, pointers, 1)
	// the Batch request itself fails (not a per-object error, which is "present")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "store unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	client := lfs.NewClient(srv.URL, "oauth2", "token")
	res := evaluatePlanPointers(client, pointers, true, func() (*lfs.ReconcileResult, error) {
		t.Fatal("reconcile must not run on an inconclusive inspection")
		return nil, nil
	})
	assert.True(t, res.warning, "an inspection error is inconclusive, never a pass")
	assert.Contains(t, res.message, "inconclusive")
}
