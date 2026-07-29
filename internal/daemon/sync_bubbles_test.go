package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeKBLister is a test double for KBBubbleLister. Each test wires it up
// once and reads the recorded ListBubbles call count (and the scopes it
// was asked for) to assert behavior.
type fakeKBLister struct {
	bubbles []api.KB
	err     error
	callCnt int
	scopes  []api.KBScope
}

func (f *fakeKBLister) ListBubbles(_ context.Context, scope api.KBScope) ([]api.KB, error) {
	f.callCnt++
	f.scopes = append(f.scopes, scope)
	if f.err != nil {
		return nil, f.err
	}
	out := make([]api.KB, len(f.bubbles))
	copy(out, f.bubbles)
	return out, nil
}

// kbTestTeamID is the team binding kbProjectWithTeam writes. Ambient
// scope derivation (kb.AmbientScopes) turns it into the single team
// scope the daemon's sync loop lists.
const kbTestTeamID = "team_kbtest"

// kbTestEnv isolates the path resolution + endpoint state so each test
// gets its own XDG data home and a deterministic endpoint slug. Without
// this, paths.KBDir would either panic (no endpoint) or write into the
// developer's real ~/.local/share/sageox.
func kbTestEnv(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("XDG_DATA_HOME", tmp)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmp, "cache"))
	t.Setenv("SAGEOX_ENDPOINT", "https://staging.sageox.ai")
	// Bubble sync tests clone from local bare repos via file:// to simulate
	// remotes. Production (sync.go:1765, gitserver.TwoPhaseClone) hardens
	// against this with `-c protocol.file.allow=never`; tests must opt back
	// in. See gitserver.TestAllowFileTransport godoc for the rationale.
	prevAllowFile := gitserver.TestAllowFileTransport
	gitserver.TestAllowFileTransport = true
	t.Cleanup(func() { gitserver.TestAllowFileTransport = prevAllowFile })
}

// kbProjectWithTeam overlays the project's config.json with a team_id
// binding so the scoped kb sync derives a non-empty ambient scope set.
// Without this, syncBubbles skips before ever consulting the lister.
func kbProjectWithTeam(t *testing.T, projectDir string) {
	t.Helper()
	body := `{"endpoint":"https://fake.test.invalid","team_id":"` + kbTestTeamID + `"}`
	require.NoError(t, os.WriteFile(
		filepath.Join(projectDir, ".sageox", "config.json"), []byte(body), 0o644))
}

// kbTestScheduler builds a SyncScheduler with verbose-quiet logging and a
// project root configured (with a team binding, so ambient kb scopes
// resolve). The project root is required for syncBubbles to know which
// endpoint to use.
func kbTestScheduler(t *testing.T) (*SyncScheduler, string) {
	t.Helper()
	projectDir := setupProjectWithConfig(t, "")
	kbProjectWithTeam(t, projectDir)
	cfg := DefaultConfig()
	cfg.ProjectRoot = projectDir
	cfg.TeamContextSyncInterval = time.Second
	cfg.SyncIntervalRead = 2 * time.Second
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	s := NewSyncScheduler(cfg, logger)
	s.SetIssueTracker(NewIssueTracker())
	return s, projectDir
}

// makeBareRepo creates a bare repo on disk and seeds it with one commit
// from a working clone. Returns the bare path (suitable for file:// URL).
// Thin wrapper over initBareRepo (sync_test_helpers_managed_test.go) that
// adds the file-write/commit/push step.
func makeBareRepo(t *testing.T, name, fileName, fileContent string) string {
	t.Helper()
	bareDir, workDir := initBareRepo(t, name)
	require.NoError(t, os.WriteFile(filepath.Join(workDir, fileName), []byte(fileContent), 0o644))
	gitInDir(t, workDir, "add", fileName)
	gitInDir(t, workDir, "commit", "-m", "initial")
	gitInDir(t, workDir, "push", "origin", "HEAD:main")
	return bareDir
}

// pushExtraCommit pushes a follow-up commit to a bare repo through a fresh
// throwaway clone so we can verify subsequent pulls pick it up.
func pushExtraCommit(t *testing.T, bareDir, fileName, content string) {
	t.Helper()
	tmpClone := filepath.Join(t.TempDir(), "pusher")
	require.NoError(t, exec.Command("git", "clone", bareDir, tmpClone).Run())
	gitConfig(t, tmpClone)
	require.NoError(t, os.WriteFile(filepath.Join(tmpClone, fileName), []byte(content), 0o644))
	require.NoError(t, exec.Command("git", "-C", tmpClone, "add", fileName).Run())
	require.NoError(t, exec.Command("git", "-C", tmpClone, "commit", "-m", "update "+fileName).Run())
	require.NoError(t, exec.Command("git", "-C", tmpClone, "push", "origin", "HEAD:main").Run())
}

