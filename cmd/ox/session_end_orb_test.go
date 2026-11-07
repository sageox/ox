package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/testguard"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Customer promise (sageox/ox#1314): Avery's team runs Amp in orbs. When a
// thread goes idle, the ox-bridge plugin sends SessionEnd, and the session
// reaches the team's Ledger before the orb pauses. No daemon has to outlive
// the agent, and no claude/codex/gemini CLI has to be logged in.
//
// Red-first (verified while authoring), each failing on "the session must reach
// the Ledger before the orb pauses":
//   - drop `!runtime.Caps().HostEndsWithAgent` from daemonOutlivesSession, and
//     SessionEnd hands the recording to a daemon that is frozen with the orb;
//   - drop the thread-URL reduction in ox-adapter-amp's findAmpSession, and the
//     stop cannot find the bridge's sidecar for a recording keyed by the URL.

const (
	orbThreadURL = "https://ampcode.com/threads/T-5f0c2a71-3b9e-4d1a-8c6f-2e7d9b4a1c03"
	orbPrompt    = "Rename the retry helper and update every caller in the billing service"
	orbReply     = "Renamed retryWithBackoff to retryIdempotent and updated the four callers."
)

// orbSessionFixture is a project with a real Ledger clone of a bare remote,
// a fake LFS store, the real ox-adapter-amp binary, and a live Amp recording
// whose bridge sidecar holds one turn.
type orbSessionFixture struct {
	*draftLedgerFixture
	store   *fakeLFSStore
	agentID string
	sidecar string // ~/.cache/amp/ox-sessions/<thread id>.jsonl
}

func newOrbSessionFixture(t *testing.T) *orbSessionFixture {
	t.Helper()
	f := newOrbLedgerFixture(t)

	// what `ox agent prime` leaves behind in the orb: a live recording keyed
	// by the thread URL Amp exported as AMP_THREAD_URL, with no sidecar path
	// (Amp records in hook mode, so the stop looks the sidecar up)
	state, err := session.StartRecording(f.projectRoot, session.StartRecordingOptions{
		AgentID: f.agentID, AdapterName: "amp", Username: "amp",
		AgentSessionID: orbThreadURL, WorkspacePath: f.projectRoot,
	})
	require.NoError(t, err)
	require.Empty(t, state.SessionFile, "precondition: the stop must find the sidecar itself")
	require.NoError(t, writeRawHeader(f.projectRoot, state))

	// what ox-bridge.ts appends for one Amp turn after that
	at := state.StartedAt.UTC()
	f.appendSidecar(t,
		ampSidecarLine{Type: "user", Content: orbPrompt, At: at.Add(time.Second)},
		ampSidecarLine{Type: "assistant", Content: orbReply, At: at.Add(2 * time.Second)},
	)
	return f
}

