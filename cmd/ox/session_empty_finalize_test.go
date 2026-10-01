package main

// Customer promise under test: session-recording/auto-record.feature, Rule
// "A session that captured no conversation is settled once". An empty
// recording that reached the Ledger before v0.19.0 is finalized once as a
// "Brief session" and then left alone, instead of being retried forever.
//
// Failure prevented (GH #1106): the daily re-arm matched the failed summary,
// downloaded the empty transcript and cleared the ledger meta.json on every
// machine, uncommitted. Since v0.19.0 the finalize scan skipped the empty
// download, so nothing ever settled the session, and the cleared meta.json
// was left as an uncommitted edit in a shared file (#1105).
//
// The proof drives the real daemon autofix (the re-arm) against a fake
// content store, then the real finalize handler against a real bare remote,
// and asserts what a teammate would see: the session's meta.json on the
// remote, and a clean ledger clone.
//
// Red-first (verified while authoring):
//   - on main before the fix → fails on "the empty session must be settled on
//     the remote" (title empty, status still unrecoverable), 0 items queued,
//     and the clone left holding "M sessions/<name>/meta.json";
//   - route the result through quality scoring → the discard branch deletes
//     the download and the same assertion fails;
//   - start meta.json from blank for a download → repo_id, user_id and model
//     are erased on the remote.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/doctor/autofix"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// publishEmptyFailedSession puts an empty recording on the remote the way
// ox 0.17/0.18 left it: a header-and-footer transcript in the content store,
// stub markdown, an untitled summary.json, and a meta.json whose summary
// failed with "title too short".
func (f *downloadLedgerFixture) publishEmptyFailedSession(t *testing.T, name string, stoppedAt time.Time) {
	t.Helper()
	sessionDir := filepath.Join(f.ledgerPath, "sessions", name)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	// the header and footer bytes the real recorder and stop door write
	scratch := filepath.Join(t.TempDir(), "raw.jsonl")
	require.NoError(t, os.WriteFile(scratch, []byte(`{"type":"header","metadata":{"agent_id":"OxRiLy","username":"riley","agent_type":"claude-code"}}`+"\n"), 0o600))
	require.NoError(t, session.StampRawCarrier(scratch, session.CarrierStamp{StoppedAt: stoppedAt}))
	raw, err := os.ReadFile(scratch)
	require.NoError(t, err)
	require.Equal(t, session.RawHeaderOnly, session.ClassifyRawFile(scratch), "precondition: the transcript must hold no conversation")

	content := map[string][]byte{
		"raw.jsonl":  raw,
		"summary.md": []byte("# Session\n"),
		"session.md": []byte("# Session\n"),
	}
	refs := map[string]lfs.FileRef{}
	f.mu.Lock()
	for filename, data := range content {
		ref := lfs.NewFileRef(data)
		f.blobs[ref.BareOID()] = data
		refs[filename] = ref
	}
	f.mu.Unlock()
	for filename, ref := range refs {
		require.NoError(t, lfs.WritePointerFile(filepath.Join(sessionDir, filename), lfs.AssertUploaded(ref)))
	}

	require.NoError(t, lfs.WriteSessionMetaOnly(sessionDir, &lfs.SessionMeta{
		Version: "1.0", SessionName: name, SessionID: sessionScopedID(name),
		Username: "riley", UserID: "usr_riley", RepoID: "repo_empty_test",
		AgentID: "OxRiLy", AgentType: "claude-code", Model: "claude-opus",
		CreatedAt:  stoppedAt.Add(-time.Minute),
		StoppedAt:  &stoppedAt,
		StopReason: "recovered",
		Files:      refs,

		SummaryStatus:   "unrecoverable",
		ValidationError: "content validation failed: title too short (0 chars, minimum 3)",
		SummaryAttempts: 3,
	}))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "summary.json"), []byte(`{"title":""}`), 0o644))

	runGit(t, f.ledgerPath, "add", "--sparse", "--", "sessions/"+name)
	runGit(t, f.ledgerPath, "commit", "--no-verify", "-m", "riley: finalize "+name)
	runGit(t, f.ledgerPath, "push", "origin", "HEAD")
}

