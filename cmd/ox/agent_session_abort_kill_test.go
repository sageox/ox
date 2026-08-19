package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is the behavior spec for the "total kill" contract of
// `ox agent <id> session abort`: aborting a session removes it and ALL the
// summarized data around it, in EVERY state it can reach — a draft placeholder
// committed after N turns, and a fully finalized/persisted session in the
// ledger. The one thing a kill must never do is destroy a teammate's finalized
// session that a partial name merely collided with.
//
// The mirror capability doc is tests/acceptance/features/session-recording/session-abort-kill.feature.

// seedFinalizedLedgerSessionWithArtifacts writes a finalized (non-draft) session
// into the ledger with the FULL set of summarized artifacts a real finalized
// session carries — the exact data the kill must leave nothing of. It does not
// commit; the caller stages + commits + pushes so the assertion runs against a
// real remote.
func seedFinalizedLedgerSessionWithArtifacts(t *testing.T, ledgerPath, sessionName string) string {
	t.Helper()
	dir := filepath.Join(ledgerPath, "sessions", sessionName)
	require.NoError(t, os.MkdirAll(dir, 0755))
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, &lfs.SessionMeta{
		SessionName: sessionName, SessionID: sessionScopedID(sessionName),
		CreatedAt: time.Now(), Title: "real work", EntryCount: 40,
		Files: map[string]lfs.FileRef{"raw.jsonl": {OID: "sha256:abc", Size: 10}},
	}))
	// The summarized data around a finalized session. raw.jsonl is the LFS
	// pointer that is git-tracked in place (the content lives in LFS).
	pointer := "version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize 10\n"
	for name, body := range map[string]string{
		"summary.md":          "REAL SUMMARY MARKDOWN",
		"summary.json":        `{"title":"real work","key_actions":["shipped kill"]}`,
		"session.md":          "full transcript rendered to markdown",
		"context-trace.jsonl": `{"consulted":"team-ctx"}`,
		"raw.jsonl":           pointer,
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0644))
	}
	require.NoError(t, ensureSessionsGitignore(filepath.Join(ledgerPath, "sessions")))
	return dir
}

// commitAndPushFinalized stages, commits, and pushes a finalized session dir the
// way a real finalize would, so remoteTree() sees it on the bare remote.
func commitAndPushFinalized(t *testing.T, f *draftLedgerFixture, sessionName string) {
	t.Helper()
	// Stage .gitignore alongside the session, exactly as a real finalize does —
	// otherwise it lingers uncommitted and a post-kill `status` reads it as a
	// dangling change that has nothing to do with the deletion.
	runGit(t, f.ledgerPath, "add", "--sparse", "--", "sessions/"+sessionName, "sessions/.gitignore")
	runGit(t, f.ledgerPath, "commit", "--no-verify", "-m", "session: "+sessionName)
	f.push(t)
	require.Contains(t, remoteTree(t, f.barePath), "sessions/"+sessionName+"/meta.json",
		"fixture precondition: the finalized session must be on the remote before the kill")
}

// remotePathsUnder returns the tracked remote paths beneath sessions/<name>/.
func remotePathsUnder(t *testing.T, barePath, sessionName string) []string {
	t.Helper()
	prefix := "sessions/" + sessionName + "/"
	var out []string
	for _, p := range remoteTree(t, barePath) {
		if strings.HasPrefix(p, prefix) {
			out = append(out, p)
		}
	}
	return out
}