// newOrbLedgerFixture is the orb environment with no recording yet: a
// project with a real Ledger clone of a bare remote, a fake LFS store, the
// real ox-adapter-amp binary, and an empty bridge sidecar directory.
func newOrbLedgerFixture(t *testing.T) *orbSessionFixture {
	t.Helper()
	base := newDraftLedgerFixture(t)
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		t.Setenv(key, t.TempDir())
	}
	// The thread's session marker is keyed by the constant orbThreadURL. In
	// the machine-wide marker dir, a concurrent `go test ./cmd/ox` run writes
	// and deletes that same file, so a re-prime read no prior marker and the
	// reply's session lost its continued-from link.
	isolateSessionMarkerDir(t)
	// a laptop baseline with a working daemon: the orb is the only difference
	clearFinalizeEnv(t)
	priorDir := gitserver.TestSetConfigDirOverride(t.TempDir())
	t.Cleanup(func() { gitserver.TestSetConfigDirOverride(priorDir) })
	priorStorage := gitserver.TestSetForceFileStorage(true)
	t.Cleanup(func() { gitserver.TestSetForceFileStorage(priorStorage) })
	oldCfg := cfg
	cfg = &config.Config{}
	t.Cleanup(func() { cfg = oldCfg })
	cli.SetNoInteractive(true)
	t.Cleanup(func() { cli.SetNoInteractive(false) })

	store := newFakeLFSStore(t)
	t.Setenv("SAGEOX_ENDPOINT", store.url)
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(store.url, gitserver.GitCredentials{
		Username: "testuser", Token: "test-token", ServerURL: store.url, ExpiresAt: time.Now().Add(time.Hour),
	}))
	// the LFS batch URL derives from the fetch remote; the push lands on the bare repo
	runGit(t, base.ledgerPath, "remote", "set-url", "origin", store.url+"/ledger.git")
	runGit(t, base.ledgerPath, "remote", "set-url", "--push", "origin", base.barePath)
	t.Chdir(base.projectRoot)

	// The real adapter, so the stop's sidecar lookup is the one that ships.
	adapterBin := filepath.Join(t.TempDir(), "ox-adapter-amp")
	build := exec.Command("go", "build", "-o", adapterBin, "./cmd/ox-adapter-amp")
	build.Dir = filepath.Join(packageDir, "..", "..") // the repository root
	build.Env = testguard.MinimalEnv(nil)
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build ox-adapter-amp: %s", out)
	ampAdapter, err := adapters.NewExternalAdapter(adapterBin)
	require.NoError(t, err)
	adapters.Register(ampAdapter)
	t.Cleanup(func() { adapters.Unregister("amp") })

	// ox-bridge.ts writes ~/.cache/amp/ox-sessions/<thread id>.jsonl
	sidecar := filepath.Join(os.Getenv("HOME"), ".cache", "amp", "ox-sessions", nativeThreadKey(orbThreadURL)+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(sidecar), 0o755))

	// The stop child runs in-process (selfexec refuses to re-exec a test
	// binary): the same command the child runs, and the same read of what it
	// printed.
	prior := runSessionStopAtEnd
	runSessionStopAtEnd = func(_, id string) error {
		var err error
		printed := captureRealStdout(t, func() {
			err = runAgentSessionStopCommand(stopCommandFlags(true), &agentinstance.Instance{AgentID: id})
		})
		if err != nil {
			return fmt.Errorf("%w: %w", errSessionStopFailed, err)
		}
		return sessionStopOutcome(printed)
	}
	t.Cleanup(func() { runSessionStopAtEnd = prior })

	return &orbSessionFixture{draftLedgerFixture: base, store: store, agentID: "OxOrb1", sidecar: sidecar}
}

// ampSidecarLine is one line ox-bridge.ts appends to a thread's sidecar.
type ampSidecarLine struct {
	Type    string // user, assistant, tool_use, tool_result
	Content string
	At      time.Time
}

// appendSidecar appends lines the way ox-bridge.ts writes them.
func (f *orbSessionFixture) appendSidecar(t *testing.T, lines ...ampSidecarLine) {
	t.Helper()
	sidecar, err := os.OpenFile(f.sidecar, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	defer sidecar.Close()
	for _, l := range lines {
		v := map[string]any{"type": l.Type, "timestamp": l.At.UTC().Format(time.RFC3339Nano)}
		switch l.Type {
		case "tool_use":
			v["tool_name"], v["tool_input"], v["call_id"] = "Bash", l.Content, "toolu_"+strconv.FormatInt(l.At.UnixNano(), 36)
		default:
			v["content"] = l.Content
		}
		line, err := json.Marshal(v)
		require.NoError(t, err)
		_, err = sidecar.Write(append(line, '\n'))
		require.NoError(t, err)
	}
}

// stopCommandFlags stands in for the cobra command `session stop` runs under.
func stopCommandFlags(agentEnded bool) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("agent-ended", agentEnded, "")
	return cmd
}

