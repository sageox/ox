package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/nativeimport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Rule: A session whose recording the coworker aborted is never imported

// abortedPrompt is what the coworker discarded. It must never reach the
// summarizer or the LFS store.
const abortedPrompt = "Draft the acquisition memo for the board covering the deal terms and the price we offered, and do not record this session"

var abortStart = time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)

// newAbortFixture is a coworker's machine with one native session about to be
// aborted (e2eClaudeA) and one unrelated session that is ready (e2eCodexA),
// the control that proves the import still works.
func newAbortFixture(t *testing.T) (*importFixture, string) {
	t.Helper()
	f := newImportFixture(t)
	t.Chdir(f.projectRoot) // abort resolves the project and Ledger from cwd
	path := f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: abortStart, prompt: abortedPrompt,
		reply: "Here is a first outline of the memo."})
	f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexA, start: abortStart.Add(time.Hour), prompt: pushPrompt,
		reply: "A stale pointer references an object that was never uploaded; re-uploading it lets the push through."})
	return f, path
}

// abortAs runs the real abort command and requires a clean abort: a warning
// would mean the abort was not recorded for the import.
func abortAs(t *testing.T, agentID string, args ...string) {
	t.Helper()
	setForceFlag(t, true)
	var out bytes.Buffer
	agentCmd.SetOut(&out)
	t.Cleanup(func() { agentCmd.SetOut(nil) })
	require.NoError(t, runAgentSessionAbort(&agentinstance.Instance{AgentID: agentID}, agentCmd, args), out.String())
	var result sessionAbortOutput
	require.NoError(t, json.Unmarshal(out.Bytes(), &result), out.String())
	require.True(t, result.Success)
	require.Empty(t, result.Warning)
}

