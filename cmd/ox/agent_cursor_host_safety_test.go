package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The production wrapper still refuses to re-execute a test binary. These
// children exercise the real exec, bounded buffering, delivery and marker commit.
func TestCursorPrimeChildCommitsOnlyDeliveredContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("isolated child fixture uses a POSIX shell")
	}
	for _, tc := range []struct {
		name, script, agentID, wantError, wantOutput string
	}{
		{"success", "cat > child-input.json\nprintf '%s\\n' \"$AGENT_ENV\" \"$SAGEOX_AGENT_ID\" > child-env\nprintf 'DELIVERED_CONTEXT'\nprintf 'PRIVATE_STDERR' >&2\n", "Oxchild", "", "DELIVERED_CONTEXT"},
		{"child failure", "printf 'INCOMPLETE_CONTEXT'\nexit 7\n", "Oxchild", "hook-prime-failed", ""},
		{"output overflow", "dd if=/dev/zero bs=1048577 count=1 2>/dev/null\n", "Oxchild", "hook-prime-failed", ""},
		{"missing identity", "printf 'DELIVERED_CONTEXT'\n", "", "missing-native-identity", "DELIVERED_CONTEXT"},
		{"missing executable", "", "Oxchild", "hook-prime-failed", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCursorHostFixture(t)
			require.NoError(t, WriteSessionMarker(&SessionMarker{AgentID: tc.agentID, AgentSessionID: cursorConversationID, ParentPID: 12345}))
			child := filepath.Join(t.TempDir(), "isolated-ox")
			if tc.script != "" {
				script := "#!/bin/sh\n[ \"$*\" = 'agent prime --agent cursor' ] || exit 9\n" + tc.script
				require.NoError(t, os.WriteFile(child, []byte(script), 0o700))
			}
			input := f.input(t, "sessionStart", "")
			hook := &HookContext{ProjectRoot: f.root, Input: input}
			var primeErr error
			output := captureStdoutForPlanCLI(t, func() { primeErr = runCursorPrimeExecutableForHook(child, tc.agentID, hook) })
			assert.Equal(t, tc.wantOutput, output)
			marker, err := ReadSessionMarker(cursorConversationID)
			require.NoError(t, err)
			if tc.wantError != "" {
				require.ErrorContains(t, primeErr, tc.wantError)
				assert.False(t, marker.IsPrimed(), "unsuccessful delivery must remain retryable")
				assert.Equal(t, 12345, marker.ParentPID)
			} else {
				require.NoError(t, primeErr)
				assert.True(t, marker.IsPrimed())
				assert.Zero(t, marker.ParentPID, "Cursor must not retain the transient hook PID")
				stdin, err := os.ReadFile(filepath.Join(f.root, "child-input.json"))
				require.NoError(t, err)
				assert.Equal(t, input.RawBytes, stdin)
				env, err := os.ReadFile(filepath.Join(f.root, "child-env"))
				require.NoError(t, err)
				assert.Equal(t, "cursor\nOxchild\n", string(env))
			}
		})
	}
}

func TestCursorPrimeChildFailedDeliveryLeavesMarkerUnprimed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("isolated child fixture uses a POSIX shell")
	}
	f := newCursorHostFixture(t)
	require.NoError(t, WriteSessionMarker(&SessionMarker{AgentID: "Oxchild", AgentSessionID: cursorConversationID}))
	child := filepath.Join(t.TempDir(), "isolated-ox")
	require.NoError(t, os.WriteFile(child, []byte("#!/bin/sh\nprintf 'UNDELIVERABLE_CONTEXT'\n"), 0o700))
	closed, err := os.CreateTemp(t.TempDir(), "closed-output")
	require.NoError(t, err)
	require.NoError(t, closed.Close())
	stdout := os.Stdout
	os.Stdout = closed
	defer func() { os.Stdout = stdout }()
	err = runCursorPrimeExecutableForHook(child, "Oxchild", &HookContext{ProjectRoot: f.root, Input: f.input(t, "sessionStart", "")})
	os.Stdout = stdout
	require.ErrorIs(t, err, os.ErrClosed)
	marker, err := ReadSessionMarker(cursorConversationID)
	require.NoError(t, err)
	assert.False(t, marker.IsPrimed())
}