// sendSessionEnd runs `ox agent hook SessionEnd` the way ox-bridge.ts does:
// AGENT_ENV=amp, the thread URL on stdin, no session marker on this machine.
func (f *orbSessionFixture) sendSessionEnd(t *testing.T) {
	t.Helper()
	t.Setenv("AGENT_ENV", "amp")
	priorInput := ReadHookInput
	ReadHookInput = func() *agentx.HookInput {
		return &agentx.HookInput{SessionID: orbThreadURL, HookEventName: "SessionEnd"}
	}
	t.Cleanup(func() { ReadHookInput = priorInput })
	require.NoError(t, runAgentHook([]string{"SessionEnd"}))
}

func TestSessionEnd_AmpOrbUploadsBeforeTheOrbPauses(t *testing.T) {
	f := newOrbSessionFixture(t)
	t.Setenv("AMP_ORB", "1")

	f.sendSessionEnd(t)

	dirs := remoteSessionDirs(t, f.barePath)
	require.Len(t, dirs, 1, "the session must reach the Ledger before the orb pauses")
	oid, size, err := lfs.ParsePointer(runGit(t, f.barePath, "show", "HEAD:sessions/"+dirs[0]+"/raw.jsonl") + "\n")
	require.NoError(t, err, "raw.jsonl on the remote must be an LFS pointer")
	raw := f.store.object(oid)
	require.NotNil(t, raw, "the remote points at a transcript the LFS store never received")
	assert.EqualValues(t, size, len(raw))
	assert.Contains(t, string(raw), orbPrompt, "the uploaded transcript must hold the coworker's prompt")
	assert.Contains(t, string(raw), orbReply, "the uploaded transcript must hold Amp's reply")

	state, err := session.LoadRecordingStateForAgent(f.projectRoot, f.agentID)
	require.NoError(t, err)
	assert.Nil(t, state, "the recording is finished, not left live in the orb")
	assert.False(t, session.HasExplicitStop(f.projectRoot, f.agentID),
		"the agent ended, the coworker did not opt out: a later prime must record again")
}

// TestSessionEnd_LaptopStillHandsOffToTheDaemon is the control: on a laptop
// the daemon outlives the agent, so SessionEnd returns at once and leaves the
// upload to it, as before.
func TestSessionEnd_LaptopStillHandsOffToTheDaemon(t *testing.T) {
	f := newOrbSessionFixture(t)
	stopRan := false
	runSessionStopAtEnd = func(string, string) error { stopRan = true; return nil }

	f.sendSessionEnd(t)

	assert.False(t, stopRan, "a laptop's SessionEnd must not block on the upload")
	assert.Empty(t, remoteSessionDirs(t, f.barePath), "the daemon, not the hook, uploads on a laptop")
	state, err := session.LoadRecordingStateForAgent(f.projectRoot, f.agentID)
	require.NoError(t, err)
	assert.Nil(t, state, "the handoff still clears the live recording")
}

// TestHandleEnd_StopThatNeverRanFallsBackToTheDaemon: when the stop never
// claimed the recording (no ox executable, or it refused before marking the
// recording stopped), the recording is as SessionEnd found it, so the daemon
// handoff takes it. Never worse than before the orb path existed.
func TestHandleEnd_StopThatNeverRanFallsBackToTheDaemon(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"could not launch", errSessionStopNotLaunched},
		{"refused before marking", fmt.Errorf("%w: exit status 1", errSessionStopFailed)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projectRoot, repoID := setupTestProject(t)
			clearFinalizeEnv(t)
			t.Setenv("AMP_ORB", "1")
			const agentID = "OxOrbFallback"
			createActiveRecording(t, projectRoot, repoID, agentID)
			prior := runSessionStopAtEnd
			runSessionStopAtEnd = func(string, string) error { return tc.err }
			t.Cleanup(func() { runSessionStopAtEnd = prior })

			require.NoError(t, handleEnd(&HookContext{Phase: phaseEnd, AgentType: "amp", ProjectRoot: projectRoot, Marker: &SessionMarker{AgentID: agentID}}))

			state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
			require.NoError(t, err)
			assert.Nil(t, state, "the daemon handoff must still run")
		})
	}
}