// runDaemonAutofix runs every daemon autofix check once, as the daemon does
// on start and on every `ox doctor`, and returns the re-arm's result.
func runDaemonAutofix(t *testing.T, projectRoot string) autofix.CheckResult {
	t.Helper()
	s := autofix.NewScheduler(autofix.Default(), nil, func() []string { return []string{projectRoot} }, nil)
	for _, r := range s.RunNow(context.Background()) {
		if r.Slug == "session-inline-summary-retry" {
			return r
		}
	}
	t.Fatal("the re-arm check did not run")
	return autofix.CheckResult{}
}

func remoteSessionMeta(t *testing.T, f *downloadLedgerFixture, name string) lfs.SessionMeta {
	t.Helper()
	var meta lfs.SessionMeta
	require.NoError(t, json.Unmarshal([]byte(runGit(t, f.barePath, "show", "HEAD:sessions/"+name+"/meta.json")), &meta))
	return meta
}

// moveLedgerToDefaultPath moves the fixture's clone to where the daemon and
// its autofix checks look for the ledger (they ignore the configured path).
func moveLedgerToDefaultPath(t *testing.T, f *downloadLedgerFixture) {
	t.Helper()
	pctx, err := config.LoadProjectContext(f.projectRoot)
	require.NoError(t, err)
	target := pctx.DefaultLedgerPath()
	require.NotEmpty(t, target)
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
	require.NoError(t, os.Rename(f.ledgerPath, target))
	f.ledgerPath = target
	require.NoError(t, config.SaveLocalConfig(f.projectRoot, &config.LocalConfig{
		Ledger: &config.LedgerConfig{Path: target},
	}))
}

func TestEmptyLedgerSession_SettledOnceWithoutLLM(t *testing.T) {
	f := newDownloadLedgerFixture(t)
	moveLedgerToDefaultPath(t, f)

	const name = "2026-09-24T23-03-riley-OxRiLy"
	stoppedAt := time.Date(2026, 9, 24, 23, 4, 0, 0, time.UTC)
	f.publishEmptyFailedSession(t, name, stoppedAt)
	before := remoteSessionMeta(t, f, name)
	rawPointer := runGit(t, f.barePath, "show", "HEAD:sessions/"+name+"/raw.jsonl")
	remoteBefore := runGit(t, f.barePath, "rev-parse", "HEAD")

	// When: the daemon's re-arm runs on Riley's machine.
	rearm := runDaemonAutofix(t, f.projectRoot)
	require.Equal(t, autofix.StatusFixed, rearm.Status, "precondition: the re-arm must pick up the failed summary: %s", rearm.Summary)
	cacheDir := filepath.Join(f.ledgerPath, ".sageox", "cache", "sessions", name)
	require.FileExists(t, filepath.Join(cacheDir, "raw.jsonl"), "precondition: the re-arm must download the transcript")

	// And the daemon's finalize scan runs.
	queued, llmRuns := runFinalizePass(t, f.projectRoot, f.ledgerPath)

	// Then: the Ledger shows the session as a settled brief session.
	after := remoteSessionMeta(t, f, name)
	assert.Equal(t, "Brief session", after.Title, "the empty session must be settled on the remote")
	assert.Equal(t, "ok", after.SummaryStatus)
	assert.Empty(t, after.ValidationError)
	assert.Zero(t, after.SummaryAttempts)
	assert.Zero(t, llmRuns, "an empty transcript must never reach the LLM")
	assert.Equal(t, 1, queued)

	// And everything else about the session is unchanged.
	assert.Equal(t, before.RepoID, after.RepoID)
	assert.Equal(t, before.UserID, after.UserID)
	assert.Equal(t, before.Model, after.Model)
	assert.Equal(t, before.SessionID, after.SessionID)
	assert.Equal(t, before.StopReason, after.StopReason)
	require.NotNil(t, after.StoppedAt)
	assert.True(t, before.StoppedAt.Equal(*after.StoppedAt))
	assert.Equal(t, before.Files["raw.jsonl"], after.Files["raw.jsonl"])
	assert.Equal(t, rawPointer, runGit(t, f.barePath, "show", "HEAD:sessions/"+name+"/raw.jsonl"),
		"the transcript pointer must not change")

	// And one commit, touching only this session, reached the remote.
	assert.Equal(t, "1", runGit(t, f.barePath, "rev-list", "--count", remoteBefore+"..HEAD"))
	for _, path := range strings.Fields(runGit(t, f.barePath, "diff", "--name-only", remoteBefore, "HEAD")) {
		assert.True(t, strings.HasPrefix(path, "sessions/"+name+"/"), "unexpected path in the commit: %s", path)
	}

	// And Riley's clone is left clean: no uncommitted edit to the shared file.
	assert.Empty(t, runGit(t, f.ledgerPath, "status", "--porcelain", "--", "sessions/"+name))
	assert.NoDirExists(t, cacheDir)

	// When: the re-arm and the scan run again.
	settled := runGit(t, f.barePath, "rev-parse", "HEAD")
	assert.Equal(t, autofix.StatusClean, runDaemonAutofix(t, f.projectRoot).Status, "the re-arm must leave a settled session alone")
	queued, llmRuns = runFinalizePass(t, f.projectRoot, f.ledgerPath)

	// Then: nothing touches the session again.
	assert.Zero(t, queued)
	assert.Zero(t, llmRuns)
	assert.Equal(t, settled, runGit(t, f.barePath, "rev-parse", "HEAD"))
	assert.Empty(t, runGit(t, f.ledgerPath, "status", "--porcelain", "--", "sessions/"+name))
}