// TestSyncBubbles_InitialClone verifies that a bubble whose canonical
// XDG path does not yet exist is cloned end-to-end and the meta.json is
// written.
// Failure prevented: a fresh login on a new machine never materializes
// any bubble checkouts because the daemon was waiting for the canonical
// path to already exist (chicken-and-egg).
func TestSyncBubbles_InitialClone(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)

	s, _ := kbTestScheduler(t)

	bareDir := makeBareRepo(t, "personal", "notes.md", "hello\n")
	bubble := api.KB{
		KBID:        "kb_personal_1",
		KBType:      api.KBTypePersonal,
		Slug:        "personal-abc",
		OwnerUserID: "user_1",
		ViewerRole:  "owner",
		RepoURL:     "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	target := paths.KBDir(endpoint.Get(), bubble.KBID)
	require.NoDirExists(t, target, "precondition: target shouldn't exist yet")

	s.syncBubbles(context.Background())

	assert.DirExists(t, filepath.Join(target, ".git"), "clone should populate .git")
	assert.FileExists(t, filepath.Join(target, "notes.md"), "clone should materialize working tree")

	metaPath := filepath.Join(target, ".sageox", "meta.json")
	require.FileExists(t, metaPath)
	raw, err := os.ReadFile(metaPath)
	require.NoError(t, err)
	var meta kbMeta
	require.NoError(t, json.Unmarshal(raw, &meta))
	assert.Equal(t, api.KBTypePersonal, meta.Type)
	assert.Equal(t, "personal-abc", meta.Slug)
	assert.Equal(t, "user_1", meta.OwnerUserID)
	assert.Equal(t, "owner", meta.ViewerRole)
	assert.False(t, meta.LastSync.IsZero(), "last_sync must be populated")
}

// TestSyncBubbles_IncrementalPull verifies that an existing bubble dir
// picks up new commits on a subsequent reconciliation pass.
// Failure prevented: bubbles getting stuck at the snapshot they were
// first cloned at because the loop re-clone-only and never pulls.
func TestSyncBubbles_IncrementalPull(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git pull operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)

	s, _ := kbTestScheduler(t)

	bareDir := makeBareRepo(t, "team", "AGENTS.md", "v1\n")
	bubble := api.KB{
		KBID:    "kb_team_1",
		KBType:  api.KBTypeTeam,
		Slug:    "platform",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	// first pass: clone
	s.syncBubbles(context.Background())
	target := paths.KBDir(endpoint.Get(), bubble.KBID)
	require.DirExists(t, filepath.Join(target, ".git"))

	// push a new commit and run another pass
	pushExtraCommit(t, bareDir, "AGENTS.md", "v2\n")

	// nudge FETCH_HEAD into the past so the dedup threshold doesn't skip
	fetchHead := filepath.Join(target, ".git", "FETCH_HEAD")
	if info, err := os.Stat(fetchHead); err == nil {
		past := info.ModTime().Add(-10 * time.Minute)
		_ = os.Chtimes(fetchHead, past, past)
	}

	s.syncBubbles(context.Background())

	got, err := os.ReadFile(filepath.Join(target, "AGENTS.md"))
	require.NoError(t, err)
	assert.Equal(t, "v2\n", string(got), "second pass must pull the new commit")
}

// TestSyncBubbles_KBAPIUnavailable_NoOp verifies the loop returns
// silently when the kb API responds 403/404 (feature flag off).
// Failure prevented: rolling out the kb feature surfacing scary daemon
// warnings to every user whose flag is still off.
func TestSyncBubbles_KBAPIUnavailable_NoOp(t *testing.T) {
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	called := 0
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		called++
		return &fakeKBLister{err: api.ErrKBAPIUnavailable}
	})

	// must not panic, must not write anywhere
	s.syncBubbles(context.Background())
	assert.Equal(t, 1, called, "lister factory still invoked")

	base := paths.KBDir(endpoint.Get(), "")
	if entries, err := os.ReadDir(base); err == nil {
		assert.Empty(t, entries, "kb dir must remain empty when API unavailable")
	}
}

// TestSyncBubbles_PerBubbleFailureIsolation verifies that one failing
// bubble (bad URL) does not abort the rest of the pass — the second
// bubble still gets cloned.
// Failure prevented: a single revoked / mis-provisioned bubble taking
// down the entire sync loop and stalling every other bubble for the
// caller.
func TestSyncBubbles_PerBubbleFailureIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)

	s, _ := kbTestScheduler(t)

	bareDir := makeBareRepo(t, "good", "README.md", "ok\n")

	bad := api.KB{
		KBID:    "kb_bad",
		KBType:  api.KBTypeRepo,
		Slug:    "bad-bubble",
		RepoURL: "file:///nonexistent/path/that/cannot/be/cloned.git",
	}
	good := api.KB{
		KBID:    "kb_good",
		KBType:  api.KBTypeProfile,
		Slug:    "good-bubble",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bad, good}}
	})

	s.syncBubbles(context.Background())

	// the bad bubble should not have a populated checkout
	badTarget := paths.KBDir(endpoint.Get(), bad.KBID)
	if _, err := os.Stat(filepath.Join(badTarget, ".git")); err == nil {
		t.Fatalf("bad bubble unexpectedly succeeded at %s", badTarget)
	}

	// the good bubble must still be cloned despite the earlier failure
	goodTarget := paths.KBDir(endpoint.Get(), good.KBID)
	assert.DirExists(t, filepath.Join(goodTarget, ".git"), "good bubble must clone even when an earlier one failed")
	assert.FileExists(t, filepath.Join(goodTarget, ".sageox", "meta.json"))
}

