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
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/nativeimport"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Rule: A session whose recording the coworker aborted is not recorded or imported again unless they ask

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
	require.Empty(t, abortWithWarning(t, agentID, args...).Warning)
}

// abortWithWarning runs the real abort command, which must succeed, and
// returns what it reported.
func abortWithWarning(t *testing.T, agentID string, args ...string) sessionAbortOutput {
	t.Helper()
	setForceFlag(t, true)
	var out bytes.Buffer
	agentCmd.SetOut(&out)
	t.Cleanup(func() { agentCmd.SetOut(nil) })
	require.NoError(t, runAgentSessionAbort(&agentinstance.Instance{AgentID: agentID}, agentCmd, args), out.String())
	var result sessionAbortOutput
	require.NoError(t, json.Unmarshal(out.Bytes(), &result), out.String())
	require.True(t, result.Success)
	return result
}

// captureHeader is a stopped capture's raw.jsonl naming one native session.
func captureHeader(nativeID string) string {
	return `{"type":"header","metadata":{"version":"1","agent_type":"claude-code","created_at":"2026-10-19T09:00:00Z",` +
		`"native_sessions":[{"id":"` + nativeID + `","source":"startup","first_seen":"2026-10-19T09:00:00Z","last_seen":"2026-10-19T09:01:00Z"}]}}` +
		"\n" + `{"type":"user","content":"` + abortedPrompt + `","seq":1}` + "\n"
}

func writeDamage(content string) func(t *testing.T, path string) {
	return func(t *testing.T, path string) { require.NoError(t, os.WriteFile(path, []byte(content), 0o600)) }
}

func unchangedDamage(content string) func(t *testing.T, path string) {
	return func(t *testing.T, path string) {
		got, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, content, string(got), "the damaged record must not be replaced")
	}
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
				require.NoError(t, os.WriteFile(filepath.Join(dir, ledgerFileRaw), []byte(captureHeader(e2eClaudeA)), 0o600))
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
			assert.Contains(t, preview.report.Guidance, "never add --include-aborted on your own")

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

// A damaged record of aborted sessions fails closed and is repaired without
// forgetting an abort: import refuses with its own code instead of reading
// "nothing aborted"; a later abort goes to the repo's other record and leaves
// the damaged one untouched; `ox doctor` keeps every ID the damaged bytes
// still hold and sets them aside.
//
// Failure prevented: a damaged file silently reopens every session the
// coworker discarded, or a repair that deletes it does the same.
func TestImportE2E_DamagedAbortRecordFailsClosedAndDoctorRepairsIt(t *testing.T) {
	damagedNamingCodex := `{"sessions": {"` + e2eCodexA + `": `
	cases := []struct {
		name     string
		damage   func(t *testing.T, path string)
		intact   func(t *testing.T, path string)
		salvages bool // the damage still names e2eCodexA
	}{
		{name: "truncated JSON", damage: writeDamage(damagedNamingCodex), intact: unchangedDamage(damagedNamingCodex), salvages: true},
		{name: "null record", damage: writeDamage("null"), intact: unchangedDamage("null")},
		{
			name: "directory where the file belongs",
			damage: func(t *testing.T, path string) {
				require.NoError(t, os.MkdirAll(path, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(path, "stray"), []byte(damagedNamingCodex), 0o600))
			},
			intact:   func(t *testing.T, path string) { assert.DirExists(t, path) },
			salvages: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, nativePath := newAbortFixture(t)
			cfg = &config.Config{}
			path := abortedSessionsPath(f.ledgerPath)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			tc.damage(t, path)

			r := f.run(t, importOptions{dryRun: true, jsonOut: true})
			require.ErrorIs(t, r.err, cli.ErrSilent)
			refusal := map[string]any{}
			require.NoError(t, json.Unmarshal([]byte(r.out), &refusal), r.out)
			assert.Equal(t, importErrAbortRecordUnreadable, refusal["error"], "a damaged abort record is not a damaged Ledger")
			assert.Contains(t, refusal["message"], path, "the refusal must name the record")
			assert.Contains(t, refusal["guidance"], "Run ox doctor")
			assert.Contains(t, refusal["guidance"], "Never delete the file")

			_, err := session.StartRecording(f.projectRoot, session.StartRecordingOptions{
				AgentID: "OxAbrt", AdapterName: "claude-code", AgentSessionID: e2eClaudeA, SessionFile: nativePath,
			})
			require.NoError(t, err)
			abortAs(t, "OxAbrt") // recorded in the repo's other record, so no warning
			tc.intact(t, path)
			pending, err := loadAbortRecordFile(pendingAbortRecordPath(e2eRepoID))
			require.NoError(t, err)
			assert.Contains(t, pending.Sessions, e2eClaudeA)

			repair := checkAbortRecord(true)
			require.True(t, repair.passed, repair.message+" "+repair.detail)
			assert.Contains(t, repair.message, "repaired")
			matches, err := filepath.Glob(path + ".damaged-*")
			require.NoError(t, err)
			assert.Len(t, matches, 1, "the damaged record is set aside, not deleted")
			assert.True(t, checkAbortRecord(false).passed, "the repaired record reads")

			after := f.run(t, importOptions{dryRun: true, jsonOut: true})
			require.NoError(t, after.err, after.out)
			assert.Equal(t, "aborted", after.session(t, e2eClaudeA).State)
			if tc.salvages {
				assert.Equal(t, "aborted", after.session(t, e2eCodexA).State, "the repair keeps every ID the damaged record held")
			} else {
				assert.Equal(t, string(stateReady), after.session(t, e2eCodexA).State)
			}
		})
	}
}