// TestHandleEnd_StopThatStartedOwnsTheRecording: a stop that failed after
// marking the recording (upload failed, recording kept for `ox doctor`) or is
// still uploading must not have the handoff rewrite the recording under it.
func TestHandleEnd_StopThatStartedOwnsTheRecording(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func(projectRoot, agentID string) error
	}{
		{"failed after marking", func(projectRoot, agentID string) error {
			require.NoError(t, session.MarkExplicitStop(projectRoot, agentID))
			return fmt.Errorf("%w: exit status 1", errSessionStopFailed)
		}},
		{"still uploading", func(string, string) error { return errSessionStopStillRunning }},
		// the finished stop already cleared its marker; the outcome alone must
		// keep the handoff from stamping a second stop onto the written session
		{"upload failed", func(string, string) error {
			return fmt.Errorf("%w: ledger upload failed", errSessionUploadFailed)
		}},
		{"outcome unreadable", func(string, string) error { return errors.New("session stop printed no result") }},
		// session_publishing: manual — the stop kept it local; the handoff must
		// not stamp a stop onto it or hand it to the daemon to publish
		{"held", func(string, string) error { return errSessionHeld }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projectRoot, repoID := setupTestProject(t)
			clearFinalizeEnv(t)
			t.Setenv("AMP_ORB", "1")
			const agentID = "OxOrbOwned"
			createActiveRecording(t, projectRoot, repoID, agentID)
			prior := runSessionStopAtEnd
			runSessionStopAtEnd = tc.stop
			t.Cleanup(func() { runSessionStopAtEnd = prior })

			require.NoError(t, handleEnd(&HookContext{Phase: phaseEnd, AgentType: "amp", ProjectRoot: projectRoot, Marker: &SessionMarker{AgentID: agentID}}))

			state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
			require.NoError(t, err)
			require.NotNil(t, state, "the stop owns the recording; the handoff must not clear it")
			assert.Nil(t, state.StoppedAt, "the handoff must not mark it stopped either")
		})
	}
}

func TestSessionEndAgentID(t *testing.T) {
	projectRoot, _ := setupTestProject(t)
	_, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
		AgentID: "OxThread", AdapterName: "amp", Username: "amp", AgentSessionID: orbThreadURL,
	})
	require.NoError(t, err)
	// a recording already stopped for its thread is not the one to end
	_, err = session.StartRecording(projectRoot, session.StartRecordingOptions{
		AgentID: "OxStopped", AdapterName: "amp", Username: "amp", AgentSessionID: "https://ampcode.com/threads/T-stopped",
	})
	require.NoError(t, err)
	require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, "OxStopped", func(s *session.RecordingState) {
		now := time.Now()
		s.StoppedAt = &now
	}))
	// a recording that has since moved on to another native session still owns the earlier one
	_, err = session.StartRecording(projectRoot, session.StartRecordingOptions{
		AgentID: "OxMoved", AdapterName: "amp", Username: "amp", AgentSessionID: "https://ampcode.com/threads/T-earlier",
	})
	require.NoError(t, err)
	require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, "OxMoved", func(s *session.RecordingState) {
		s.RecordNativeSession("https://ampcode.com/threads/T-current", "resume", time.Now())
	}))

	for _, tc := range []struct {
		name   string
		marker *SessionMarker
		input  string
		want   string
	}{
		{"marker wins", &SessionMarker{AgentID: "OxMarker"}, orbThreadURL, "OxMarker"},
		{"thread URL matches the recording", nil, orbThreadURL, "OxThread"},
		{"trailing slash and other host still match", nil, "https://amp.example.com/threads/T-5f0c2a71-3b9e-4d1a-8c6f-2e7d9b4a1c03/", "OxThread"},
		{"bare thread id matches", nil, "T-5f0c2a71-3b9e-4d1a-8c6f-2e7d9b4a1c03", "OxThread"},
		{"another thread matches nothing", nil, "https://ampcode.com/threads/T-other", ""},
		{"a stopped recording is skipped", nil, "https://ampcode.com/threads/T-stopped", ""},
		{"an earlier native session still matches", nil, "https://ampcode.com/threads/T-earlier", "OxMoved"},
		{"no session id", nil, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &HookContext{ProjectRoot: projectRoot, Marker: tc.marker}
			if tc.input != "" {
				ctx.Input = &agentx.HookInput{SessionID: tc.input}
			}
			assert.Equal(t, tc.want, sessionEndAgentID(ctx))
		})
	}
}