// Devon aborts an ox recording of a session, in each state abort reaches, and
// later runs `ox session import`: the session is listed as aborted, not
// ready, and neither the default selection, --yes, nor naming it with
// --session uploads it. The unrelated session still imports.
//
// Failure prevented: abort deletes everything keyed by the recording, so once
// the native transcript goes quiet the import lists the discarded session as
// ready and pre-selected, and a coworker who confirms the picker publishes it,
// possibly to a public Ledger.
func TestImportE2E_AbortedRecordingIsNeverImported(t *testing.T) {
	cases := []struct {
		name string
		// record makes ox hold the session and returns the import state and
		// covering session the preview shows before the abort.
		record func(t *testing.T, f *importFixture, nativePath string) (string, string)
		abort  func(t *testing.T, recording string)
	}{
		{
			name: "live recording aborted by its own AI coworker",
			record: func(t *testing.T, f *importFixture, nativePath string) (string, string) {
				state, err := session.StartRecording(f.projectRoot, session.StartRecordingOptions{
					AgentID: "OxAbrt", AdapterName: "claude-code", Username: "devon",
					AgentSessionID: e2eClaudeA, AgentSessionSource: "startup", SessionFile: nativePath,
				})
				require.NoError(t, err)
				return string(stateInProgress), filepath.Base(state.SessionPath)
			},
			abort: func(t *testing.T, _ string) { abortAs(t, "OxAbrt") },
		},
		{
			// Recognized only by the link prime wrote into the transcript.
			name: "live recording that never stored its native session ID",
			record: func(t *testing.T, f *importFixture, nativePath string) (string, string) {
				state, err := session.StartRecording(f.projectRoot, session.StartRecordingOptions{
					AgentID: "OxMark", AdapterName: "claude-code", Username: "devon", SessionFile: nativePath,
				})
				require.NoError(t, err)
				require.NotEmpty(t, state.SessionID)
				f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: abortStart, prompt: abortedPrompt,
					reply: "Here is a first outline of the memo.",
					hook:  `<session-context agent_id="OxMark" recording="true" url="https://sageox.ai/c/` + state.SessionID + `">`})
				return string(stateInProgress), filepath.Base(state.SessionPath)
			},
			abort: func(t *testing.T, _ string) { abortAs(t, "OxMark") },
		},
		{
			// Only the published placeholder is left; the transcript links it.
			name: "draft placeholder whose local recording is gone",
			record: func(t *testing.T, f *importFixture, _ string) (string, string) {
				name := "2026-10-19T09-00-devon-OxDraft"
				f.publish(t, name, config.DraftPublishTurn)
				f.draftLedgerFixture.push(t)
				f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: abortStart, prompt: abortedPrompt,
					reply: "Here is a first outline of the memo.",
					hook:  `<session-context agent_id="OxDrft" recording="true" url="https://sageox.ai/c/` + draftTestSessionID + `">`})
				return string(stateRecordedLive), name
			},
			abort: func(t *testing.T, recording string) { abortAs(t, "OxOthr", recording) },
		},
		{
			name: "stopped capture discarded by name",
			record: func(t *testing.T, f *importFixture, _ string) (string, string) {
				name := "2026-10-19T09-00-devon-OxCapt"
				dir := filepath.Join(session.GetContextPath(e2eRepoID), "sessions", name)
				require.NoError(t, os.MkdirAll(dir, 0o755))
				header := `{"type":"header","metadata":{"version":"1","agent_type":"claude-code","created_at":"2026-10-19T09:00:00Z",` +
					`"native_sessions":[{"id":"` + e2eClaudeA + `","source":"startup","first_seen":"2026-10-19T09:00:00Z","last_seen":"2026-10-19T09:01:00Z"}]}}`
				require.NoError(t, os.WriteFile(filepath.Join(dir, ledgerFileRaw),
					[]byte(header+"\n"+`{"type":"user","content":"`+abortedPrompt+`","seq":1}`+"\n"), 0o600))
				return string(stateRecordedLive), name
			},
			abort: func(t *testing.T, recording string) { abortAs(t, "OxOthr", recording) },
		},
		{
			name: "finalized session killed by its exact name",
			record: func(t *testing.T, f *importFixture, _ string) (string, string) {
				name := "2026-10-19T09-00-devon-OxFinl"
				dir := filepath.Join(f.ledgerPath, "sessions", name)
				require.NoError(t, os.MkdirAll(dir, 0o755))
				seen := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
				require.NoError(t, lfs.WriteSessionMetaOnly(dir, &lfs.SessionMeta{
					SessionName: name, SessionID: sessionScopedID(name), AgentType: "claude-code",
					CreatedAt: seen, Title: "Acquisition memo", EntryCount: 2,
					Files:          map[string]lfs.FileRef{ledgerFileRaw: {OID: "sha256:abc", Size: 10}},
					NativeSessions: []lfs.NativeSession{{ID: e2eClaudeA, Source: "startup", FirstSeen: seen, LastSeen: seen.Add(time.Minute)}},
				}))
				require.NoError(t, os.WriteFile(filepath.Join(dir, ledgerFileRaw),
					[]byte("version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize 10\n"), 0o644))
				require.NoError(t, ensureSessionsGitignore(filepath.Join(f.ledgerPath, "sessions")))
				commitAndPushFinalized(t, f.draftLedgerFixture, name)
				return string(stateRecordedLive), name
			},
			abort: func(t *testing.T, recording string) { abortAs(t, "OxOthr", recording) },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, nativePath := newAbortFixture(t)
			cfg = &config.Config{}
			wantBefore, recording := tc.record(t, f, nativePath)

			before := f.run(t, importOptions{dryRun: true, jsonOut: true})
			require.NoError(t, before.err, before.out)
			held := before.session(t, e2eClaudeA)
			require.Equal(t, wantBefore, held.State, "precondition: ox holds the session before the abort")
			require.Equal(t, recording, coveringRecording(held), "precondition: the recording being aborted is the one holding it")

			tc.abort(t, recording)

			// The preview lists it as aborted, unselectable, with when.
			preview := f.run(t, importOptions{dryRun: true, jsonOut: true, agentCtx: true})
			require.NoError(t, preview.err, preview.out)
			aborted := preview.session(t, e2eClaudeA)
			assert.Equal(t, "aborted", aborted.State, "an aborted session must not be offered as ready")
			assert.False(t, aborted.Selected, "an aborted session must not be pre-selected")
			assert.Regexp(t, `^you aborted its recording on \d{4}-\d{2}-\d{2}$`, aborted.Reason)
			assert.Equal(t, 1, preview.report.Counts["aborted"])
			control := preview.session(t, e2eCodexA)
			assert.Equal(t, string(stateReady), control.State)
			assert.True(t, control.Selected)
			assert.Contains(t, preview.report.NextCommand, e2eCodexA)
			assert.NotContains(t, preview.report.NextCommand, e2eClaudeA, "the printed upload command must not carry it")

			text := f.run(t, importOptions{dryRun: true})
			require.NoError(t, text.err, text.out)
			assert.Contains(t, text.out, "aborted by you (1)")
			assert.Contains(t, text.out, nativeShortID(e2eClaudeA)+": you aborted its recording on ")

			// Naming it explicitly uploads nothing.
			remoteHead := runGit(t, f.barePath, "rev-parse", "HEAD")
			named := f.run(t, importOptions{yes: true, jsonOut: true, agentCtx: true, sessions: []string{e2eClaudeA}})
			require.NoError(t, named.err, named.out)
			assert.Equal(t, "aborted", named.session(t, e2eClaudeA).State)
			assert.False(t, named.session(t, e2eClaudeA).Selected)
			assertNothingMoved(t, f, remoteHead)

			// --yes uploads the control and only the control.
			all := f.run(t, importOptions{yes: true, jsonOut: true, agentCtx: true})
			require.NoError(t, all.err, all.out)
			assert.Equal(t, "uploaded", all.session(t, e2eCodexA).Outcome)
			assert.Empty(t, all.session(t, e2eClaudeA).Outcome)
			assert.NotContains(t, remoteSessionDirs(t, f.barePath), aborted.SessionName)
			assert.Zero(t, f.store.holding(abortedPrompt), "the discarded conversation must never reach the LFS store")
			for _, prompt := range f.summarizer.prompts {
				assert.NotContains(t, prompt, abortedPrompt, "the discarded conversation must never be summarized")
			}
		})
	}
}