func TestCursorHostRefusesInvalidHooksBeforePriming(t *testing.T) {
	for _, scenario := range []string{"unsupported event", "missing payload", "event mismatch", "missing home", "invalid native ID", "corrupt marker", "missing generation", "incomplete startup export", "different workspace"} {
		t.Run(scenario, func(t *testing.T) {
			f := newCursorHostFixture(t)
			event := "sessionStart"
			input := f.input(t, event, "")
			want := ""
			switch scenario {
			case "unsupported event":
				event, want = "unknown", "unsupported-scope"
			case "missing payload":
				input, want = nil, "identity-conflict"
			case "event mismatch":
				input.HookEventName, want = "stop", "identity-conflict"
			case "missing home":
				t.Setenv("HOME", "")
				want = "source-unreadable"
			case "invalid native ID":
				input.RawBytes = []byte(strings.ReplaceAll(string(input.RawBytes), cursorConversationID, "not-a-conversation"))
				want = "missing-native-identity"
			case "corrupt marker":
				require.NoError(t, os.WriteFile(markerPath(cursorConversationID), []byte("{"), 0o600))
				want = "failed to parse marker"
			case "missing generation":
				event = "beforeSubmitPrompt"
				input = f.input(t, event, "")
				want = "boundary-unavailable"
			case "incomplete startup export":
				f.write(t, `{"role":"user"`)
				want = "boundary-unavailable"
			case "different workspace":
				_, err := UpdateSessionMarker(cursorConversationID, func(marker *SessionMarker) error {
					marker.CursorSourceBoundary = &CursorSourceBoundary{WorkspacePath: t.TempDir(), SourcePath: f.source}
					return nil
				})
				require.NoError(t, err)
				want = "workspace-mismatch"
			}
			primed := false
			err := runCursorAgentHookWithPrime(event, input, f.root, func(string, *HookContext) error { primed = true; return nil })
			require.ErrorContains(t, err, want)
			assert.False(t, primed)
			assert.False(t, session.IsRecording(f.root))
			assert.Zero(t, f.reader.calls.Load())
		})
	}
}

func TestCursorHostPrimeFailureAndTestBinaryGuardRemainRetryable(t *testing.T) {
	f := newCursorHostFixture(t)
	primeFailure := errors.New("child could not deliver context")
	err := runCursorAgentHookWithPrime("sessionStart", f.input(t, "sessionStart", ""), f.root, func(string, *HookContext) error { return primeFailure })
	require.ErrorIs(t, err, primeFailure)
	marker, err := ReadSessionMarker(cursorConversationID)
	require.NoError(t, err)
	assert.False(t, marker.IsPrimed())
	require.NotNil(t, marker.CursorSourceBoundary)
	// The public wrapper exercises selfexec.Path's test-binary safety guard.
	require.NoError(t, runCursorAgentHook("sessionStart", f.input(t, "sessionStart", ""), f.root))
	marker, err = ReadSessionMarker(cursorConversationID)
	require.NoError(t, err)
	assert.False(t, marker.IsPrimed())
	assert.False(t, session.IsRecording(f.root))
}

type cursorHostNonIncremental struct{ adapters.Adapter }