func TestSessionStopCommand(t *testing.T) {
	t.Setenv("AMP_ORB", "1")
	t.Setenv("CLICOLOR_FORCE", "1")
	cmd := sessionStopCommand("/usr/local/bin/ox", "/work/repo", "OxAb12")
	assert.Equal(t, []string{"/usr/local/bin/ox", "agent", "OxAb12", "session", "stop", "--agent-ended"}, cmd.Args)
	assert.Equal(t, "/work/repo", cmd.Dir)
	assert.Nil(t, cmd.Stdout, "no pipe: the hook may exit while the upload runs")
	assert.Nil(t, cmd.Stderr, "no pipe: the hook may exit while the upload runs")
	assert.Contains(t, cmd.Env, "AMP_ORB=1", "the child inherits the venue, so it also uploads synchronously")
	// exec keeps the last value of a repeated key, so these win over the inherited ones
	n := len(cmd.Env)
	assert.Equal(t, []string{"OX_COLOR_PROFILE=ascii", "NO_COLOR=1"}, cmd.Env[n-2:], "the child's output is parsed: no color")
}

// fakeOxStop writes a stand-in for the ox binary that records its arguments
// and working directory, prints result, waits delay, marks itself done, and
// exits with code.
func fakeOxStop(t *testing.T, result, delay string, code int) (exe, calls, done string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for ox")
	}
	dir := t.TempDir()
	exe, calls, done = filepath.Join(dir, "ox"), filepath.Join(dir, "calls"), filepath.Join(dir, "done")
	printed := filepath.Join(dir, "printed")
	require.NoError(t, os.WriteFile(printed, []byte(result), 0o600))
	script := "#!/bin/sh\n" +
		"echo \"$@\" > '" + calls + "'\n" +
		"pwd -P >> '" + calls + "'\n" +
		"cat '" + printed + "'\n" +
		"[ " + strconv.Itoa(code) + " -ne 0 ] && printf '\\033[31mError: not currently recording\\033[0m\\nRun ox agent prime\\n' >&2\n" +
		"sleep " + delay + "\n" +
		"touch '" + done + "'\n" +
		"exit " + strconv.Itoa(code) + "\n"
	require.NoError(t, os.WriteFile(exe, []byte(script), 0o755))
	return exe, calls, done
}

// TestWaitForSessionStop covers the process boundary SessionEnd crosses: the
// stop child gets the agent and the project, its failure is reported as a
// failure (so the handoff does not rewrite the recording under it), and a
// stop that outlives the hook's wait keeps uploading after the hook returns.
func TestWaitForSessionStop(t *testing.T) {
	projectRoot, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	t.Run("finished", func(t *testing.T) {
		exe, calls, _ := fakeOxStop(t, "", "0", 0)
		require.NoError(t, waitForSessionStop(sessionStopCommand(exe, projectRoot, "OxAb12"), time.Minute))
		got, err := os.ReadFile(calls)
		require.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(string(got)), "\n")
		require.Len(t, lines, 2)
		assert.Equal(t, "agent OxAb12 session stop --agent-ended", lines[0])
		assert.Equal(t, projectRoot, lines[1], "the stop runs in the project, where it finds the recording")
	})

	t.Run("failed", func(t *testing.T) {
		exe, _, _ := fakeOxStop(t, "", "0", 3)
		err := waitForSessionStop(sessionStopCommand(exe, projectRoot, "OxAb12"), time.Minute)
		require.ErrorIs(t, err, errSessionStopFailed)
		assert.NotErrorIs(t, err, errSessionStopNotLaunched, "it ran, so it may have marked the recording")
		assert.NotErrorIs(t, err, errSessionStopStillRunning)
	})

	t.Run("outlives the wait and keeps uploading", func(t *testing.T) {
		if testing.Short() {
			t.Skip("short: waits about a second for the detached stop")
		}
		exe, _, done := fakeOxStop(t, "", "1", 0)
		start := time.Now()
		err := waitForSessionStop(sessionStopCommand(exe, projectRoot, "OxAb12"), 50*time.Millisecond)
		require.ErrorIs(t, err, errSessionStopStillRunning)
		assert.Less(t, time.Since(start), time.Second, "the hook stops waiting at its bound")
		require.Eventually(t, func() bool { _, err := os.Stat(done); return err == nil }, 10*time.Second, 20*time.Millisecond,
			"the detached stop must finish after the hook has returned")
	})

	t.Run("cannot start", func(t *testing.T) {
		err := waitForSessionStop(sessionStopCommand(filepath.Join(t.TempDir(), "missing-ox"), projectRoot, "OxAb12"), time.Minute)
		require.ErrorIs(t, err, errSessionStopNotLaunched)
	})
}

