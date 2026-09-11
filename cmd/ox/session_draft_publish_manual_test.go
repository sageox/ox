package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// session_publishing: manual must suppress the draft placeholder exactly as
// it suppresses the transcript upload and the start-registration POST. Before
// this fix, a grep for "SessionPublishing" in session_draft_publish.go
// returned zero hits: a draft commit — session name, agent id/type, model,
// repo id, username, title — landed in the shared ledger and was pushed by
// the daemon's sync cycle regardless of the setting.
//
// These tests drive the real maybePublishSessionDraft -> publishDraftPlaceholder
// -> commitDraftLocally chain against a real git ledger clone and a real bare
// remote, rather than stubbing any layer — mocking would hide exactly the
// failure class this guards (a gate bypassed because the mock never touches
// git, or because session.LoadRecordingStateForAgent never actually found the
// state the hook wrote).

// draftHookLedgerFixture is a project wired so a RecordingState under
// production's ledger-cache convention (paths.LedgerSessionCacheBase) is both
// (a) discoverable by session.LoadRecordingStateForAgent, which searches that
// exact computed path, and (b) resolved back to this ledger by
// deriveLedgerPath / resolveDraftLedgerPath in session_draft_publish.go.
//
// This is deliberately NOT newDraftLedgerFixture (session_draft_git_test.go):
// that fixture's callers drive commitDraftLocally / lfs.WriteDraftSessionMeta
// directly and never touch the RecordingState/turn-counter machinery, so an
// arbitrary temp dir for the ledger is fine there. maybePublishSessionDraft is
// the one caller that goes through the real hook and the real
// session.LoadAllRecordingStates search paths, so the ledger has to live where
// production actually puts it (config.Endpoint + repo_id -> paths.LedgersDataDir),
// or the fixture would silently prove nothing (state never found -> hook no-ops
// for a reason unrelated to session_publishing).
type draftHookLedgerFixture struct {
	projectRoot string
	ledgerPath  string
	repoID      string
}

const draftHookTestRepoID = "repo_draft_hook_test"
const draftHookTestEndpoint = "https://ledger-hook-test.invalid"

func newDraftHookLedgerFixture(t *testing.T) *draftHookLedgerFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	cacheHome := t.TempDir()
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("HOME", cacheHome)
	t.Setenv("XDG_CACHE_HOME", cacheHome)
	// internal/paths.getHomeDir() memoizes os.UserHomeDir() process-wide via
	// sync.Once, so t.Setenv("HOME", ...) alone is not reliable in a shared
	// test binary — whichever test in the package happens to call any
	// paths.*Dir() first wins the cache for every later test. XDG_DATA_HOME is
	// checked before that cache, so setting it explicitly is what actually
	// sandboxes paths.LedgersDataDir below. (Confirmed the hard way: an
	// earlier version of this fixture without this line wrote a real git
	// clone under the developer's actual ~/.local/share/sageox/.)
	t.Setenv("XDG_DATA_HOME", cacheHome)
	// A separate, unreachable endpoint for pushLedger's credential refresh /
	// remote rewrite, distinct from the project's OWN config.Endpoint below —
	// this test never pushes, but keeping the split matches
	// session_upload_push_test.go's isolatePushEnv convention.
	t.Setenv("SAGEOX_ENDPOINT", "https://test-only-no-creds.invalid")

	remoteBase := t.TempDir()
	barePath := filepath.Join(remoteBase, "remote.git")
	runGit(t, remoteBase, "init", "--bare", barePath)
	// No background git maintenance: receive-pack forks `gc --auto` and returns
	// without waiting, so that child can still be writing into remote.git when
	// t.TempDir() cleanup runs — a "directory not empty" failure unrelated to
	// anything the test asserts. Mirrors createBareAndClone.
	runGit(t, barePath, "config", "gc.auto", "0")
	runGit(t, barePath, "config", "receive.autogc", "false")
	runGit(t, barePath, "config", "maintenance.auto", "false")

	// The ledger clone MUST live at the exact path production computes from
	// (repoID, endpoint) — see the type doc comment.
	ledgerPath := paths.LedgersDataDir(draftHookTestRepoID, draftHookTestEndpoint)
	require.NoError(t, os.MkdirAll(filepath.Dir(ledgerPath), 0755))
	runGit(t, filepath.Dir(ledgerPath), "clone", barePath, ledgerPath)
	runGit(t, ledgerPath, "config", "user.email", "test@example.com")
	runGit(t, ledgerPath, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(ledgerPath, ".gitkeep"), nil, 0644))
	runGit(t, ledgerPath, "add", ".gitkeep")
	runGit(t, ledgerPath, "commit", "--no-verify", "-m", "initial")
	runGit(t, ledgerPath, "push")

	projectRoot := t.TempDir()
	runGit(t, projectRoot, "init")
	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, ".sageox"), 0755))
	require.NoError(t, config.SaveProjectConfig(projectRoot, &config.ProjectConfig{
		ConfigVersion: config.CurrentConfigVersion,
		RepoID:        draftHookTestRepoID,
		Endpoint:      draftHookTestEndpoint,
	}))
	require.NoError(t, config.SaveLocalConfig(projectRoot, &config.LocalConfig{
		Ledger: &config.LedgerConfig{Path: ledgerPath},
	}))

	return &draftHookLedgerFixture{projectRoot: projectRoot, ledgerPath: ledgerPath, repoID: draftHookTestRepoID}
}