// TestEmptyLedgerSession_SettledAfterFailedPush: the first attempt settles the
// session locally but cannot reach the remote, which clears the re-arm's
// request. The next scan must still publish it and remove the download.
// Failure prevented: the session stays unsettled on the team's Ledger and the
// download lingers on this machine forever.
func TestEmptyLedgerSession_SettledAfterFailedPush(t *testing.T) {
	f := newDownloadLedgerFixture(t)
	moveLedgerToDefaultPath(t, f)

	const name = "2026-09-24T23-03-riley-OxRiLy"
	f.publishEmptyFailedSession(t, name, time.Date(2026, 9, 24, 23, 4, 0, 0, time.UTC))
	remoteBefore := runGit(t, f.barePath, "rev-parse", "HEAD")
	require.Equal(t, autofix.StatusFixed, runDaemonAutofix(t, f.projectRoot).Status, "precondition: the re-arm must pick up the failed summary")
	cacheDir := filepath.Join(f.ledgerPath, ".sageox", "cache", "sessions", name)

	// When: the first attempt cannot reach the remote.
	runGit(t, f.ledgerPath, "remote", "set-url", "--push", "origin", filepath.Join(t.TempDir(), "unreachable.git"))
	_, llmRuns := runFinalizePass(t, f.projectRoot, f.ledgerPath)
	require.Zero(t, llmRuns)
	require.Equal(t, remoteBefore, runGit(t, f.barePath, "rev-parse", "HEAD"), "precondition: the push must fail")
	require.DirExists(t, cacheDir, "precondition: the download is kept for a retry")

	// And the remote is reachable on the next scan.
	runGit(t, f.ledgerPath, "remote", "set-url", "--push", "origin", f.barePath)
	_, llmRuns = runFinalizePass(t, f.projectRoot, f.ledgerPath)

	// Then: the session reaches the Ledger settled, and nothing is left behind.
	after := remoteSessionMeta(t, f, name)
	assert.Equal(t, "Brief session", after.Title, "the settled session must reach the remote on the retry")
	assert.Equal(t, "ok", after.SummaryStatus)
	assert.Zero(t, llmRuns, "an empty transcript must never reach the LLM")
	assert.NoDirExists(t, cacheDir)
	assert.Empty(t, runGit(t, f.ledgerPath, "status", "--porcelain", "--", "sessions/"+name))
}
