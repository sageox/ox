package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The orphaned-draft reaper is the ONLY thing that ever reclaims a placeholder
// whose recording died. Everything else deliberately ignores drafts: the
// daemon's anti-entropy skips them, prune no longer counts them as uploaded,
// retraction runs only from a clean stop, and deletion only from an explicit
// abort — none of which a crashed agent reaches.
//
// The asymmetry that shapes every test here: a false NEGATIVE leaves a stale
// "in progress" page, while a false POSITIVE deletes a LIVE session's
// placeholder. So the conservative direction is always "not an orphan", and
// each guard gets its own negative control.

// draftReaperFixture is a project + ledger with NO git init and no
// skipIntegration gate, so the reaper's decision table runs in the fast loop.
//
// This matters more than it looks. The table is the guard against DELETING A
// LIVE SESSION'S placeholder, and findOrphanedDrafts takes explicit paths — it
// needs no git, no remote, and no daemon. Built on setupLedgerProject it
// inherited that helper's skipIntegration gate, so under `-short` every subtest
// skipped while the PARENT still reported PASS: a green, empty table in exactly
// the run developers do before committing.
func draftReaperFixture(t *testing.T) (projectRoot, ledgerPath string) {
	t.Helper()
	projectRoot = t.TempDir()
	ledgerPath = t.TempDir()

	// A real `git init`: getLedgerPath resolves through findGitRoot, which
	// shells out to `git rev-parse --show-toplevel`. Cheap (~10ms) and it keeps
	// these tests in the fast loop, unlike the full bare-remote fixture.
	require.NoError(t, exec.Command("git", "-C", projectRoot, "init", "--quiet").Run())

	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"),
		[]byte(`{"config_version":"2","repo_id":"repo_reaper_test"}`), 0644))

	// Register the ledger so the helpers that resolve it internally
	// (draftViewNotice, checkSessionDraftOrphan) find it, not only the ones
	// that take explicit paths.
	require.NoError(t, config.SaveLocalConfig(projectRoot, &config.LocalConfig{
		Ledger: &config.LedgerConfig{Path: ledgerPath},
	}))

	cacheDir := t.TempDir()
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("HOME", cacheDir)
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	return projectRoot, ledgerPath
}

// agedDraft writes a draft whose updated_at is `age` in the past.
// Ages are passed explicitly and far from the 72h threshold rather than nudged
// by a few seconds, so a slow CI machine can never flip a boundary.
func agedDraft(t *testing.T, ledgerPath, sessionName string, age time.Duration) string {
	t.Helper()
	dir := filepath.Join(ledgerPath, "sessions", sessionName)
	require.NoError(t, os.MkdirAll(dir, 0755))
	updated := time.Now().Add(-age).UTC()
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, &lfs.SessionMeta{
		Version: "1.0", SessionName: sessionName, SessionID: draftTestSessionID,
		AgentID: "OxOrphan", CreatedAt: updated,
		Draft: true, TurnCount: 2, UpdatedAt: &updated,
		Files: map[string]lfs.FileRef{},
	}))
	return dir
}