// seedDraftHookState writes a RecordingState under the ledger-cache session
// path with a real user turn already in raw.jsonl (so publishDraftPlaceholder's
// HasUserTurn guard passes), at the given starting TurnCount, and persists it
// exactly as the real recording lifecycle would.
//
// LifecycleRegistrationState is deliberately "confirmed", not "deferred": a
// real session would have already gone through notifySessionStartedAsync by
// its second turn, and pinning it here keeps these tests isolated to the
// draft-commit gate rather than also exercising the registration gate
// (covered separately in session_notify_manual_test.go).
func (f *draftHookLedgerFixture) seedState(t *testing.T, agentID, sessionName string, startingTurnCount int) *session.RecordingState {
	t.Helper()
	sessionPath := filepath.Join(paths.LedgerSessionCacheBase(f.repoID, draftHookTestEndpoint), "sessions", sessionName)
	require.NoError(t, os.MkdirAll(sessionPath, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sessionPath, "raw.jsonl"),
		[]byte(`{"type":"header"}`+"\n"+`{"type":"user","content":"hello"}`+"\n"), 0644))

	state := &session.RecordingState{
		AgentID:                    agentID,
		SessionID:                  draftTestSessionID,
		SessionPath:                sessionPath,
		AdapterName:                "claude-code",
		StartedAt:                  time.Now().Add(-time.Minute),
		TurnCount:                  startingTurnCount,
		LifecycleRegistrationState: "confirmed",
	}
	require.NoError(t, session.SaveRecordingState(f.projectRoot, state))
	return state
}

func (f *draftHookLedgerFixture) stopHook(agentID string) {
	maybePublishSessionDraft(&HookContext{
		Phase: phaseStop, ProjectRoot: f.projectRoot,
		Marker: &SessionMarker{AgentID: agentID},
	})
}

func (f *draftHookLedgerFixture) setPublishing(t *testing.T, mode string) {
	t.Helper()
	projCfg, err := config.LoadProjectConfig(f.projectRoot)
	require.NoError(t, err)
	projCfg.SessionPublishing = mode
	require.NoError(t, config.SaveProjectConfig(f.projectRoot, projCfg))
}

func (f *draftHookLedgerFixture) draftCommitCount(t *testing.T, sessionName string) int {
	t.Helper()
	return len(commitsTouching(t, f.ledgerPath, "sessions/"+sessionName))
}

// TestMaybePublishSessionDraft_ManualMakesNoLedgerCommit is the red-first
// target: remove the session_publishing check added to maybePublishSessionDraft
// and this test fails with a draft commit present in the ledger.
func TestMaybePublishSessionDraft_ManualMakesNoLedgerCommit(t *testing.T) {
	f := newDraftHookLedgerFixture(t)
	const sessionName = "2026-01-01T00-00-testuser-OxManualDraft"
	f.setPublishing(t, config.SessionPublishingManual)

	// One turn below the publish threshold; the hook call below completes it.
	state := f.seedState(t, "OxDraftManual", sessionName, 1)

	f.stopHook(state.AgentID)

	updated, err := session.LoadRecordingStateForAgent(f.projectRoot, state.AgentID)
	require.NoError(t, err)
	require.NotNil(t, updated, "the hook must still find and update the recording state")

	assert.Equal(t, 2, updated.TurnCount, "the turn counter must still increment under manual publishing")
	assert.Zero(t, updated.DraftAttemptTurn, "manual publishing must never even attempt a draft")
	assert.Nil(t, updated.DraftPublishedAt)
	assert.Equal(t, 0, f.draftCommitCount(t, sessionName), "manual publishing must not commit a draft to the ledger")

	_, err = os.Stat(filepath.Join(f.ledgerPath, "sessions", sessionName, "meta.json"))
	assert.True(t, os.IsNotExist(err), "no meta.json may exist in the ledger tree for a manual-mode session")
}

