//go:build !short

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/session/pipeline"
	"github.com/stretchr/testify/require"
)

const pausedPublicationContent = "private conversation during pause"

// Deliberately interleave carrier footers and use unrelated native seq values:
// lifecycle boundaries count entries, not records or native sequence numbers.
const pausedPublicationRaw = `{"type":"header","metadata":{"agent_id":"OxMask","model":"test-model"}}
{"type":"user","content":"public before pause","seq":9007199254740993,"native":{"keep":true}}
{"type":"footer","native_sessions":[{"id":"native-session","agent_type":"claude-code"}]}
{"type":"assistant","content":"private conversation during pause"}
{"type":"user","content":"public after resume"}
{"type":"footer","entry_count":3,"exit_reason":"stopped"}
`

func publicationPauseLifecycle(resume bool) []session.LifecycleEvent {
	lifecycle := []session.LifecycleEvent{
		{Action: session.LifecycleActionStart, Seq: 0},
		{Action: session.LifecycleActionPause, Seq: 1},
	}
	if resume {
		lifecycle = append(lifecycle, session.LifecycleEvent{Action: session.LifecycleActionResume, Seq: 2})
	}
	return lifecycle
}

func requirePublicationMask(t *testing.T, path string, resume bool) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(data), pausedPublicationContent)
	require.Contains(t, string(data), "public before pause")
	if resume {
		require.Contains(t, string(data), "public after resume")
	} else {
		require.NotContains(t, string(data), "public after resume")
	}
}

func TestIncrementalPublicationMasksPausedRawAndArtifacts(t *testing.T) {
	for _, resume := range []bool{false, true} {
		name := "pause then stop"
		if resume {
			name = "pause then resume"
		}
		t.Run(name, func(t *testing.T) {
			root := setupIncrementalTest(t)
			state := startTestRecording(t, root, "OxMask", "claude-code")
			state.EntryCount = 3
			state.Lifecycle = publicationPauseLifecycle(resume)
			rawPath := filepath.Join(state.SessionPath, ledgerFileRaw)
			require.NoError(t, os.WriteFile(rawPath, []byte(pausedPublicationRaw), 0600))
			adapter, err := adapters.GetAdapter("claude-code")
			require.NoError(t, err)
			result, err := finalizeIncrementalSession(root, state, rawPath, adapter, &agentSessionResult{})
			require.NoError(t, err)
			want := 1
			if resume {
				want = 2
			}
			require.Equal(t, want, result.EntryCount)
			requirePublicationMask(t, result.RawPath, resume)
			requirePublicationMask(t, result.SessionMDPath, resume)
			for _, path := range []string{result.SummaryMDPath, filepath.Join(state.SessionPath, "summary.json")} {
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				require.NotContains(t, string(data), pausedPublicationContent)
			}
			// Exercise the actual cache-to-ledger boundary used before LFS upload.
			ledger := t.TempDir()
			require.NoError(t, pipeline.CopySessionToLedger(pipeline.OSFileSystem{}, result, ledger, result.SessionName))
			requirePublicationMask(t, filepath.Join(ledger, "sessions", result.SessionName, ledgerFileRaw), resume)
			requirePublicationMask(t, filepath.Join(ledger, "sessions", result.SessionName, ledgerFileSessionMD), resume)
			stored, err := session.ReadSessionFromPath(result.RawPath)
			require.NoError(t, err)
			require.Equal(t, float64(want), stored.Footer["entry_count"])
			require.Equal(t, "stopped", stored.Footer["exit_reason"])
			data, err := os.ReadFile(result.RawPath)
			require.NoError(t, err)
			require.Contains(t, string(data), "9007199254740993")
			require.Contains(t, string(data), `"native":{"keep":true}`)
		})
	}
}

func TestCachedPublicationMasksPausedRawAndPrompt(t *testing.T) {
	for _, resume := range []bool{false, true} {
		name := "pause then recovery"
		if resume {
			name = "resume then recovery"
		}
		t.Run(name, func(t *testing.T) {
			f := newRecoverCollisionFixture(t, collisionRecoveredID, nil)
			state, err := session.LoadRecordingStateForAgent(f.projectRoot, "OxRcvr")
			require.NoError(t, err)
			state.EntryCount = 3
			state.Lifecycle = publicationPauseLifecycle(resume)
			rawPath := filepath.Join(f.sessionPath, ledgerFileRaw)
			require.NoError(t, os.WriteFile(rawPath, []byte(pausedPublicationRaw), 0600))
			output, err := publishCachedRecording(&agentinstance.Instance{AgentID: "OxRcvr"}, f.projectRoot, state, rawPath)
			require.NoError(t, err)
			require.NotContains(t, output.SummaryPrompt, pausedPublicationContent)
			requirePublicationMask(t, filepath.Join(f.ledgerSessionDir, ledgerFileRaw), resume)
			// The summary prompt's local raw path must also be masked, not just
			// the preview entries embedded in the returned prompt.
			requirePublicationMask(t, output.RawPath, resume)
		})
	}
}