// An abort made while the repo has no Ledger clone is still recorded, and
// the import honors it once the Ledger is there.
//
// Failure prevented: the abort completes with only a warning, and the first
// import after the Ledger is cloned offers the discarded session as ready.
func TestImportE2E_AbortWithoutALedgerIsStillHonored(t *testing.T) {
	f, nativePath := newAbortFixture(t)
	cfg = &config.Config{}
	_, err := session.StartRecording(f.projectRoot, session.StartRecordingOptions{
		AgentID: "OxAbrt", AdapterName: "claude-code", AgentSessionID: e2eClaudeA, SessionFile: nativePath,
	})
	require.NoError(t, err)
	parked := f.ledgerPath + ".not-cloned-yet"
	require.NoError(t, os.Rename(f.ledgerPath, parked))
	abortAs(t, "OxAbrt")
	require.NoError(t, os.Rename(parked, f.ledgerPath))

	r := f.run(t, importOptions{dryRun: true, jsonOut: true})
	require.NoError(t, r.err, r.out)
	assert.Equal(t, "aborted", r.session(t, e2eClaudeA).State)
	assert.Equal(t, string(stateReady), r.session(t, e2eCodexA).State)
}

// An abort that fails while the recording can still upload takes its record
// back; one that failed after the recording was disarmed keeps it.
//
// Failure prevented: a failed abort leaves a live recording that uploads at
// stop while its native session is marked aborted forever.
func TestAbort_FailedAbortTakesItsRecordBack(t *testing.T) {
	cases := []struct {
		name   string
		record func(t *testing.T, f *importFixture) string // returns the session name to abort
		breaks func(t *testing.T, f *importFixture, name string)
	}{
		{
			name: "local capture whose recording state cannot be removed",
			record: func(t *testing.T, f *importFixture) string {
				name := "2026-10-19T09-00-devon-OxCapt"
				dir := filepath.Join(session.GetContextPath(e2eRepoID), "sessions", name)
				require.NoError(t, os.MkdirAll(dir, 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(dir, ledgerFileRaw), []byte(captureHeader(e2eClaudeA)), 0o600))
				return name
			},
			breaks: func(t *testing.T, _ *importFixture, name string) {
				// a non-empty directory where .recording.json belongs cannot be removed
				stuck := filepath.Join(session.GetContextPath(e2eRepoID), "sessions", name, ".recording.json")
				require.NoError(t, os.MkdirAll(stuck, 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(stuck, "x"), []byte("x"), 0o600))
			},
		},
		{
			name: "finalized session the Ledger cannot remove",
			record: func(t *testing.T, f *importFixture) string {
				name := "2026-10-19T09-00-devon-OxFinl"
				dir := filepath.Join(f.ledgerPath, "sessions", name)
				require.NoError(t, os.MkdirAll(dir, 0o755))
				seen := abortStart
				require.NoError(t, lfs.WriteSessionMetaOnly(dir, &lfs.SessionMeta{
					SessionName: name, SessionID: sessionScopedID(name), AgentType: "claude-code", CreatedAt: seen,
					Files:          map[string]lfs.FileRef{ledgerFileRaw: {OID: "sha256:abc", Size: 10}},
					NativeSessions: []lfs.NativeSession{{ID: e2eClaudeA, FirstSeen: seen, LastSeen: seen}},
				}))
				require.NoError(t, ensureSessionsGitignore(filepath.Join(f.ledgerPath, "sessions")))
				commitAndPushFinalized(t, f.draftLedgerFixture, name)
				return name
			},
			breaks: func(t *testing.T, f *importFixture, _ string) {
				// a garbage index makes git rm fail before it touches the session
				require.NoError(t, os.WriteFile(filepath.Join(f.ledgerPath, ".git", "index"), []byte("garbage"), 0o644))
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := newAbortFixture(t)
			cfg = &config.Config{}
			name := tc.record(t, f)
			tc.breaks(t, f, name)
			setForceFlag(t, true)
			agentCmd.SetOut(&bytes.Buffer{})
			t.Cleanup(func() { agentCmd.SetOut(nil) })
			require.Error(t, runAgentSessionAbort(&agentinstance.Instance{AgentID: "OxOthr"}, agentCmd, []string{name}))

			record, err := loadAbortRecords(f.ledgerPath, e2eRepoID)
			require.NoError(t, err)
			assert.NotContains(t, record.Sessions, e2eClaudeA, "a failed abort must take its record back")
		})
	}
}