// TestAbort_KillsCommittedDraftAndAllLocalData is the N-turn example, end to end.
//
// A session committed after N turns is an ADR-029 draft placeholder pushed to
// the ledger. Aborting the live recording must remove that committed placeholder
// from the REMOTE (not just locally), delete the local recording folder and
// every summary artifact in it, and clear the recording marker — in one command.
//
// This drives the real runAgentSessionAbort handler, not deleteDraftFromLedger
// in isolation, so it proves the whole active-abort wiring, not one helper.
func TestAbort_KillsCommittedDraftAndAllLocalData(t *testing.T) {
	f := newDraftLedgerFixture(t)
	// abort's active path resolves the project from cwd; the fixture chdir'd into
	// the ledger clone for push-credential isolation, so point cwd back.
	t.Chdir(f.projectRoot)
	cfg = &config.Config{}

	// Given: a live recording whose draft placeholder crossed the publish
	// threshold and was committed + pushed, plus local summary artifacts.
	state, err := session.StartRecording(f.projectRoot, session.StartRecordingOptions{
		AgentID: "OxKill", AdapterName: "test",
	})
	require.NoError(t, err)
	sessionName := session.GetSessionName(state.SessionPath)

	localArtifacts := []string{"raw.jsonl", "summary.md", "summary.json", "session.md"}
	for _, name := range localArtifacts {
		require.NoError(t, os.WriteFile(filepath.Join(state.SessionPath, name),
			[]byte("local "+name), 0644))
	}

	f.publish(t, sessionName, config.DraftPublishTurn)
	f.push(t)
	require.Contains(t, remoteTree(t, f.barePath), "sessions/"+sessionName+"/meta.json")

	// When: the agent aborts its own recording.
	setForceFlag(t, true)
	var buf bytes.Buffer
	agentCmd.SetOut(&buf)
	t.Cleanup(func() { agentCmd.SetOut(nil) })
	inst := &agentinstance.Instance{AgentID: "OxKill"}
	require.NoError(t, runAgentSessionAbort(inst, agentCmd, nil))

	// Then: the committed draft is gone from the remote and the worktree.
	assert.NotContains(t, remoteTree(t, f.barePath), "sessions/"+sessionName+"/meta.json",
		"the committed draft must be removed from the remote, not just locally")
	assert.NoDirExists(t, draftLedgerSessionDir(f.ledgerPath, sessionName))

	// And: the local recording folder and every summary artifact are gone.
	assert.NoDirExists(t, state.SessionPath)

	// And: the recording marker is cleared, so a future session start works.
	st, err := session.LoadRecordingStateForAgent(f.projectRoot, "OxKill")
	require.NoError(t, err)
	assert.Nil(t, st, ".recording.json must be cleared after abort")

	// And: no repo corruption, no dangling staged deletion.
	gitFsckClean(t, f.barePath)
	assert.Empty(t, runGit(t, f.ledgerPath, "status", "--porcelain", "--", "sessions/"),
		"no staged deletion may be left dangling")

	// And: the output reports the ledger draft was removed.
	var out sessionAbortOutput
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))
	assert.True(t, out.Success)
	assert.True(t, out.LedgerDraftDeleted, "abort must report it removed the published draft")
}

