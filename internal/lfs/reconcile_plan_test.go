package lfs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeLFSDownloadServer stands up a minimal Batch API server for the DOWNLOAD
// operation the reconcile uses to probe blob presence. codeByOID maps a bare OID
// to the HTTP code the server reports for it (0 == present, no error).
func fakeLFSDownloadServer(t *testing.T, codeByOID map[string]int) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Operation string `json:"operation"`
			Objects   []struct {
				OID  string `json:"oid"`
				Size int64  `json:"size"`
			} `json:"objects"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		type oerr struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		type dl struct {
			Href string `json:"href"`
		}
		type actions struct {
			Download *dl `json:"download,omitempty"`
		}
		type obj struct {
			OID     string   `json:"oid"`
			Size    int64    `json:"size"`
			Actions *actions `json:"actions,omitempty"`
			Error   *oerr    `json:"error,omitempty"`
		}
		resp := struct {
			Transfer string `json:"transfer"`
			Objects  []obj  `json:"objects"`
		}{Transfer: "basic"}
		for _, o := range req.Objects {
			ob := obj{OID: o.OID, Size: o.Size}
			if code := codeByOID[o.OID]; code != 0 {
				ob.Error = &oerr{Code: code, Message: "x"}
			} else {
				// reconcile only reads Error (BatchDownload), never fetches — a
				// static href is enough and avoids a self-reference on srv.
				ob.Actions = &actions{Download: &dl{Href: "http://127.0.0.1/blob"}}
			}
			resp.Objects = append(resp.Objects, ob)
		}
		w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "oauth2", "token")
}

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

// TestReconcile_BlanksMissingPlanPointerAndSquashes exercises the actual recovery
// core for a plan pointer: a 404 blob → the pointer is blanked to empty bytes and
// the unpushed history is squashed so the poisoned OID leaves the push pack.
//
// Failure prevented: the plan-walk tests above assert only ScannedPointers; this
// drives the blank+squash that unblocks a wedged ledger — the whole point of the
// #810 reconcile extension. A regression that scanned plan pointers but failed to
// blank/squash them would leave the ledger stuck.
func TestReconcile_BlanksMissingPlanPointerAndSquashes(t *testing.T) {
	ledger, _ := initLedgerWithRemote(t)

	oid := strings.Repeat("d", 64)
	planDir := filepath.Join(ledger, "data", "plans", "2026-08-24-orphan")
	require.NoError(t, os.MkdirAll(planDir, 0o755))
	htmlPath := filepath.Join(planDir, "plan.html")
	require.NoError(t, os.WriteFile(htmlPath, []byte(lfsPointerContent(oid, 500000)), 0o644))
	git(t, ledger, "add", ".")
	git(t, ledger, "commit", "-m", "plan: orphan pointer", "--no-verify")

	client := fakeLFSDownloadServer(t, map[string]int{oid: http.StatusNotFound})
	result, err := reconcileUnpushedPointers(context.Background(), ledger, nil,
		func() (*Client, error) { return client, nil })
	require.NoError(t, err)

	assert.Equal(t, 1, result.Replaced, "the orphaned plan pointer must be blanked")
	assert.True(t, result.Squashed, "unpushed commits collapse to one")

	info, err := os.Stat(htmlPath)
	require.NoError(t, err)
	assert.Zero(t, info.Size(), "blanked plan.html must be empty (content acknowledged gone)")
	assert.Equal(t, 1, unpushedCount(t, ledger), "history squashed to a single unpushed commit")
}

// TestReconcile_TransientErrorNeverBlanks pins the destructive-op guard for BOTH
// the plan and session trees: only a 404 proves absence. A 401 (token expired) or
// 5xx is inconclusive and must abort the reconcile, never zero out a live pointer
// — blanking is an operation with no inverse.
//
// Failure prevented: a flaky endpoint blanking real content to empty bytes.
func TestReconcile_TransientErrorNeverBlanks(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			ledger, _ := initLedgerWithRemote(t)

			planOID := strings.Repeat("e", 64)
			sessOID := strings.Repeat("f", 64)
			planHTML := filepath.Join(ledger, "data", "plans", "2026-08-24-p", "plan.html")
			sessRaw := filepath.Join(ledger, "sessions", "2026-08-24-s", "raw.jsonl")
			require.NoError(t, os.MkdirAll(filepath.Dir(planHTML), 0o755))
			require.NoError(t, os.MkdirAll(filepath.Dir(sessRaw), 0o755))
			planPtr := []byte(lfsPointerContent(planOID, 300000))
			sessPtr := []byte(lfsPointerContent(sessOID, 300000))
			require.NoError(t, os.WriteFile(planHTML, planPtr, 0o644))
			require.NoError(t, os.WriteFile(sessRaw, sessPtr, 0o644))
			git(t, ledger, "add", ".")
			git(t, ledger, "commit", "-m", "add pointers", "--no-verify")

			client := fakeLFSDownloadServer(t, map[string]int{planOID: code, sessOID: code})
			result, err := reconcileUnpushedPointers(context.Background(), ledger, nil,
				func() (*Client, error) { return client, nil })

			require.Error(t, err, "a non-404 batch result must abort, never blank")
			assert.Zero(t, result.Replaced)

			got, _ := os.ReadFile(planHTML)
			assert.Equal(t, planPtr, got, "plan pointer must be untouched on HTTP %d", code)
			got, _ = os.ReadFile(sessRaw)
			assert.Equal(t, sessPtr, got, "session pointer must be untouched on HTTP %d", code)
		})
	}
}
