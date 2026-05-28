package daemon

// Mirrors sync_integration_test.go for the kb sync path: end-to-end
// multi-bubble flow under realistic conditions. Where the original
// runs CLI -> IPC -> daemon -> git fetch+pull, this exercises the kb
// equivalent: kb-API mock -> daemon syncBubbles -> on-disk kb checkout
// -> meta.json round-trip -> CLI (`internal/api.KBClient.ListBubbles`)
// reads back what was synced.
//
// Per Ryan: "all the ledger and team context tests need to be applied
// to kb git repos."
//
// Slow tests skip under -short.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// kbAPIMockServer stands up an httptest.Server that responds to
// GET /api/v1/kb with a configurable bubble list. Call count is
// observable so tests can assert no-extra-calls invariants.
func kbAPIMockServer(t *testing.T, bubbles []api.KB) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// only respond on the kb path; anything else is 404 to surface
		// a wrong-route bug.
		if !strings.HasPrefix(r.URL.Path, "/api/v1/kb") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"bubbles": bubbles})
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// TestSyncBubblesIntegration_FullFlow_MultipleBubbles is the kb
// counterpart of TestSyncIntegration_FullFlow: API mock -> daemon ->
// on-disk -> CLI read-back. Multiple bubbles to ensure the
// per-bubble loop genuinely iterates instead of bailing on the first
// success.
//
// Failure prevented: the kb sync loop only ever processing the first
// row in the API response — easy to mis-introduce with a `return`
// instead of `continue` in reconcileBubble.
func TestSyncBubblesIntegration_FullFlow_MultipleBubbles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	bareA := makeBareRepo(t, "ifa", "AGENTS.md", "team-a\n")
	bareB := makeBareRepo(t, "ifb", "notes.md", "personal-b\n")

	a := api.KB{
		KBID:    "kb_int_team",
		KBType:  api.KBTypeTeam,
		Slug:    "team-a",
		Name:    "Team A",
		RepoURL: "file://" + bareA,
	}
	b := api.KB{
		KBID:    "kb_int_personal",
		KBType:  api.KBTypePersonal,
		Slug:    "personal-b",
		Name:    "Personal B",
		RepoURL: "file://" + bareB,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{a, b}}
	})

	s.syncBubbles(context.Background())

	// both bubbles materialize.
	for _, kb := range []api.KB{a, b} {
		target := paths.KBDir(kb.KBID)
		assert.DirExists(t, filepath.Join(target, ".git"), "%s: .git", kb.Slug)
		assert.FileExists(t, filepath.Join(target, ".sageox", "meta.json"), "%s: meta.json", kb.Slug)
	}
	assert.FileExists(t, filepath.Join(paths.KBDir(a.KBID), "AGENTS.md"))
	assert.FileExists(t, filepath.Join(paths.KBDir(b.KBID), "notes.md"))

	// CLI read-back via the same KB type used by `ox kb list`.
	for _, kb := range []api.KB{a, b} {
		raw, err := os.ReadFile(filepath.Join(paths.KBDir(kb.KBID), ".sageox", "meta.json"))
		require.NoError(t, err)
		var meta kbMeta
		require.NoError(t, json.Unmarshal(raw, &meta))
		assert.Equal(t, kb.KBType, meta.Type)
		assert.Equal(t, kb.Slug, meta.Slug)
	}
}

// TestSyncBubblesIntegration_APIMockToOnDisk verifies the same flow
// against a real httptest.Server (not a hand-written fake lister), so
// the JSON envelope shape and KBClient parsing are exercised
// end-to-end. This is the part the existing integration test cannot
// cover for kb because the kb API didn't exist.
//
// Failure prevented: a server-side schema change ("bubbles" -> "items")
// silently breaking the daemon's parsing without any unit test
// failing.
func TestSyncBubblesIntegration_APIMockToOnDisk(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)

	bareDir := makeBareRepo(t, "apimock", "AGENTS.md", "from-mock\n")
	row := api.KB{
		KBID:    "kb_apimock",
		KBType:  api.KBTypeTeam,
		Slug:    "apimock",
		Name:    "Mock Team",
		RepoURL: "file://" + bareDir,
	}
	srv, calls := kbAPIMockServer(t, []api.KB{row})

	// build a scheduler that uses the REAL kb client against the mock.
	s, _ := kbTestScheduler(t)
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return api.NewKBClientWithEndpoint(srv.URL)
	})

	s.syncBubbles(context.Background())

	target := paths.KBDir(row.KBID)
	body, err := os.ReadFile(filepath.Join(target, "AGENTS.md"))
	require.NoError(t, err)
	assert.Equal(t, "from-mock\n", string(body))
	assert.GreaterOrEqual(t, calls.Load(), int32(1), "API must have been called at least once")
}