// Devon aborts one recording, then keeps working: a later recording is primed
// and stopped, and a second one is aborted too. Both aborted sessions stay
// unimportable.
//
// Failure prevented: a record kept with the recording, or rewritten by the
// next abort, forgets the first session and offers it again.
func TestImportE2E_AbortSurvivesLaterRecordings(t *testing.T) {
	f, nativePath := newAbortFixture(t)
	cfg = &config.Config{}
	later := f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeB, start: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC),
		prompt: abortedPrompt + " either", reply: "Understood."})

	_, err := session.StartRecording(f.projectRoot, session.StartRecordingOptions{
		AgentID: "OxAbrt", AdapterName: "claude-code", AgentSessionID: e2eClaudeA, SessionFile: nativePath,
	})
	require.NoError(t, err)
	abortAs(t, "OxAbrt")

	// A later prime and stop of an unrelated recording.
	_, err = session.StartRecording(f.projectRoot, session.StartRecordingOptions{AgentID: "OxNext", AdapterName: "claude-code"})
	require.NoError(t, err)
	_, err = session.StopRecording(f.projectRoot, "OxNext")
	require.NoError(t, err)

	// A second abort, of another session.
	_, err = session.StartRecording(f.projectRoot, session.StartRecordingOptions{
		AgentID: "OxSecd", AdapterName: "claude-code", AgentSessionID: e2eClaudeB, SessionFile: later,
	})
	require.NoError(t, err)
	abortAs(t, "OxSecd")

	r := f.run(t, importOptions{dryRun: true, jsonOut: true})
	require.NoError(t, r.err, r.out)
	assert.Equal(t, "aborted", r.session(t, e2eClaudeA).State, "the first abort must survive the later recordings")
	assert.Equal(t, "aborted", r.session(t, e2eClaudeB).State)
	assert.Equal(t, string(stateReady), r.session(t, e2eCodexA).State)
}

// The record of aborted sessions fails closed: an unreadable record stops
// the import instead of reading as "nothing aborted", and a later abort leaves
// it untouched instead of replacing it with only its own entry.
//
// Failure prevented: a damaged file silently reopens every session the
// coworker discarded.
func TestImportE2E_UnreadableAbortRecordFailsClosed(t *testing.T) {
	f, nativePath := newAbortFixture(t)
	cfg = &config.Config{}
	path := abortedSessionsPath(f.ledgerPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	damaged := []byte(`{"sessions": {"` + e2eClaudeB + `": `)
	require.NoError(t, os.WriteFile(path, damaged, 0o600))

	r := f.run(t, importOptions{dryRun: true, jsonOut: true})
	require.ErrorIs(t, r.err, cli.ErrSilent)
	refusal := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(r.out), &refusal), r.out)
	assert.Equal(t, importErrLedgerUnreadable, refusal["error"])
	assert.Contains(t, refusal["guidance"], "Never delete it")

	_, err := session.StartRecording(f.projectRoot, session.StartRecordingOptions{
		AgentID: "OxAbrt", AdapterName: "claude-code", AgentSessionID: e2eClaudeA, SessionFile: nativePath,
	})
	require.NoError(t, err)
	setForceFlag(t, true)
	var out bytes.Buffer
	agentCmd.SetOut(&out)
	t.Cleanup(func() { agentCmd.SetOut(nil) })
	require.NoError(t, runAgentSessionAbort(&agentinstance.Instance{AgentID: "OxAbrt"}, agentCmd, nil), "abort must still discard the recording")
	var abortOut sessionAbortOutput
	require.NoError(t, json.Unmarshal(out.Bytes(), &abortOut))
	assert.Contains(t, abortOut.Warning, "'ox session import', which may offer this session again")
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, damaged, got, "the damaged record must not be replaced")
}

// Concurrent aborts each keep their entry.
//
// Failure prevented: two AI coworkers aborting at once, each rewriting the
// record from what it read, and one discarded session coming back.
func TestRecordAbortedSessions_ConcurrentAbortsKeepEveryEntry(t *testing.T) {
	ledgerPath := t.TempDir()
	const n = 8
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			errs <- recordAbortedSessions(ledgerPath, fmt.Sprintf("rec-%d", i), "", []abortedNative{{agent: "claude-code", id: fmt.Sprintf("native-%d", i)}}, time.Now())
		}()
	}
	for i := 0; i < n; i++ {
		require.NoError(t, <-errs)
	}
	record, err := loadAbortedSessions(ledgerPath)
	require.NoError(t, err)
	assert.Len(t, record.Sessions, n)
}

// coveringRecording is the recording the preview says holds a session: named
// as covered_by, or in the reason while it is still recording.
func coveringRecording(s importJSONSession) string {
	if s.State == string(stateInProgress) {
		return strings.TrimPrefix(s.Reason, "ox is recording it: ")
	}
	return s.CoveredBy
}