func TestCursorHostCaptureFailuresPreserveCheckpointAndReportStatus(t *testing.T) {
	for _, tc := range []struct{ name, status string }{
		{"pending source", "source-pending"}, {"changed source", "source-changed"},
		{"stopped", "stopped"}, {"missing adapter", "capture-deferred"},
		{"nonincremental adapter", "capture-deferred"}, {"active owner", "capture-owner-active"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCursorHostFixture(t)
			t.Setenv("SAGEOX_DAEMON", "false")
			state := f.record(t)
			switch tc.name {
			case "changed source":
				f.write(t, cursorHostUser)
				require.NoError(t, requestCursorCapture(f.root, state.AgentID, f.home))
				f.write(t, strings.ReplaceAll(cursorHostUser, "first prompt", "changed prompt"))
			case "stopped":
				require.NoError(t, session.MarkExplicitStop(f.root, state.AgentID))
			case "missing adapter":
				adapters.Unregister("cursor")
			case "nonincremental adapter":
				adapters.Unregister("cursor")
				adapters.Register(&cursorHostNonIncremental{Adapter: f.reader})
			case "active owner":
				locked, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
				go func() {
					done <- fileutil.WithFileLock(context.Background(), filepath.Join(state.SessionPath, "raw.jsonl"), func() error {
						close(locked)
						<-release
						return nil
					})
				}()
				<-locked
				t.Cleanup(func() { close(release); require.NoError(t, <-done) })
			}
			before, err := session.LoadRecordingStateForAgent(f.root, state.AgentID)
			require.NoError(t, err)
			output := captureStdoutForPlanCLI(t, func() {
				require.NoError(t, runCursorAgentHookWithPrime("afterAgentResponse", f.input(t, "afterAgentResponse", "g1"), f.root, func(string, *HookContext) error {
					t.Fatal("completed prime must not repeat")
					return nil
				}))
			})
			assert.Empty(t, output)
			after, err := session.LoadRecordingStateForAgent(f.root, state.AgentID)
			require.NoError(t, err)
			assert.Equal(t, tc.status, after.LastHookStatus)
			assert.NotNil(t, after.LastHookAt)
			assert.Equal(t, before.SourceOffset, after.SourceOffset)
			assert.Equal(t, before.EntryCount, after.EntryCount)
			assert.Equal(t, before.SourcePrefixSHA256, after.SourcePrefixSHA256)
		})
	}
}

func TestCursorHostSessionEndRetainsPendingFinalDrain(t *testing.T) {
	f := newCursorHostFixture(t)
	t.Setenv("SAGEOX_DAEMON", "false")
	state := f.record(t)
	for i := 0; i < 2; i++ {
		require.NoError(t, runCursorAgentHook("sessionEnd", f.input(t, "sessionEnd", "g1"), f.root))
		current, err := session.LoadRecordingStateForAgent(f.root, state.AgentID)
		require.NoError(t, err)
		require.NotNil(t, current)
		require.NotNil(t, current.StoppedAt)
		assert.True(t, current.CursorFinalDrainPending)
		assert.Equal(t, "source-pending", current.LastHookStatus)
		if state.StoppedAt != nil {
			assert.Equal(t, *state.StoppedAt, *current.StoppedAt)
		}
		state = current
	}
	assert.Zero(t, f.reader.calls.Load())
}

func TestCursorHostRefusesRecordingOwnedByAnotherAdapter(t *testing.T) {
	f := newCursorHostFixture(t)
	state := f.record(t)
	require.NoError(t, session.UpdateRecordingStateForAgent(f.root, state.AgentID, func(current *session.RecordingState) { current.AdapterName = "generic" }))
	require.ErrorContains(t, requestCursorCapture(f.root, state.AgentID, f.home), "identity-conflict")
	for _, event := range []string{"afterAgentResponse", "beforeSubmitPrompt"} {
		err := runCursorAgentHookWithPrime(event, f.input(t, event, "g1"), f.root, func(string, *HookContext) error { t.Fatal("foreign recording must not prime"); return nil })
		require.ErrorContains(t, err, "identity-conflict")
	}
	assert.Zero(t, f.reader.calls.Load())
}

func TestCursorHostIgnoresNondeliverableEventWithoutIdentity(t *testing.T) {
	f := newCursorHostFixture(t)
	for _, event := range []string{"afterAgentResponse", "stop", "sessionEnd"} {
		require.NoError(t, runCursorAgentHook(event, f.input(t, event, "g1"), f.root))
	}
	assert.False(t, session.IsRecording(f.root))
	assert.Zero(t, f.reader.calls.Load())
}