// A finalized session whose meta.json cannot be read is killed by name only
// through a local copy (abort refuses a Ledger directory it cannot classify).
// The abort is recorded from that copy, and when the copy names nothing the
// coworker is warned.
//
// Failure prevented: the kill deletes the only readable identity and the
// native transcript is offered again with no warning.
func TestImportE2E_FinalizedKillWithUnreadableMeta(t *testing.T) {
	for _, namesIt := range []bool{true, false} {
		t.Run(fmt.Sprintf("local copy names the session %t", namesIt), func(t *testing.T) {
			f, _ := newAbortFixture(t)
			cfg = &config.Config{}
			name := "2026-10-19T09-00-devon-OxBadM"
			dir := filepath.Join(f.ledgerPath, "sessions", name)
			require.NoError(t, os.MkdirAll(dir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"session_name": "`), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(dir, ledgerFileRaw),
				[]byte("version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize 10\n"), 0o644))
			require.NoError(t, ensureSessionsGitignore(filepath.Join(f.ledgerPath, "sessions")))
			commitAndPushFinalized(t, f.draftLedgerFixture, name)
			hydrated := filepath.Join(f.ledgerPath, ".sageox", "cache", "sessions", name)
			require.NoError(t, os.MkdirAll(hydrated, 0o755))
			raw := captureHeader(e2eClaudeA)
			if !namesIt {
				raw = `{"type":"header","metadata":{"version":"1","created_at":"2026-10-19T09:00:00Z"}}` + "\n" +
					`{"type":"user","content":"` + abortedPrompt + `","seq":1}` + "\n"
			}
			require.NoError(t, os.WriteFile(filepath.Join(hydrated, ledgerFileRaw), []byte(raw), 0o600))

			abortOut := abortWithWarning(t, "OxOthr", name)
			assert.Empty(t, remotePathsUnder(t, f.barePath, name), "the kill must still remove the session")
			r := f.run(t, importOptions{dryRun: true, jsonOut: true})
			require.NoError(t, r.err, r.out)
			if namesIt {
				assert.Empty(t, abortOut.Warning)
				assert.Equal(t, "aborted", r.session(t, e2eClaudeA).State)
			} else {
				assert.Contains(t, abortOut.Warning, "which may offer this session again")
			}
		})
	}
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
			_, err := recordAbortedSessions(abortedSessionsPath(ledgerPath), fmt.Sprintf("rec-%d", i), "",
				[]abortedNative{{agent: "claude-code", id: fmt.Sprintf("native-%d", i)}}, time.Now())
			errs <- err
		}()
	}
	for i := 0; i < n; i++ {
		require.NoError(t, <-errs)
	}
	record, err := loadAbortRecordFile(abortedSessionsPath(ledgerPath))
	require.NoError(t, err)
	assert.Len(t, record.Sessions, n)
}

