package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/plan"
	"github.com/sageox/ox/pkg/planreview"
	"github.com/stretchr/testify/require"
)

// Failure prevented: retries lose marks, reopen resolved feedback, or commit
// unfinished authoring files when clones publish onto different heads.
func TestPlanReviewDurability_MixedToolsRecoverAcrossClones(t *testing.T) {
	f := newDurableReviewFixture(t)
	rel, err := filepath.Rel(f.ledger, f.planDir)
	require.NoError(t, err)
	clone := func() string {
		t.Helper()
		dir := filepath.Join(t.TempDir(), "ledger")
		runGitInDir(t, filepath.Dir(dir), "clone", "-q", "-b", "main", f.origin, dir)
		runGitInDir(t, dir, "config", "user.email", "test@example.com")
		runGitInDir(t, dir, "config", "user.name", "Test")
		runGitInDir(t, dir, "config", "commit.gpgsign", "false")
		return dir
	}
	c1, c2 := clone(), clone()
	// Each HTTP handler must write to its own clone, as independent processes do.
	prev := planLedgerPathFor
	planLedgerPathFor = func(root string) (string, error) { return root, nil }
	t.Cleanup(func() { planLedgerPathFor = prev })
	serve := func(dir string) *httptest.Server {
		t.Helper()
		h := liveReviewHandler(dir, "durable", filepath.Join(dir, rel), "http://x", "secret",
			newBroadcaster(), make(chan int, 16), make(chan struct{}, 1))
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		return srv
	}
	s0, s1, s2 := serve(f.ledger), serve(c1), serve(c2)
	post := func(srv *httptest.Server, path, body string) reviewDurabilityResp {
		t.Helper()
		code, answer := reviewPOSTBody(t, srv.URL+path, "secret", body)
		require.Equal(t, http.StatusOK, code, answer)
		r := decodeReviewResp(t, answer)
		require.True(t, r.Saved, answer)
		return r
	}
	blob := func(dir, ref string) []byte {
		t.Helper()
		cmd := exec.Command("git", "show", ref)
		cmd.Dir, cmd.Env = dir, isolatedGitEnv(dir)
		b, err := cmd.Output()
		require.NoError(t, err)
		return b
	}

	drawing, region := drawingExport(t)
	element := `{"anchor":"h-element","label":"Risks","status":"request-change","note":"Bound retries","future":{"n":9007199254740993}}`
	highlight := `{"anchor":"q-highlight","label":"exact words","quote":"retry twice","status":"flag","note":"Keep these words"}`
	frozen := `{"id":"mixed-round-0001","reviewer":"Devon","future":{"keep":true},"items":[],"marks":[` + element + `,` + highlight + `,` + drawing + `]}`

	lock := filepath.Join(f.ledger, ".git", "index.lock")
	require.NoError(t, os.WriteFile(lock, nil, 0o644))
	r := post(s0, "/feedback", frozen)
	require.False(t, r.Committed)
	require.False(t, r.Pushed)
	require.NoError(t, os.Remove(lock))
	entries, err := listReviewPending(f.ledger)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	original := entries[0]
	var round planreview.Round
	require.NoError(t, json.Unmarshal(original.Data, &round))
	require.Equal(t, sha256Hex([]byte(frozen)), round.RequestSHA256)

	// A clone that never saw the first attempt stores an equivalent copy while
	// offline. A third clone then moves the remote before either retry lands.
	runGitInDir(t, c1, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "offline.git"))
	r = post(s1, "/feedback", frozen)
	require.True(t, r.Committed)
	require.False(t, r.Pushed)
	c1Pending, err := listReviewPending(c1)
	require.NoError(t, err)
	require.Len(t, c1Pending, 1)
	require.True(t, post(s2, "/feedback", roundBody("other-round-0001", "h-other")).Pushed)
	anchors := []string{"h-element", "q-highlight", "q-r4-abort-item"}
	for i, anchor := range anchors {
		accept := fmt.Sprintf(`{"id":"verify-mixed-%04d","reviewer":"Devon","anchor":%q,"state":"verified","note":"Checked"}`, i, anchor)
		// Reviewers may verify an item, not mark it addressed.
		before := remoteFiles(t, f.origin)
		code, _ := reviewPOSTBody(t, s2.URL+"/accept", "secret", strings.Replace(accept, "verified", "addressed", 1))
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, before, remoteFiles(t, f.origin))
		require.True(t, post(s2, "/accept", accept).Pushed)
	}
	runGitInDir(t, c1, "remote", "set-url", "origin", f.origin)
	r = post(s1, "/feedback", frozen) // the response was lost; resend frozen bytes
	require.NotNil(t, r.Duplicate)
	require.True(t, *r.Duplicate)
	require.True(t, r.Pushed)

	// Staged and unstaged authoring edits remain local while the retry rebases
	// onto the other clones. Git's autostash may change their staging state.
	require.NoError(t, os.WriteFile(filepath.Join(f.planDir, "meta.json"), []byte(`{"topic":"Staged draft"}`), 0o644))
	runGitInDir(t, f.ledger, "add", filepath.ToSlash(filepath.Join(rel, "meta.json")))
	require.NoError(t, os.WriteFile(filepath.Join(f.planDir, "plan.md"), []byte("# Unstaged draft\n"), 0o644))
	s0.Close()
	entries, err = listReviewPending(f.ledger)
	require.NoError(t, err)
	require.Equal(t, []reviewPending{original}, entries)
	ok, failed, err := flushPendingReviewRecords(context.Background(), f.ledger)
	require.NoError(t, err)
	require.Equal(t, 1, ok)
	require.Zero(t, failed)
	require.Equal(t, original.Data, blob(f.origin, "main:"+original.PlanDir+"/"+original.Path))
	require.Equal(t, c1Pending[0].Data, blob(f.origin, "main:"+c1Pending[0].PlanDir+"/"+c1Pending[0].Path))
	require.Equal(t, 3, feedbackRoundsOnRemote(t, f.origin))
	require.Equal(t, 3, strings.Count(remoteFiles(t, f.origin), "/feedback/resolutions/"))
	draft, err := os.ReadFile(filepath.Join(f.planDir, "plan.md"))
	require.NoError(t, err)
	require.Equal(t, "# Unstaged draft\n", string(draft))
	meta, err := os.ReadFile(filepath.Join(f.planDir, "meta.json"))
	require.NoError(t, err)
	require.Equal(t, `{"topic":"Staged draft"}`, string(meta))
	require.Equal(t, "# Durable\n", string(blob(f.origin, "main:"+filepath.ToSlash(filepath.Join(rel, "plan.md")))))
	require.JSONEq(t, `{"topic":"Durable"}`, string(blob(f.origin, "main:"+filepath.ToSlash(filepath.Join(rel, "meta.json")))))
	for _, dir := range []string{f.ledger, c1} {
		left, err := listReviewPending(dir)
		require.NoError(t, err)
		require.Empty(t, left)
	}

	// Reconstruct from the remote alone: two physical mixed-tool records are
	// one logical round; the late copy must not undo any verification.
	fresh := clone()
	rev := plan.ReadReview(filepath.Join(fresh, rel))
	require.True(t, rev.Complete(), "%+v", rev)
	require.Len(t, rev.Rounds, 2)
	require.Len(t, rev.Items, 4)
	for _, it := range rev.Items {
		if it.Anchor == "h-other" {
			continue
		}
		require.False(t, it.Open, "%+v", it)
		require.NotNil(t, it.Resolution)
		require.Equal(t, planreview.StateVerified, it.Resolution.State)
		if it.Anchor == "q-highlight" {
			require.Equal(t, "retry twice", it.Quote)
		}
		if it.Anchor == "q-r4-abort-item" {
			var got any
			require.NoError(t, json.Unmarshal(it.Region, &got))
			require.Equal(t, region, got)
		}
	}
	require.Contains(t, string(original.Data), `9007199254740993`)
	var stored map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(original.Data, &stored))
	require.JSONEq(t, `{"keep":true}`, string(stored["future"]))

	// Conflicting reuse of the same id is refused without adding a record.
	s3 := serve(fresh)
	before := remoteFiles(t, f.origin)
	code, _ := reviewPOSTBody(t, s3.URL+"/feedback", "secret", strings.Replace(frozen, "Bound retries", "Conflicting note", 1))
	require.Equal(t, http.StatusConflict, code)
	require.Equal(t, before, remoteFiles(t, f.origin))
	for i, item := range []string{element, highlight, drawing} {
		reopen := fmt.Sprintf(`{"id":"reopen-mixed-%04d","reviewer":"Devon","items":[],"marks":[%s]}`, i,
			strings.ReplaceAll(item, `"status":"flag"`, `"status":"request-change"`))
		require.True(t, post(s3, "/feedback", reopen).Pushed)
	}
	final := plan.ReadReview(filepath.Join(clone(), rel))
	require.True(t, final.Complete(), "%+v", final)
	require.Len(t, final.Items, 4)
	for _, it := range final.Items {
		require.True(t, it.Open, "%+v", it)
	}
	// Each review commit contains one record.
	reviewCommits := strings.Fields(runGitInDir(t, f.origin, "log", "--format=%H", "--grep=plan review:", "main"))
	require.NotEmpty(t, reviewCommits, "no review commits found")
	for _, sha := range reviewCommits {
		files := strings.Fields(runGitInDir(t, f.origin, "show", "--name-only", "--format=", sha))
		require.Len(t, files, 1)
		require.Contains(t, files[0], "/feedback/")
	}
}