// TestRunSessionStopProcess_NeverRunsTheTestBinary: under go test the stop is
// reported as never launched, which is what sends handleEnd to the handoff.
func TestRunSessionStopProcess_NeverRunsTheTestBinary(t *testing.T) {
	require.ErrorIs(t, runSessionStopProcess(t.TempDir(), "OxAb12"), errSessionStopNotLaunched)
}

// TestSessionStopOutcome: `session stop` exits 0 when the upload fails, so the
// hook reads the result it printed. Whispers can come first.
func TestSessionStopOutcome(t *testing.T) {
	for _, tc := range []struct {
		name    string
		printed string
		want    error
		wantErr bool
	}{
		{"uploaded", "{\n  \"success\": true,\n  \"type\": \"session_stop\"\n}\n", nil, false},
		{"whispers first", "<whisper from=\"Avery\">{not json}</whisper>\n{\n  \"success\": true\n}\n", nil, false},
		{"upload failed", "{\n  \"success\": true,\n  \"upload_warning\": \"Session saved locally but ledger upload failed.\"\n}\n", errSessionUploadFailed, true},
		{"forced color", "\x1b[1m{\x1b[0m\n  \x1b[34m\"upload_warning\"\x1b[0m: \x1b[32m\"ledger upload failed\"\x1b[0m\n}\n", errSessionUploadFailed, true},
		// a held stop names the session in upload_warning too; held wins
		{"held", "{\n  \"success\": true,\n  \"held\": true,\n  \"upload_warning\": \"Session held on this machine: ox session upload 2026-10-14T09-00-amp-OxOrb1\"\n}\n", errSessionHeld, true},
		{"nothing printed", "", nil, true},
		{"truncated", "{\n  \"success\": tr", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := sessionStopOutcome([]byte(tc.printed))
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			if tc.want != nil {
				assert.ErrorIs(t, err, tc.want)
			}
		})
	}
}

// TestSessionStop_CoworkerStopStillSuppressesTheNextAutoStart: only the
// SessionEnd hook's stop (--agent-ended) clears the explicit-stop signal. A
// coworker who runs `session stop` themselves still opted out of the next
// automatic recording.
func TestSessionStop_CoworkerStopStillSuppressesTheNextAutoStart(t *testing.T) {
	f := newOrbSessionFixture(t)
	captureRealStdout(t, func() {
		require.NoError(t, runAgentSessionStopCommand(stopCommandFlags(false), &agentinstance.Instance{AgentID: f.agentID}))
	})
	assert.True(t, session.HasExplicitStop(f.projectRoot, f.agentID))
}