// TestAbort_KillsFinalizedSessionAndAllSummarizedData is the intent test.
//
// The user's contract: abort is a KILL — it deletes a finalized/persisted
// session too, along with ALL of its summarized data (summary.md, summary.json,
// session.md, context-trace.jsonl, the raw.jsonl pointer), from the remote, from
// the local cache, and from the ledger hydration cache.
//
// Red-first check: revert the StatusUploaded branch in
// runAgentSessionAbortByName to `return fmt.Errorf(... already uploaded ...)` and
// this test fails — abort refuses and the finalized dir survives on the remote.
func TestAbort_KillsFinalizedSessionAndAllSummarizedData(t *testing.T) {
	f := newDraftLedgerFixture(t)
	t.Chdir(f.projectRoot)
	cfg = &config.Config{}

	const sessionName = "2026-01-01T00-00-testuser-OxFinl"

	// Given: a finalized session with the full artifact set, committed + pushed.
	seedFinalizedLedgerSessionWithArtifacts(t, f.ledgerPath, sessionName)
	commitAndPushFinalized(t, f, sessionName)

	// And: a local recording-cache copy and a ledger hydration-cache copy.
	repoID := getRepoIDOrDefault(f.projectRoot)
	localDir := filepath.Join(session.GetContextPath(repoID), "sessions", sessionName)
	require.NoError(t, os.MkdirAll(localDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(localDir, "raw.jsonl"), []byte("local raw"), 0644))

	hydrationDir := filepath.Join(f.ledgerPath, ".sageox", "cache", "sessions", sessionName)
	require.NoError(t, os.MkdirAll(hydrationDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(hydrationDir, "raw.jsonl"), []byte("hydrated bytes"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(hydrationDir, "summary.md"), []byte("hydrated summary"), 0644))

	// When: the agent aborts it by its EXACT name.
	setForceFlag(t, true)
	inst := &agentinstance.Instance{AgentID: "OxKill"}
	require.NoError(t, runAgentSessionAbort(inst, agentCmd, []string{sessionName}))

	// Then: nothing under sessions/<name>/ survives on the remote.
	assert.Empty(t, remotePathsUnder(t, f.barePath, sessionName),
		"every summarized artifact of a finalized session must be gone from the remote after a kill")
	assert.NoDirExists(t, filepath.Join(f.ledgerPath, "sessions", sessionName),
		"the finalized session must be gone from the ledger worktree")

	// And: both caches are gone.
	assert.NoDirExists(t, localDir, "the local recording cache copy must be removed")
	assert.NoDirExists(t, hydrationDir, "the ledger hydration cache (summarized bytes) must be removed")

	// And: no corruption, no dangling staged deletion.
	gitFsckClean(t, f.barePath)
	assert.Empty(t, runGit(t, f.ledgerPath, "status", "--porcelain", "--", "sessions/"),
		"no staged deletion may be left dangling after the kill")
}

// TestAbort_PartialNameNeverKillsTeammateFinalizedSession is the safety control
// that must stay green before AND after the kill lands.
//
// Abort resolves by partial (agent-id suffix) name. A partial that collides with
// a teammate's finalized session pulled from the shared ledger must NEVER become
// a deletion of their work — a finalized kill requires the exact name. Without
// this guard the kill would multiply into data loss across the team.
func TestAbort_PartialNameNeverKillsTeammateFinalizedSession(t *testing.T) {
	f := newDraftLedgerFixture(t)
	t.Chdir(f.projectRoot)
	cfg = &config.Config{}

	// Given: a teammate's finalized session on the shared ledger.
	const teammate = "2026-01-01T00-00-teammate-OxTeam"
	seedFinalizedLedgerSessionWithArtifacts(t, f.ledgerPath, teammate)
	commitAndPushFinalized(t, f, teammate)

	// When: an agent aborts using only the partial (agent-id suffix) name.
	setForceFlag(t, true)
	inst := &agentinstance.Instance{AgentID: "OxMe"}
	err := runAgentSessionAbort(inst, agentCmd, []string{"OxTeam"})

	// Then: abort refuses, and the teammate's session survives everywhere.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exact name",
		"a partial-name finalized abort must be refused and demand the exact name")
	assert.Contains(t, remoteTree(t, f.barePath), "sessions/"+teammate+"/meta.json",
		"a teammate's finalized session must survive a colliding partial abort")
	assert.DirExists(t, filepath.Join(f.ledgerPath, "sessions", teammate))
	gitFsckClean(t, f.barePath)
}