// Devon changes their mind about one aborted session: naming it with
// --session and --include-aborted uploads exactly that session, after a
// confirmation that says it was aborted. Nothing else widens.
//
// Failure prevented: an abort that can never be undone, or an override that
// reaches sessions the coworker did not name.
func TestImportE2E_IncludeAbortedUploadsOnlyTheNamedSession(t *testing.T) {
	f, nativePath := newAbortFixture(t)
	cfg = &config.Config{}
	later := f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeB, start: abortStart.Add(3 * time.Hour),
		prompt: abortedPrompt + " either, it is about the same deal", reply: "Understood."})
	for _, rec := range []struct{ agent, native, file string }{{"OxAbrt", e2eClaudeA, nativePath}, {"OxSecd", e2eClaudeB, later}} {
		_, err := session.StartRecording(f.projectRoot, session.StartRecordingOptions{
			AgentID: rec.agent, AdapterName: "claude-code", AgentSessionID: rec.native, SessionFile: rec.file,
		})
		require.NoError(t, err)
		abortAs(t, rec.agent)
	}

	// Without --session the flag is refused before anything runs.
	cmd := &cobra.Command{Use: "import"}
	cmd.PersistentFlags().Bool("json", true, "")
	addSessionImportFlags(cmd.Flags())
	require.NoError(t, cmd.ParseFlags([]string{"--include-aborted"}))
	_, refusal := parseImportOptions(cmd)
	require.NotNil(t, refusal)
	assert.Equal(t, importErrBadFlag, refusal.Code)
	assert.Contains(t, refusal.Message, "--include-aborted requires --session")

	unnamed := f.run(t, importOptions{dryRun: true, jsonOut: true, includeAborted: true, sessions: []string{e2eCodexA}})
	require.NoError(t, unnamed.err, unnamed.out)
	assert.Equal(t, "aborted", unnamed.session(t, e2eClaudeA).State, "the override reaches only the sessions it names")

	preview := f.run(t, importOptions{dryRun: true, jsonOut: true, agentCtx: true, includeAborted: true, sessions: []string{e2eClaudeA}})
	require.NoError(t, preview.err, preview.out)
	named := preview.session(t, e2eClaudeA)
	assert.Equal(t, string(stateReady), named.State)
	assert.True(t, named.Selected)
	assert.True(t, named.IncludedAborted)
	assert.Contains(t, named.Reason, "included by --include-aborted")
	assert.Equal(t, "aborted", preview.session(t, e2eClaudeB).State, "an aborted session it does not name stays aborted")
	assert.Contains(t, preview.report.NextCommand, "--include-aborted")

	text := f.run(t, importOptions{dryRun: true, includeAborted: true, sessions: []string{e2eClaudeA}})
	require.NoError(t, text.err, text.out)
	assert.Contains(t, text.out, nativeShortID(e2eClaudeA)+"  (you aborted its recording)")

	// At a terminal, the confirmation says what is being undone.
	var asked string
	f.interactive = true
	f.confirm = func(prompt string) (bool, error) { asked = prompt; return false, nil }
	declined := f.run(t, importOptions{includeAborted: true, sessions: []string{e2eClaudeA}})
	require.NoError(t, declined.err, declined.out)
	assert.Contains(t, asked, "including 1 whose recording you aborted")
	assert.Zero(t, f.store.count(), "a declined confirmation uploads nothing")
	f.interactive, f.confirm = false, nil

	uploaded := f.run(t, optionsFromCommand(t, preview.report.NextCommand))
	require.NoError(t, uploaded.err, uploaded.out)
	assert.Equal(t, "uploaded", uploaded.session(t, e2eClaudeA).Outcome)
	assert.Contains(t, remoteSessionDirs(t, f.barePath), named.SessionName)
	assert.Empty(t, uploaded.session(t, e2eClaudeB).Outcome)
}