// TestSyncBubbles_MetaJSONFields verifies meta.json holds exactly the
// fields the rest of the system relies on (type, slug, owner, role,
// last_sync).
// Failure prevented: a future field rename silently breaking `ox status`
// or `ox doctor` reads.
func TestSyncBubbles_MetaJSONFields(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)

	s, _ := kbTestScheduler(t)
	bareDir := makeBareRepo(t, "profile", "P.md", "p\n")
	bubble := api.KB{
		KBID:        "kb_profile_meta",
		KBType:      api.KBTypeProfile,
		Slug:        "ryan-profile",
		Name:        "Ryan Profile",
		ScopeType:   api.KBScopeTypeTeam,
		ScopeID:     kbTestTeamID,
		Description: "curated profile synthesis",
		Topics:      []string{"profile", "people"},
		OwnerUserID: "user_42",
		ViewerRole:  "owner",
		RepoURL:     "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	before := time.Now().Add(-time.Second)
	s.syncBubbles(context.Background())

	target := paths.KBDir(endpoint.Get(), bubble.KBID)
	raw, err := os.ReadFile(filepath.Join(target, ".sageox", "meta.json"))
	require.NoError(t, err)

	// also verify the JSON keys themselves so a struct rename is caught.
	var generic map[string]any
	require.NoError(t, json.Unmarshal(raw, &generic))
	assert.Equal(t, "profile", generic["type"])
	assert.Equal(t, "ryan-profile", generic["slug"])
	assert.Equal(t, "Ryan Profile", generic["name"])
	assert.Equal(t, "team", generic["scope_type"])
	assert.Equal(t, kbTestTeamID, generic["scope_id"])
	assert.Equal(t, "curated profile synthesis", generic["description"])
	assert.Equal(t, []any{"profile", "people"}, generic["topics"])
	assert.Equal(t, "user_42", generic["owner_user_id"])
	assert.Equal(t, "owner", generic["viewer_role"])

	var meta kbMeta
	require.NoError(t, json.Unmarshal(raw, &meta))
	assert.True(t, meta.LastSync.After(before), "last_sync should be set to ~now")
}

// TestSyncBubbles_ScopedList verifies syncBubbles asks the lister for
// exactly the project's ambient team scope — not a contextless list.
// Failure prevented: the daemon regressing to a bare (scope-less) list
// request, which the server 400s under ADR-073, silently killing kb
// sync for every user.
func TestSyncBubbles_ScopedList(t *testing.T) {
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	lister := &fakeKBLister{}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister { return lister })

	s.syncBubbles(context.Background())

	require.Len(t, lister.scopes, 1, "one list call per ambient scope (v1: team only)")
	assert.Equal(t, api.KBScope{Type: api.KBScopeTypeTeam, ID: kbTestTeamID}, lister.scopes[0])
}

// TestSyncBubbles_NoTeamScope_NoOp verifies the loop is a silent no-op
// when the project has no team binding — there is no ambient scope to
// list, and the scoped endpoint cannot be called without one.
// Failure prevented: an un-bound project issuing scope-less requests
// (server 400) every cadence tick.
func TestSyncBubbles_NoTeamScope_NoOp(t *testing.T) {
	kbTestEnv(t)
	projectDir := setupProjectWithConfig(t, "") // config.json WITHOUT team_id
	cfg := DefaultConfig()
	cfg.ProjectRoot = projectDir
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	s := NewSyncScheduler(cfg, logger)

	lister := &fakeKBLister{}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister { return lister })

	s.syncBubbles(context.Background())
	assert.Zero(t, lister.callCnt, "must not call the lister without an ambient scope")
}

// TestSyncBubbles_NoProjectRoot_NoOp verifies the loop is a silent no-op
// when the daemon has no project root configured (pre-init or odd
// startup state).
// Failure prevented: panicking inside endpoint.GetForProject("") when
// the daemon starts in a workspace that hasn't run `ox init` yet.
func TestSyncBubbles_NoProjectRoot_NoOp(t *testing.T) {
	kbTestEnv(t)

	cfg := DefaultConfig()
	cfg.ProjectRoot = ""
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	s := NewSyncScheduler(cfg, logger)

	called := false
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		called = true
		return &fakeKBLister{}
	})
	s.syncBubbles(context.Background())
	assert.False(t, called, "must not call lister without a project root")
}

// TestSyncBubbles_ListErrorIsLoggedNotPanicked verifies that non-sentinel
// errors from the kb API are logged + swallowed, never propagated as
// panics.
// Failure prevented: a brief 500 from the kb endpoint crashing the
// scheduler tick.
func TestSyncBubbles_ListErrorIsLoggedNotPanicked(t *testing.T) {
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{err: errors.New("boom")}
	})
	assert.NotPanics(t, func() {
		s.syncBubbles(context.Background())
	})
}
