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
	require.ErrorContains(t, err, "HTTP 401")
	assert.Empty(t, missing, "an inconclusive inspection cannot certify plan content")
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

	codes := map[string]int{oid: http.StatusNotFound}
	client := newFakeDownloadServer(t, codes)

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
		codes[oid] = 0 // simulate restoring the blob, preserving the pointer
		return &lfs.ReconcileResult{RecoveredUploads: 1}, nil
	})
	assert.True(t, called, "the --fix path must invoke the reconcile")
	assert.False(t, fix.warning, "a successful reconcile is a passed result")
	assert.Contains(t, fix.message, "backed by the store")
	assert.True(t, lfs.IsPointerFile(htmlPath), "repair never blanks a pointer")

	codes[oid] = http.StatusNotFound
	unchanged := evaluatePlanPointers(client, pointers, true, func() (*lfs.ReconcileResult, error) {
		return &lfs.ReconcileResult{}, nil
	})
	assert.True(t, unchanged.warning, "no recovered blob must keep the push blocked")
	assert.NotContains(t, unchanged.detail, "blank")
	assert.True(t, lfs.IsPointerFile(htmlPath))

	unverified := evaluatePlanPointers(client, pointers, true, func() (*lfs.ReconcileResult, error) {
		codes[oid] = http.StatusServiceUnavailable
		return &lfs.ReconcileResult{RecoveredUploads: 1}, nil
	})
	assert.True(t, unverified.warning, "a failed post-repair probe cannot certify success")
	assert.Contains(t, unverified.detail, "HTTP 503")
	codes[oid] = http.StatusNotFound

	// reconcile-failure path: a reconcile error must surface as a warning (with
	// the count preserved), never be swallowed into a passing result.
	failed := evaluatePlanPointers(client, pointers, true, func() (*lfs.ReconcileResult, error) {
		return nil, fmt.Errorf("batch check inconclusive: HTTP 401")
	})
	assert.True(t, failed.warning, "a reconcile error must warn, not pass")
	assert.Contains(t, failed.message, "reconcile failed")
	assert.Contains(t, failed.message, "1", "the missing count is preserved in the failure message")
}

// an incomplete Batch response cannot certify the missing plan after a repair.
func TestEvaluatePlanPointers_IncompleteStoreResponseKeepsWarning(t *testing.T) {
	oid := lfs.ComputeOID([]byte("pending render"))
	pointers := []planPointer{{Name: "pending", ref: lfs.NewFileRef([]byte("pending render"))}}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
		if calls == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"objects": []map[string]any{{"oid": oid, "error": map[string]any{"code": 404}}}})
		} else {
			_, _ = w.Write([]byte(`{"objects":[]}`))
		}
	}))
	defer server.Close()
	result := evaluatePlanPointers(lfs.NewClient(server.URL, "oauth2", "token"), pointers, true, func() (*lfs.ReconcileResult, error) { return &lfs.ReconcileResult{RecoveredUploads: 1}, nil })
	assert.True(t, result.warning)
	assert.NotContains(t, result.message, "backed by the store")
	assert.Contains(t, result.detail, "omitted plan blob")
}

// a failed initial inspection cannot mean healthy plans or authorize repair.
func TestEvaluatePlanPointers_UnavailableStoreCannotReportHealthy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	pointers := []planPointer{{Name: "pending", ref: lfs.NewFileRef([]byte("original plan"))}}
	result := evaluatePlanPointers(lfs.NewClient(server.URL, "oauth2", "token"), pointers, true, func() (*lfs.ReconcileResult, error) {
		t.Fatal("repair attempted without a successful inspection")
		return nil, nil
	})
	assert.True(t, result.warning)
	assert.Contains(t, result.message, "could not verify")
	assert.Contains(t, result.detail, "503")
	assert.NotContains(t, result.message, "backed by the store")
}