func TestPublicationMaskFailsClosedAndPreservesCache(t *testing.T) {
	for _, mode := range []string{"malformed JSONL", "negative sequence", "backwards sequence", "out of bounds", "count mismatch"} {
		t.Run(mode, func(t *testing.T) {
			f := newRecoverCollisionFixture(t, collisionRecoveredID, nil)
			state, err := session.LoadRecordingStateForAgent(f.projectRoot, "OxRcvr")
			require.NoError(t, err)
			state.EntryCount = 3
			state.Lifecycle = publicationPauseLifecycle(true)
			raw := pausedPublicationRaw
			switch mode {
			case "malformed JSONL":
				raw = strings.Replace(raw, `{"type":"assistant"`, `{invalid "type":"assistant"`, 1)
			case "negative sequence":
				state.Lifecycle[1].Seq = -1
			case "backwards sequence":
				state.Lifecycle[2].Seq = 0
			case "out of bounds":
				state.Lifecycle[2].Seq = 4
			case "count mismatch":
				state.EntryCount = 4
			}
			rawPath := filepath.Join(f.sessionPath, ledgerFileRaw)
			require.NoError(t, os.WriteFile(rawPath, []byte(raw), 0600))
			output, err := publishCachedRecording(&agentinstance.Instance{AgentID: "OxRcvr"}, f.projectRoot, state, rawPath)
			require.Error(t, err)
			require.Nil(t, output)
			require.NotContains(t, err.Error(), pausedPublicationContent)
			data, err := os.ReadFile(rawPath)
			require.NoError(t, err)
			require.Equal(t, raw, string(data))
			require.NoFileExists(t, filepath.Join(f.ledgerSessionDir, ledgerFileRaw))
			survivor, err := session.LoadRecordingStateForAgent(f.projectRoot, "OxRcvr")
			require.NoError(t, err)
			require.NotNil(t, survivor)
			// Repair the local evidence and retry the same publication policy.
			state.EntryCount = 3
			state.Lifecycle = publicationPauseLifecycle(true)
			require.NoError(t, os.WriteFile(rawPath, []byte(pausedPublicationRaw), 0600))
			require.NoError(t, maskCachedRecordingForPublication(rawPath, f.projectRoot, state))
			requirePublicationMask(t, rawPath, true)
		})
	}
}

func TestIncrementalPublicationMaskRejectsMalformedCache(t *testing.T) {
	root := setupIncrementalTest(t)
	state := startTestRecording(t, root, "OxMask", "claude-code")
	state.EntryCount = 3
	state.Lifecycle = publicationPauseLifecycle(true)
	rawPath := filepath.Join(state.SessionPath, ledgerFileRaw)
	raw := pausedPublicationRaw + "{invalid trailing entry\n"
	require.NoError(t, os.WriteFile(rawPath, []byte(raw), 0600))
	adapter, err := adapters.GetAdapter("claude-code")
	require.NoError(t, err)
	result, err := finalizeIncrementalSession(root, state, rawPath, adapter, &agentSessionResult{})
	require.Error(t, err)
	require.Nil(t, result)
	data, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	require.Equal(t, raw, string(data))
	require.NoFileExists(t, filepath.Join(state.SessionPath, ledgerFileSessionMD))
}

func TestPublicationMaskAllPausedAndShortenedCacheFailsClosed(t *testing.T) {
	root := setupIncrementalTest(t)
	state := startTestRecording(t, root, "OxMask", "claude-code")
	state.EntryCount = 3
	state.Lifecycle = []session.LifecycleEvent{
		{Action: session.LifecycleActionPause, Seq: 0},
		{Action: session.LifecycleActionStop, Seq: 3},
	}
	rawPath := filepath.Join(state.SessionPath, ledgerFileRaw)
	require.NoError(t, os.WriteFile(rawPath, []byte(pausedPublicationRaw), 0600))
	result, err := processAgentSession(root, state)
	require.NoError(t, err)
	require.Zero(t, result.EntryCount)
	stored, err := session.ReadSessionFromPath(result.RawPath)
	require.NoError(t, err)
	require.Empty(t, stored.Entries)
	require.Equal(t, float64(0), stored.Footer["entry_count"])

	// A checkpoint still referring to the original sequence is not authority
	// to reinterpret a shortened cache, or fall back to the native source.
	result, err = processAgentSession(root, state)
	require.ErrorContains(t, err, "cached entry count does not match lifecycle checkpoint")
	require.Nil(t, result)
}