// TestAbort_KillsCommittedThenFinalizedSessionAndAllSummarizedData is the union
// lifecycle: the session the customer actually aborts has already traveled the
// full road — committed to the Ledger BECAUSE N turns passed (an ADR-029 draft
// placeholder), THEN finalized with its whole summarized artifact set
// superseding that draft. Aborting it by exact name must leave nothing behind:
// not the draft commit's tree, not the finalized summary/transcript/trace, not
// the local or hydrated caches.
//
// This is the customer promise the two sibling tests only cover in halves —
// TestAbort_KillsCommittedDraftAndAllLocalData drives the N-turn draft but a
// draft carries no summarized data, and TestAbort_KillsFinalizedSessionAndAll-
// SummarizedData carries the full summary set but seeds it directly, never
// through the draft->finalize supersession. Failure prevented: a kill that
// handles a freshly-finalized session but mishandles one whose git history
// includes the earlier draft-placeholder commit — leaving an orphaned artifact
// on the remote or a dangling staged deletion.
//
// Red-first check: revert the StatusUploaded branch in runAgentSessionAbortByName
// to `return fmt.Errorf(... already uploaded ...)` and this test fails — abort
// refuses and the finalized dir survives on the remote.
func TestAbort_KillsCommittedThenFinalizedSessionAndAllSummarizedData(t *testing.T) {
	f := newDraftLedgerFixture(t)
	t.Chdir(f.projectRoot)
	cfg = &config.Config{}

	const sessionName = "2026-01-01T00-00-testuser-OxUnin"

	// Given: the session was committed to the Ledger after N turns as a draft
	// placeholder (the "committed because N turns passed" beat), pushed to the
	// remote so /c/<id> resolves mid-recording.
	f.publish(t, sessionName, config.DraftPublishTurn)
	f.push(t)
	require.Contains(t, remoteTree(t, f.barePath), "sessions/"+sessionName+"/meta.json",
		"precondition: the N-turn draft placeholder must be committed to the remote")

	// And: it was then finalized — the full summarized artifact set supersedes
	// the draft in the same session dir, committed on top and pushed. Git history
	// now carries BOTH the draft commit and the finalize commit.
	seedFinalizedLedgerSessionWithArtifacts(t, f.ledgerPath, sessionName)
	commitAndPushFinalized(t, f, sessionName)

	// And: an unrelated peer session also lives in the Ledger — the kill must
	// leave it untouched (a "delete everything" regression would take it too).
	const peerName = "2026-01-01T00-00-peer-OxPeer"
	seedFinalizedLedgerSessionWithArtifacts(t, f.ledgerPath, peerName)
	commitAndPushFinalized(t, f, peerName)

	// And: a local recording-cache copy and a ledger hydration-cache copy exist.
	repoID := getRepoIDOrDefault(f.projectRoot)
	localDir := filepath.Join(session.GetContextPath(repoID), "sessions", sessionName)
	require.NoError(t, os.MkdirAll(localDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(localDir, "raw.jsonl"), []byte("local raw"), 0644))

	hydrationDir := filepath.Join(f.ledgerPath, ".sageox", "cache", "sessions", sessionName)
	require.NoError(t, os.MkdirAll(hydrationDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(hydrationDir, "raw.jsonl"), []byte("hydrated bytes"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(hydrationDir, "summary.md"), []byte("hydrated summary"), 0644))

	// When: the agent aborts it by its EXACT name.
	setForceFlag(t, true)
	inst := &agentinstance.Instance{AgentID: "OxKill"}
	require.NoError(t, runAgentSessionAbort(inst, agentCmd, []string{sessionName}))

	// Then: nothing under sessions/<name>/ survives on the remote — neither the
	// N-turn draft nor the finalized summarized artifacts.
	assert.Empty(t, remotePathsUnder(t, f.barePath, sessionName),
		"a committed-then-finalized session must leave nothing on the remote after a kill")
	assert.NoDirExists(t, filepath.Join(f.ledgerPath, "sessions", sessionName),
		"the session must be gone from the ledger worktree")

	// And: both caches are gone.
	assert.NoDirExists(t, localDir, "the local recording cache copy must be removed")
	assert.NoDirExists(t, hydrationDir, "the ledger hydration cache (summarized bytes) must be removed")

	// And: the Ledger history stays intact for everyone else, no dangling deletion.
	gitFsckClean(t, f.barePath)
	assert.Empty(t, runGit(t, f.ledgerPath, "status", "--porcelain", "--", "sessions/"),
		"no staged deletion may be left dangling after the kill")

	// And: the unrelated peer session is untouched — on the remote and in the worktree.
	assert.Contains(t, remoteTree(t, f.barePath), "sessions/"+peerName+"/meta.json",
		"aborting one session must never delete a peer session")
	assert.DirExists(t, filepath.Join(f.ledgerPath, "sessions", peerName))
}