// TestFindOrphanedDrafts_OnlyReapsTrulyAbandoned is the whole decision table.
// Every non-orphan row is a way a LIVE session could be destroyed.
func TestFindOrphanedDrafts_OnlyReapsTrulyAbandoned(t *testing.T) {
	setTestCfg(t)

	tests := []struct {
		name       string
		setup      func(t *testing.T, projectRoot, ledgerPath, sessionName string)
		wantOrphan bool
		why        string
	}{
		{
			name: "stale draft with no local data anywhere",
			setup: func(t *testing.T, _, ledgerPath, name string) {
				agedDraft(t, ledgerPath, name, 120*time.Hour)
			},
			wantOrphan: true,
			why:        "nothing else will ever reclaim this",
		},
		{
			name: "recently refreshed draft",
			setup: func(t *testing.T, _, ledgerPath, name string) {
				agedDraft(t, ledgerPath, name, 1*time.Minute)
			},
			why: "a refreshing draft is a LIVE session",
		},
		{
			name: "stale draft whose recording is still in the ledger cache",
			setup: func(t *testing.T, _, ledgerPath, name string) {
				agedDraft(t, ledgerPath, name, 120*time.Hour)
				cache := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", name)
				require.NoError(t, os.MkdirAll(cache, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(cache, "raw.jsonl"),
					[]byte(`{"type":"header"}`+"\n"), 0644))
			},
			why: "a cached transcript is recoverable work; upload-retry owns it, not the reaper",
		},
		{
			name: "stale draft with an active recording marker in the cache",
			setup: func(t *testing.T, _, ledgerPath, name string) {
				agedDraft(t, ledgerPath, name, 120*time.Hour)
				cache := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", name)
				require.NoError(t, os.MkdirAll(cache, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(cache, ".recording.json"),
					[]byte(`{"agent_id":"OxOrphan"}`), 0644))
			},
			why: "an existing recording marker means the session may still be alive",
		},
		{
			name: "stale draft whose recording is in the XDG cache",
			setup: func(t *testing.T, projectRoot, ledgerPath, name string) {
				agedDraft(t, ledgerPath, name, 120*time.Hour)
				makeXDGCacheSession(t, projectRoot, name)
			},
			why: "the XDG cache is a real session location and must be searched too",
		},
		{
			name: "draft with NO updated_at cannot be aged",
			setup: func(t *testing.T, _, ledgerPath, name string) {
				dir := filepath.Join(ledgerPath, "sessions", name)
				require.NoError(t, os.MkdirAll(dir, 0755))
				require.NoError(t, lfs.WriteSessionMetaOnly(dir, &lfs.SessionMeta{
					Version: "1.0", SessionName: name, SessionID: draftTestSessionID,
					CreatedAt: time.Now().Add(-120 * time.Hour), Draft: true,
					Files: map[string]lfs.FileRef{},
				}))
			},
			why: "without a heartbeat there is no evidence of death; refuse rather than guess",
		},
		{
			name: "finalized session, however old",
			setup: func(t *testing.T, _, ledgerPath, name string) {
				finalizedLedgerSession(t, ledgerPath, name)
			},
			why: "NEGATIVE CONTROL: the reaper must never touch real work",
		},
		{
			name: "unreadable meta.json",
			setup: func(t *testing.T, _, ledgerPath, name string) {
				dir := filepath.Join(ledgerPath, "sessions", name)
				require.NoError(t, os.MkdirAll(dir, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"draft":tr`), 0644))
			},
			why: "we cannot classify it, so we do not delete it",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			projectRoot, ledgerPath := draftReaperFixture(t)
			const sessionName = "2026-01-01T00-00-testuser-OxOrph1"
			tc.setup(t, projectRoot, ledgerPath, sessionName)

			orphans, err := findOrphanedDrafts(projectRoot, ledgerPath)
			require.NoError(t, err)

			if tc.wantOrphan {
				assert.Contains(t, orphans, sessionName, tc.why)
				return
			}
			assert.NotContains(t, orphans, sessionName, tc.why)
		})
	}
}

// TestFindOrphanedDrafts_OrdersOldestFirst — the non-fix report names the
// oldest orphan, so a stable order matters for a legible message.
func TestFindOrphanedDrafts_OrdersOldestFirst(t *testing.T) {
	setTestCfg(t)
	projectRoot, ledgerPath := draftReaperFixture(t)

	agedDraft(t, ledgerPath, "2026-01-01T00-00-testuser-OxNewer", 96*time.Hour)
	agedDraft(t, ledgerPath, "2026-01-01T00-00-testuser-OxOldst", 200*time.Hour)
	agedDraft(t, ledgerPath, "2026-01-01T00-00-testuser-OxMiddl", 100*time.Hour)

	orphans, err := findOrphanedDrafts(projectRoot, ledgerPath)
	require.NoError(t, err)
	require.Len(t, orphans, 3)
	assert.Equal(t, "2026-01-01T00-00-testuser-OxOldst", orphans[0], "oldest first")
	assert.Equal(t, "2026-01-01T00-00-testuser-OxNewer", orphans[2])
}

// TestFindOrphanedDrafts_MissingSessionsDirIsNotAnError — a ledger that has
// never held a session must report cleanly rather than surfacing an error the
// user cannot act on.
func TestFindOrphanedDrafts_MissingSessionsDirIsNotAnError(t *testing.T) {
	setTestCfg(t)
	projectRoot, ledgerPath := draftReaperFixture(t)

	orphans, err := findOrphanedDrafts(projectRoot, ledgerPath)
	require.NoError(t, err)
	assert.Empty(t, orphans)
}

// TestCheckSessionDraftOrphan_ReportsWithoutMutating.
//
// Without --fix the check must be strictly read-only. Asserting only on the
// returned status would be theater: a check that deleted the draft and returned
// Warning passes that. So this fingerprints the tree and requires it unchanged.
func TestCheckSessionDraftOrphan_ReportsWithoutMutating(t *testing.T) {
	setTestCfg(t)
	projectRoot, ledgerPath := setupLedgerProject(t)
	t.Chdir(projectRoot)

	agedDraft(t, ledgerPath, "2026-01-01T00-00-testuser-OxRept", 120*time.Hour)
	before := treeHash(t, ledgerPath)

	res := checkSessionDraftOrphan(false)
	// WarningCheck in this codebase sets passed AND warning — assert on the
	// warning flag, which is the one that reaches the user.
	assert.True(t, res.warning, "a stale orphan must be reported as a warning")
	assert.False(t, res.skipped)
	assert.Contains(t, res.message, "1 draft placeholder")

	assert.Equal(t, before, treeHash(t, ledgerPath),
		"the reporting pass must not touch the ledger at all")
}

// TestCheckSessionDraftOrphan_PassesWhenNothingToReap is the negative control
// for the check itself: without it, a check hardcoded to Warning would satisfy
// the test above.
func TestCheckSessionDraftOrphan_PassesWhenNothingToReap(t *testing.T) {
	setTestCfg(t)
	projectRoot, ledgerPath := setupLedgerProject(t)
	t.Chdir(projectRoot)

	agedDraft(t, ledgerPath, "2026-01-01T00-00-testuser-OxLive1", 1*time.Minute)
	finalizedLedgerSession(t, ledgerPath, "2026-01-01T00-00-testuser-OxDone1")

	res := checkSessionDraftOrphan(false)
	assert.False(t, res.warning, "a live draft and a finalized session are not orphans")
	assert.True(t, res.passed)
	assert.Equal(t, "no orphaned drafts", res.message)
}

// TestCheckSessionDraftOrphan_FixRemovesFromRemote drives the real fix against
// a real bare remote, and asserts through a FRESH clone — the local worktree is
// not what teammates see, and a stale /c/ page is exactly the symptom being
// fixed.
func TestCheckSessionDraftOrphan_FixRemovesFromRemote(t *testing.T) {
	setTestCfg(t)
	f := newDraftLedgerFixture(t)
	t.Chdir(f.projectRoot)

	const orphan = "2026-01-01T00-00-testuser-OxReap"
	const live = "2026-01-01T00-00-testuser-OxKeep"

	f.publish(t, orphan, 2)
	f.publish(t, live, 2)
	// Age only the orphan. The live draft keeps its fresh updated_at.
	agedDraft(t, f.ledgerPath, orphan, 120*time.Hour)
	runGit(t, f.ledgerPath, "add", "--sparse", "--", "sessions/"+orphan+"/meta.json")
	runGit(t, f.ledgerPath, "commit", "--no-verify", "-m", "session-draft: age "+orphan)
	f.push(t)
	require.Contains(t, remoteTree(t, f.barePath), "sessions/"+orphan+"/meta.json")

	res := checkSessionDraftOrphan(true)
	require.False(t, res.warning, "the fix should succeed: %s %s", res.message, res.detail)
	assert.Contains(t, res.message, "retracted 1")

	tree := remoteTree(t, f.barePath)
	assert.NotContains(t, tree, "sessions/"+orphan+"/meta.json",
		"the orphan must be gone from the REMOTE, not just locally")
	assert.Contains(t, tree, "sessions/"+live+"/meta.json",
		"a live session's placeholder must survive the reaper")
	gitFsckClean(t, f.barePath)
}

// TestHasLocalSessionData covers the helper's own decision, including the
// XDG-vs-ledger-cache split that the table above exercises only indirectly.
func TestHasLocalSessionData(t *testing.T) {
	base := t.TempDir()
	dirA := filepath.Join(base, "a")
	dirB := filepath.Join(base, "b")
	const name = "2026-01-01T00-00-testuser-OxHas01"

	assert.False(t, hasLocalSessionData([]string{dirA, dirB}, name),
		"no cache location holds it")

	require.NoError(t, os.MkdirAll(filepath.Join(dirB, name), 0755))
	assert.False(t, hasLocalSessionData([]string{dirA, dirB}, name),
		"an EMPTY directory is not session data — a bare mkdir must not block reaping")

	require.NoError(t, os.WriteFile(filepath.Join(dirB, name, "raw.jsonl"), []byte("x"), 0644))
	assert.True(t, hasLocalSessionData([]string{dirA, dirB}, name),
		"a transcript in the SECOND location must be found")
}

// TestOrphanedDraftAge_IsLongerThanTheRefreshCadence.
//
// The reaper's safety rests entirely on drafts refreshing more often than the
// threshold. If the refresh cadence were ever raised past orphanedDraftAge, the
// reaper would start deleting live sessions' placeholders — a silent, gradual
// failure with no other signal. This pins the relationship rather than the
// numbers.
func TestOrphanedDraftAge_IsLongerThanTheRefreshCadence(t *testing.T) {
	assert.Positive(t, config.DraftRefreshEveryTurns,
		"a zero refresh cadence means no heartbeat, which makes the reaper unsafe")
	assert.GreaterOrEqual(t, orphanedDraftAge, 12*time.Hour,
		"the reap threshold must stay far above any plausible gap between refreshes")
}
