package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func cursorAutoRecordingFixture(t *testing.T) *cursorHostFixture {
	t.Helper()
	f := newCursorHostFixture(t)
	t.Setenv("SAGEOX_DAEMON", "false")
	t.Setenv("OX_SESSION_RECORDING", "auto")
	t.Setenv(envClearNotice, "")
	path, err := ledger.DefaultPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(path, ".git"), 0o700))
	return f
}

func cursorPendingPrimeBoundary(f *cursorHostFixture) *CursorSourceBoundary {
	return &CursorSourceBoundary{
		WorkspacePath: f.root, SourcePath: f.source, SourcePending: true,
		KnownZero: true, SourcePrefixSHA256: cursorEmptyPrefixSHA256, GenerationID: "generation-one",
	}
}

func TestCursorPrimeAutoStartNeedsIdentityAndBoundary(t *testing.T) {
	f := cursorAutoRecordingFixture(t)
	missingIdentity := startSessionRecording(f.root, "Oxprime", "cursor", "", "", "")
	require.NotNil(t, missingIdentity)
	require.False(t, missingIdentity.Recording)
	require.Contains(t, missingIdentity.UserNotification, "native conversation identity")
	missingBoundary := startSessionRecording(f.root, "Oxprime", "cursor", "", "", cursorConversationID)
	require.NotNil(t, missingBoundary)
	require.False(t, missingBoundary.Recording)
	require.Contains(t, missingBoundary.UserNotification, "waiting for the next prompt")
	state, err := session.LoadRecordingStateForAgent(f.root, "Oxprime")
	require.NoError(t, err)
	require.Nil(t, state)
}

func TestCursorPrimeBindsOnlyAvailableSourceForPendingRecording(t *testing.T) {
	for _, available := range []bool{false, true} {
		t.Run(map[bool]string{false: "export still missing", true: "export appeared"}[available], func(t *testing.T) {
			f := cursorAutoRecordingFixture(t)
			original := f.record(t)
			boundary := cursorPendingPrimeBoundary(f)
			boundary.SourcePending = false
			if available {
				f.write(t, cursorHostUser)
			}
			status := startSessionRecording(f.root, original.AgentID, "cursor", "", "", cursorConversationID, boundary)
			require.NotNil(t, status)
			require.True(t, status.Recording)
			state, err := session.LoadRecordingStateForAgent(f.root, original.AgentID)
			require.NoError(t, err)
			require.Equal(t, original.SessionID, state.SessionID)
			require.Zero(t, state.SourceOffset, "binding must preserve the saved pre-prompt boundary")
			if available {
				require.Equal(t, f.source, state.SessionFile)
			} else {
				require.Empty(t, state.SessionFile, "failed binding must preserve the pending recording")
			}
		})
	}
}

func TestCursorPrimeInheritedPauseIsDurableBeforeCapture(t *testing.T) {
	for _, fromClear := range []bool{false, true} {
		t.Run(map[bool]string{false: "restart", true: "clear"}[fromClear], func(t *testing.T) {
			f := cursorAutoRecordingFixture(t)
			require.NoError(t, session.MarkExplicitPause(f.root, "Oxprime", 7))
			if fromClear {
				t.Setenv(envClearNotice, `{"session_name":"prior-paused-session","was_suspended":true}`)
			}
			status := startSessionRecording(f.root, "Oxprime", "cursor", "", "", cursorConversationID, cursorPendingPrimeBoundary(f))
			require.NotNil(t, status)
			require.True(t, status.AutoStarted)
			state, err := session.LoadRecordingStateForAgent(f.root, "Oxprime")
			require.NoError(t, err)
			require.NotNil(t, state)
			require.NotNil(t, state.SuspendedAt)
			require.True(t, state.InheritedPause)
			require.Equal(t, 1, state.PauseCount)
			require.Len(t, state.Lifecycle, 1)
			require.Equal(t, session.LifecycleActionPause, state.Lifecycle[0].Action)
			require.Zero(t, state.Lifecycle[0].Seq)
			if fromClear {
				require.Equal(t, "prior-paused-session", state.InheritedFromSession)
				require.Equal(t, "inherited-from-clear", state.Lifecycle[0].Reason)
			} else {
				require.Empty(t, state.InheritedFromSession)
				require.Equal(t, "inherited", state.Lifecycle[0].Reason)
			}
			_, _, paused := session.PeekExplicitPause(f.root, "Oxprime")
			require.True(t, paused, "automatic startup must preserve an explicit pause")
			raw, err := session.ReadSessionFromPath(filepath.Join(state.SessionPath, "raw.jsonl"))
			require.NoError(t, err)
			require.Empty(t, raw.Entries)
		})
	}
}