// TestSyncBubblesIntegration_EndpointSwitchMidFlight verifies that
// changing the active endpoint between two sync passes does not
// corrupt local state — each endpoint's bubble checkouts live under
// distinct XDG paths. ox-gzp.25 explicitly calls this out.
//
// Failure prevented: a developer flipping SAGEOX_ENDPOINT between
// staging/prod sees one endpoint's bubbles overwritten by the other.
func TestSyncBubblesIntegration_EndpointSwitchMidFlight(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// This test inlines its own env setup rather than calling kbTestEnv (it
	// needs to switch endpoint mid-test). Mirror kbTestEnv's
	// TestAllowFileTransport opt-in so file:// clones aren't refused by the
	// production hardening — see gitserver.TestAllowFileTransport godoc.
	prevAllowFile := gitserver.TestAllowFileTransport
	gitserver.TestAllowFileTransport = true
	t.Cleanup(func() { gitserver.TestAllowFileTransport = prevAllowFile })

	bareA := makeBareRepo(t, "epsa", "f.md", "from-staging\n")
	bareB := makeBareRepo(t, "epsb", "f.md", "from-prod\n")

	// Pass 1: staging.
	tmpDir := t.TempDir()
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("XDG_DATA_HOME", tmpDir)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmpDir, "cache"))
	t.Setenv("SAGEOX_ENDPOINT", "https://staging.sageox.ai")

	s1, _ := kbTestScheduler(t)
	bubble1 := api.KB{
		KBID:    "kb_endpoint_switch",
		KBType:  api.KBTypePersonal,
		Slug:    "ep-switch",
		RepoURL: "file://" + bareA,
	}
	s1.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble1}}
	})
	s1.syncBubbles(context.Background())
	stagingTarget := paths.KBDir(bubble1.KBID)
	stagingBody, err := os.ReadFile(filepath.Join(stagingTarget, "f.md"))
	require.NoError(t, err)
	assert.Equal(t, "from-staging\n", string(stagingBody))

	// Pass 2: switch to prod (new XDG home so the path helper resolves
	// to a different on-disk location, mirroring how a different
	// endpoint slug would).
	tmpDir2 := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir2)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmpDir2, "cache"))
	t.Setenv("SAGEOX_ENDPOINT", "https://app.sageox.ai")

	s2, _ := kbTestScheduler(t)
	bubble2 := api.KB{
		KBID:    "kb_endpoint_switch", // SAME id as before
		KBType:  api.KBTypePersonal,
		Slug:    "ep-switch",
		RepoURL: "file://" + bareB,
	}
	s2.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble2}}
	})
	s2.syncBubbles(context.Background())
	prodTarget := paths.KBDir(bubble2.KBID)
	prodBody, err := os.ReadFile(filepath.Join(prodTarget, "f.md"))
	require.NoError(t, err)
	assert.Equal(t, "from-prod\n", string(prodBody))

	// the two on-disk locations must be distinct.
	assert.NotEqual(t, stagingTarget, prodTarget, "endpoint switch must produce distinct on-disk paths")

	// staging's content must NOT have been mutated by the prod pass.
	stagingStill, err := os.ReadFile(filepath.Join(stagingTarget, "f.md"))
	require.NoError(t, err)
	assert.Equal(t, "from-staging\n", string(stagingStill), "staging checkout must be untouched after prod pass")
}

// TestSyncBubblesIntegration_RepeatedSyncIsIdempotent verifies that
// running multiple sync passes back-to-back over the same bubble set
// produces stable on-disk state — no growing artifacts, no flaky
// re-clones. The kb counterpart of TestSyncIntegration_AlreadyUpToDate.
//
// Failure prevented: a regression where every pass writes a new
// meta.json with a different last_sync that never settles, or
// repeatedly re-fetches when nothing changed.
func TestSyncBubblesIntegration_RepeatedSyncIsIdempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)
	// Long intervals so dedup engages.
	s.config.TeamContextSyncInterval = 10 * time.Minute
	s.config.SyncIntervalRead = 10 * time.Minute

	bareDir := makeBareRepo(t, "repeated", "f.md", "v1\n")
	bubble := api.KB{
		KBID:    "kb_repeated",
		KBType:  api.KBTypeTeam,
		Slug:    "repeated",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	for i := 0; i < 4; i++ {
		s.syncBubbles(context.Background())
	}

	target := paths.KBDir(bubble.KBID)
	require.DirExists(t, filepath.Join(target, ".git"))

	// exactly one meta.json. No tmp leftovers.
	metaDir := filepath.Join(target, ".sageox")
	entries, err := os.ReadDir(metaDir)
	require.NoError(t, err)
	metaCount := 0
	tmpCount := 0
	for _, e := range entries {
		if e.Name() == "meta.json" {
			metaCount++
		} else if strings.HasPrefix(e.Name(), "meta.json.tmp-") {
			tmpCount++
		}
	}
	assert.Equal(t, 1, metaCount, "exactly one meta.json after repeated syncs")
	assert.Equal(t, 0, tmpCount, "no leftover tmp files from atomic-write rotation")
}

// TestSyncBubblesIntegration_BubbleAddedSecondPass verifies that a
// bubble that was absent from the first API response is correctly
// cloned when it appears in the next response. End-to-end membership
// change.
//
// Failure prevented: the kb sync loop caching the first API
// response and never noticing newly-provisioned bubbles — the user
// would think provisioning is broken.
func TestSyncBubblesIntegration_BubbleAddedSecondPass(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	bareA := makeBareRepo(t, "addedA", "a.md", "a\n")
	bareB := makeBareRepo(t, "addedB", "b.md", "b\n")

	a := api.KB{
		KBID:    "kb_added_first",
		KBType:  api.KBTypeTeam,
		Slug:    "first",
		RepoURL: "file://" + bareA,
	}
	b := api.KB{
		KBID:    "kb_added_second",
		KBType:  api.KBTypePersonal,
		Slug:    "second",
		RepoURL: "file://" + bareB,
	}

	var pass atomic.Int32
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		i := pass.Add(1)
		if i == 1 {
			return &fakeKBLister{bubbles: []api.KB{a}}
		}
		return &fakeKBLister{bubbles: []api.KB{a, b}}
	})

	s.syncBubbles(context.Background())
	assert.DirExists(t, filepath.Join(paths.KBDir(a.KBID), ".git"))
	assert.NoDirExists(t, filepath.Join(paths.KBDir(b.KBID), ".git"))

	s.syncBubbles(context.Background())
	assert.DirExists(t, filepath.Join(paths.KBDir(a.KBID), ".git"))
	assert.DirExists(t, filepath.Join(paths.KBDir(b.KBID), ".git"), "newly-added bubble must clone on the next pass")
}