// Resuming a conversation whose recording Devon aborted does not start a new
// recording; prime says why, and an unrelated conversation still records. A
// damaged record stops auto-recording until ox doctor repairs it.
//
// Failure prevented: SessionStart prime on resume quietly records the
// discarded conversation again and publishes it at stop.
func TestPrime_DoesNotAutoRecordAnAbortedConversation(t *testing.T) {
	f, _ := newAbortFixture(t)
	cfg = &config.Config{}
	defaultLedger, err := ledger.DefaultPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(defaultLedger, ".git"), 0o755))
	oxConfigSetRepo(t, "session_recording", "auto")

	first := startSessionRecording(f.projectRoot, "OxPrm1", "claude-code", "", "", e2eClaudeA)
	require.NotNil(t, first)
	require.True(t, first.Recording, "control: prime records a new conversation")
	abortAs(t, "OxPrm1")

	resumed := startSessionRecording(f.projectRoot, "OxPrm1", "claude-code", "", "", e2eClaudeA)
	require.NotNil(t, resumed)
	assert.False(t, resumed.Recording, "a resumed aborted conversation must not be recorded again")
	assert.Contains(t, resumed.UserNotification, "you aborted ox's recording of this conversation on ")
	assert.Contains(t, resumed.UserNotification, "ox agent OxPrm1 session start")
	state, err := session.LoadRecordingStateForAgent(f.projectRoot, "OxPrm1")
	require.NoError(t, err)
	assert.Nil(t, state, "no recording may start")

	other := startSessionRecording(f.projectRoot, "OxPrm2", "claude-code", "", "", e2eClaudeB)
	require.NotNil(t, other)
	assert.True(t, other.Recording, "another conversation still records")

	require.NoError(t, os.WriteFile(abortedSessionsPath(f.ledgerPath), []byte("{"), 0o600))
	blocked := startSessionRecording(f.projectRoot, "OxPrm3", "claude-code", "", "", e2eClaudeC)
	require.NotNil(t, blocked)
	assert.False(t, blocked.Recording, "an unreadable record fails closed")
	assert.Contains(t, blocked.UserNotification, "ox doctor")
	require.True(t, checkAbortRecord(true).passed)
	repaired := startSessionRecording(f.projectRoot, "OxPrm3", "claude-code", "", "", e2eClaudeC)
	require.NotNil(t, repaired)
	assert.True(t, repaired.Recording, "after the repair, an unrelated conversation records again")
}

// coveringRecording is the recording the preview says holds a session: named
// as covered_by, or in the reason while it is still recording.
func coveringRecording(s importJSONSession) string {
	if s.State == string(stateInProgress) {
		return strings.TrimPrefix(s.Reason, "ox is recording it: ")
	}
	return s.CoveredBy
}