func TestCursorPrimeRejectsUnsafeOrChangedStartupBoundary(t *testing.T) {
	for _, failure := range []string{"different source", "changed source prefix", "startup lock unavailable"} {
		t.Run(failure, func(t *testing.T) {
			f := cursorAutoRecordingFixture(t)
			boundary := cursorPendingPrimeBoundary(f)
			switch failure {
			case "different source":
				boundary.SourcePath = filepath.Join(t.TempDir(), "other.jsonl")
			case "changed source prefix":
				f.write(t, cursorHostUser)
				boundary.SourcePending = false
				boundary.SourcePrefixSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			case "startup lock unavailable":
				lock := fileutil.LockPath(markerPath(cursorConversationID) + ".recording-start")
				require.NoError(t, os.MkdirAll(lock, 0o700))
				t.Cleanup(func() { _ = os.Remove(lock) })
			}
			require.Nil(t, startSessionRecording(f.root, "Oxprime", "cursor", "", "", cursorConversationID, boundary))
			state, err := session.LoadRecordingStateForAgent(f.root, "Oxprime")
			require.NoError(t, err)
			require.Nil(t, state, "a refused startup must not publish a recording marker")
		})
	}
}

func TestCursorPrimeWatchNotificationRejectsInvalidSource(t *testing.T) {
	f := newCursorHostFixture(t)
	state := f.record(t)
	state.SourcePrefixSHA256 = "invalid"
	sendSessionWatchStart(state, f.root)
	t.Setenv("HOME", "")
	sendSessionWatchStart(state, f.root)
	reloaded, err := session.LoadRecordingStateForAgent(f.root, state.AgentID)
	require.NoError(t, err)
	require.Equal(t, cursorEmptyPrefixSHA256, reloaded.SourcePrefixSHA256)
}

func cursorPrimeSafetyCommand() (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	cmd.Flags().String("agent", "cursor", "")
	cmd.Flags().String("format", "json", "")
	output := &bytes.Buffer{}
	cmd.SetOut(output)
	cmd.SetErr(output)
	return cmd, output
}

func TestCursorPrimeValidatesNativeHookBeforeContextWork(t *testing.T) {
	if testing.Short() {
		t.Skip("short: exercises six complete prime invocations")
	}
	for _, failure := range []string{"malformed identity", "missing generation", "missing home", "corrupt startup marker", "corrupt identity marker", "before prompt"} {
		t.Run(failure, func(t *testing.T) {
			f, _ := newCursorManualStartFixture(t)
			t.Setenv("FEATURE_AUTH", "true")
			t.Setenv("SAGEOX_TOKEN", "")
			event := "sessionStart"
			switch failure {
			case "before prompt", "missing generation":
				event = "beforeSubmitPrompt"
			case "corrupt identity marker":
				event = "postToolUse"
			}
			input := f.input(t, event, "generation-one")
			want := ""
			switch failure {
			case "malformed identity":
				input.RawBytes = []byte(`{"session_id":"native","conversation_id":"wrong"}`)
				want = "missing-native-identity"
			case "missing generation":
				input = f.input(t, event, "")
				want = "generation is required"
			case "missing home":
				t.Setenv("HOME", "")
				want = "resolve Cursor home directory"
			case "corrupt startup marker", "corrupt identity marker":
				require.NoError(t, os.WriteFile(markerPath(cursorConversationID), []byte("{"), 0o600))
				want = "persist Cursor"
			}
			cmd, output := cursorPrimeSafetyCommand()
			withStdin(t, string(input.RawBytes), func() {
				err := runAgentPrime(cmd, nil)
				if want == "" {
					require.NoError(t, err)
				} else {
					require.ErrorContains(t, err, want)
				}
			})
			require.Empty(t, output.String(), "invalid inputs and prompt-boundary handoffs must not deliver prime context")
			if failure == "before prompt" {
				marker, err := ReadSessionMarker(cursorConversationID)
				require.NoError(t, err)
				require.NotNil(t, marker.CursorSourceBoundary)
				require.Empty(t, marker.AgentID)
				require.False(t, marker.IsPrimed())
			}
		})
	}
}

func TestCursorPrimeDegradedAuthenticationKeepsReservedIdentityUnprimed(t *testing.T) {
	f, _ := newCursorManualStartFixture(t)
	t.Setenv("FEATURE_AUTH", "true")
	t.Setenv("SAGEOX_TOKEN", "")
	cmd, output := cursorPrimeSafetyCommand()
	withStdin(t, string(f.input(t, "sessionStart", "generation-one").RawBytes), func() {
		require.NoError(t, runAgentPrime(cmd, nil))
	})
	var result agentPrimeOutput
	require.NoError(t, json.Unmarshal(output.Bytes(), &result))
	require.Equal(t, "degraded", result.Status)
	marker, err := ReadSessionMarker(cursorConversationID)
	require.NoError(t, err)
	require.NotEmpty(t, marker.AgentID)
	require.Equal(t, result.AgentID, marker.AgentID)
	require.Zero(t, marker.ParentPID)
	require.False(t, marker.IsPrimed(), "only the successful hook parent confirms delivery")
	require.NotNil(t, marker.CursorSourceBoundary)
}