// TestRunSessionStopWith drives the whole process boundary SessionEnd uses:
// the child's printed result, read back from a file after it exits.
func TestRunSessionStopWith(t *testing.T) {
	projectRoot := t.TempDir()
	uploaded := "<whisper from=\"Avery\">rebasing the billing branch</whisper>\n{\n  \"success\": true,\n  \"type\": \"session_stop\"\n}\n"
	keptLocally := "{\n  \"success\": true,\n  \"upload_warning\": \"Session saved locally but ledger upload failed. Run 'ox doctor' to retry.\"\n}\n"

	t.Run("uploaded", func(t *testing.T) {
		exe, _, _ := fakeOxStop(t, uploaded, "0", 0)
		require.NoError(t, runSessionStopWith(exe, projectRoot, "OxAb12", time.Minute))
	})
	t.Run("exit 0 with a failed upload is a failure", func(t *testing.T) {
		exe, _, _ := fakeOxStop(t, keptLocally, "0", 0)
		require.ErrorIs(t, runSessionStopWith(exe, projectRoot, "OxAb12", time.Minute), errSessionUploadFailed)
	})
	t.Run("non-zero exit says why", func(t *testing.T) {
		exe, _, _ := fakeOxStop(t, "", "0", 1)
		err := runSessionStopWith(exe, projectRoot, "OxAb12", time.Minute)
		require.ErrorIs(t, err, errSessionStopFailed)
		assert.Contains(t, err.Error(), "Error: not currently recording", "the stop's own message must reach the hook log")
	})
	t.Run("still uploading", func(t *testing.T) {
		if testing.Short() {
			t.Skip("short: waits about a second for the detached stop")
		}
		exe, _, done := fakeOxStop(t, uploaded, "1", 0)
		require.ErrorIs(t, runSessionStopWith(exe, projectRoot, "OxAb12", 50*time.Millisecond), errSessionStopStillRunning)
		require.Eventually(t, func() bool { _, err := os.Stat(done); return err == nil }, 10*time.Second, 20*time.Millisecond)
	})
}

// TestSessionStopCommand_FailedStopKeepsTheSignal: a stop that fails leaves
// the explicit-stop signal alone; it still owns the recording.
func TestSessionStopCommand_FailedStopKeepsTheSignal(t *testing.T) {
	projectRoot, _ := setupTestProject(t)
	t.Chdir(projectRoot)
	const agentID = "OxNotRecording"
	require.NoError(t, session.MarkExplicitStop(projectRoot, agentID))

	err := runAgentSessionStopCommand(stopCommandFlags(true), &agentinstance.Instance{AgentID: agentID})

	require.Error(t, err, "nothing is recording for this agent")
	assert.True(t, session.HasExplicitStop(projectRoot, agentID))
}

func TestStopDiagnosticsTail(t *testing.T) {
	assert.Equal(t, "Error: not currently recording Run ox agent prime",
		stopDiagnosticsTail([]byte("\x1b[31mError: not currently recording\x1b[0m\nRun ox agent prime\n")))
	assert.Equal(t, "", stopDiagnosticsTail(nil))
	long := stopDiagnosticsTail([]byte(strings.Repeat("x", 1000) + " the end"))
	assert.LessOrEqual(t, len(long), stopDiagnosticsMax+len("…"))
	assert.True(t, strings.HasSuffix(long, "the end"), "keep the end, where the error is")
}

// TestDeliverPendingWhispers_SkipsTheSessionEndStop: whispers that arrive
// while an orb thread is idle must still reach the agent on its next command.
// The SessionEnd stop writes its output to a file nobody shows the agent, so
// it must not read them (reading marks them delivered).
func TestDeliverPendingWhispers_SkipsTheSessionEndStop(t *testing.T) {
	prior := emitWhispersFn
	t.Cleanup(func() { emitWhispersFn = prior })
	for _, tc := range []struct {
		name       string
		agentEnded bool
		want       int
	}{
		{"an agent's own command delivers them", false, 1},
		{"the SessionEnd stop leaves them pending", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			emitWhispersFn = func(io.Writer, string) { calls++ }
			deliverPendingWhispers(stopCommandFlags(tc.agentEnded), "OxOrb1")
			assert.Equal(t, tc.want, calls)
		})
	}
}