// TestMaybePublishSessionDraft_AutoStillPublishes is the regression that
// matters most: auto publishing (the overwhelming default) must be completely
// unaffected by the manual-mode guard.
func TestMaybePublishSessionDraft_AutoStillPublishes(t *testing.T) {
	f := newDraftHookLedgerFixture(t)
	const sessionName = "2026-01-01T00-00-testuser-OxAutoDraft"
	f.setPublishing(t, config.SessionPublishingAuto)

	state := f.seedState(t, "OxDraftAuto", sessionName, 1)
	f.stopHook(state.AgentID)

	updated, err := session.LoadRecordingStateForAgent(f.projectRoot, state.AgentID)
	require.NoError(t, err)
	require.NotNil(t, updated)

	assert.Equal(t, 2, updated.TurnCount)
	assert.Equal(t, 2, updated.DraftAttemptTurn)
	assert.Equal(t, 2, updated.DraftPublishedTurn)
	require.NotNil(t, updated.DraftPublishedAt)
	assert.Equal(t, 1, f.draftCommitCount(t, sessionName), "auto publishing must still commit a draft")

	meta, err := lfs.ReadSessionMeta(draftLedgerSessionDir(f.ledgerPath, sessionName))
	require.NoError(t, err)
	assert.True(t, meta.IsDraft())
}

// TestMaybePublishSessionDraft_ManualNeverPublishesAcrossManyTurns guards
// against a refresh-interval-shaped bypass: even at the refresh boundary
// (turn 12 with the default 10-turn cadence), manual publishing must still
// commit nothing.
func TestMaybePublishSessionDraft_ManualNeverPublishesAcrossManyTurns(t *testing.T) {
	f := newDraftHookLedgerFixture(t)
	const sessionName = "2026-01-01T00-00-testuser-OxManualLong"
	f.setPublishing(t, config.SessionPublishingManual)

	state := f.seedState(t, "OxDraftManualLong", sessionName, 0)

	for i := 0; i < 15; i++ {
		f.stopHook(state.AgentID)
	}

	updated, err := session.LoadRecordingStateForAgent(f.projectRoot, state.AgentID)
	require.NoError(t, err)
	assert.Equal(t, 15, updated.TurnCount, "the counter must keep incrementing every turn")
	assert.Equal(t, 0, f.draftCommitCount(t, sessionName),
		"manual publishing must not commit even at the refresh boundary")
}

// TestMaybePublishSessionDraft_MidSessionFlipManualToAutoResumes proves the
// state machine is not wedged: a session that stays below the publish
// threshold while manual, then flips to auto, publishes on the very next
// eligible turn rather than being permanently skipped because an earlier
// manual-mode call already "used up" the publish turn.
func TestMaybePublishSessionDraft_MidSessionFlipManualToAutoResumes(t *testing.T) {
	f := newDraftHookLedgerFixture(t)
	const sessionName = "2026-01-01T00-00-testuser-OxFlipToAuto"
	f.setPublishing(t, config.SessionPublishingManual)

	state := f.seedState(t, "OxDraftFlipAuto", sessionName, 1)

	// Turn 2 under manual: nothing committed.
	f.stopHook(state.AgentID)
	require.Equal(t, 0, f.draftCommitCount(t, sessionName))

	f.setPublishing(t, config.SessionPublishingAuto)

	// Turn 3 under auto: "before the first success, retry every turn" means
	// this must publish now rather than waiting for a whole new refresh cycle.
	f.stopHook(state.AgentID)

	updated, err := session.LoadRecordingStateForAgent(f.projectRoot, state.AgentID)
	require.NoError(t, err)
	assert.Equal(t, 3, updated.TurnCount)
	assert.Equal(t, 3, updated.DraftPublishedTurn, "must publish on the first eligible turn after switching to auto")
	assert.Equal(t, 1, f.draftCommitCount(t, sessionName))
}

// TestMaybePublishSessionDraft_MidSessionFlipAutoToManualStopsRefreshing is the
// mirror case: a session published under auto, then flipped to manual, must
// not commit a refresh at the next refresh boundary.
func TestMaybePublishSessionDraft_MidSessionFlipAutoToManualStopsRefreshing(t *testing.T) {
	f := newDraftHookLedgerFixture(t)
	const sessionName = "2026-01-01T00-00-testuser-OxFlipToManual"
	f.setPublishing(t, config.SessionPublishingAuto)

	state := f.seedState(t, "OxDraftFlipManual", sessionName, 1)

	// Turn 2: publishes under auto.
	f.stopHook(state.AgentID)
	require.Equal(t, 1, f.draftCommitCount(t, sessionName))

	f.setPublishing(t, config.SessionPublishingManual)

	// Advance to the refresh boundary (published at 2, refresh every 10 -> 12).
	for turn := 3; turn <= 12; turn++ {
		f.stopHook(state.AgentID)
	}

	updated, err := session.LoadRecordingStateForAgent(f.projectRoot, state.AgentID)
	require.NoError(t, err)
	assert.Equal(t, 12, updated.TurnCount)
	assert.Equal(t, 2, updated.DraftPublishedTurn, "manual mode must not advance the published-turn watermark")
	assert.Equal(t, 1, f.draftCommitCount(t, sessionName),
		"manual mode must suppress the refresh at what would have been turn 12")
}
