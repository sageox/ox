package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	oxruntime "github.com/sageox/ox/internal/runtime"
	"github.com/sageox/ox/internal/session"
)

// Customer promise (sageox/ox#1318): an Amp thread lands in the Ledger whole.
// Its session holds the prompt that started it, and a reply the coworker sends
// after the orb's idle end lands as a session continued from the first, with
// no turn in both.
//
// Red-first (verified while authoring): with the recording reading the sidecar
// from its start and dropping entries older than the recording, both tests
// fail on "the session must hold the prompt": ox-bridge.ts writes each prompt
// at agent.start, before `ox agent prime` starts the recording.

const (
	earlierPrompt  = "What does the billing service retry on?"
	followUpPrompt = "Now add a unit test for retryIdempotent"
	followUpReply  = "Added TestRetryIdempotent covering the three retryable status codes."
)

// newAmpThreadFixture is an Amp orb with the bridge installed and automatic
// recording on, before any prime.
func newAmpThreadFixture(t *testing.T) *orbSessionFixture {
	t.Helper()
	f := newOrbLedgerFixture(t)
	t.Setenv("AMP_ORB", "1")
	oxruntime.Reset()
	oxConfigSetRepo(t, "session_recording", "auto")
	// install-hooks' AGENTS.md block is what makes Amp record in hook mode
	require.NoError(t, os.WriteFile(filepath.Join(f.projectRoot, "AGENTS.md"),
		[]byte("<!-- ox:prime:amp:start -->\nox agent prime\n<!-- ox:prime:amp:end -->\n"), 0o644))
	t.Cleanup(func() { _ = DeleteSessionMarker(orbThreadURL) })
	return f
}

// prime starts the thread's recording the way `ox agent prime` does in an
// Amp orb, and keys the thread's session marker by its URL.
func (f *orbSessionFixture) prime(t *testing.T) *session.RecordingState {
	t.Helper()
	prior, _ := ReadSessionMarker(orbThreadURL)
	status := startSessionRecording(f.projectRoot, f.agentID, "amp", "", recordingSessionIDFromMarker(prior), orbThreadURL)
	require.NotNil(t, status)
	require.True(t, status.Recording, "prime must record the thread")
	state, err := session.LoadRecordingStateForAgent(f.projectRoot, f.agentID)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.NoError(t, WriteSessionMarker(&SessionMarker{
		AgentID: f.agentID, AgentSessionID: orbThreadURL, RecordingSessionID: state.SessionID, PrimedAt: time.Now(),
	}))
	return state
}

// uploadedSession is a raw.jsonl the LFS store received.
type uploadedSession struct {
	raw           string
	continuedFrom string
}

// uploaded returns every raw.jsonl the LFS store received, keyed by the
// session ID in its header. The store, not the remote's folders, because
// session folders are named to the minute: two sessions an agent starts
// within one minute, as these tests do, share a folder. In an orb the reply
// comes minutes after the first session started.
func (f *orbSessionFixture) uploaded(t *testing.T) map[string]uploadedSession {
	t.Helper()
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	sessions := map[string]uploadedSession{}
	for _, data := range f.store.objects {
		first, _, _ := strings.Cut(string(data), "\n")
		var header struct {
			Type     string `json:"type"`
			Metadata struct {
				SessionID     string `json:"session_id"`
				ContinuedFrom string `json:"continued_from_session_id"`
			} `json:"metadata"`
		}
		if json.Unmarshal([]byte(first), &header) != nil || header.Type != "header" {
			continue // not a raw.jsonl
		}
		sessions[header.Metadata.SessionID] = uploadedSession{raw: string(data), continuedFrom: header.Metadata.ContinuedFrom}
	}
	return sessions
}

func TestSessionEnd_AmpThreadKeepsThePromptThatStartedIt(t *testing.T) {
	f := newAmpThreadFixture(t)
	now := time.Now().UTC()
	f.appendSidecar(t,
		// a turn that finished before anything primed belongs to no session
		ampSidecarLine{Type: "user", Content: earlierPrompt, At: now.Add(-30 * time.Minute)},
		ampSidecarLine{Type: "assistant", Content: "It retries on 429, 502 and 503.", At: now.Add(-29 * time.Minute)},
		// ox-bridge.ts writes the prompt at agent.start; the model then primes
		ampSidecarLine{Type: "user", Content: orbPrompt, At: now.Add(-20 * time.Second)},
		ampSidecarLine{Type: "tool_use", Content: `{"cmd":"ox agent prime"}`, At: now.Add(-2 * time.Second)},
	)
	state := f.prime(t)
	f.appendSidecar(t, ampSidecarLine{Type: "assistant", Content: orbReply, At: time.Now().Add(time.Second)})

	f.sendSessionEnd(t)

	require.Len(t, remoteSessionDirs(t, f.barePath), 1, "the session must reach the Ledger")
	raw := f.uploaded(t)[state.SessionID].raw
	assert.Contains(t, raw, orbPrompt, "the session must hold the prompt that started the thread")
	assert.Contains(t, raw, orbReply)
	assert.NotContains(t, raw, earlierPrompt, "a turn from before the prime is not part of this session")
}

func TestSessionEnd_AmpReplyAfterIdleEndIsAContinuedSession(t *testing.T) {
	f := newAmpThreadFixture(t)
	now := time.Now().UTC()
	f.appendSidecar(t,
		ampSidecarLine{Type: "user", Content: orbPrompt, At: now.Add(-20 * time.Second)},
		ampSidecarLine{Type: "tool_use", Content: `{"cmd":"ox agent prime"}`, At: now.Add(-2 * time.Second)},
	)
	first := f.prime(t)
	f.appendSidecar(t, ampSidecarLine{Type: "assistant", Content: orbReply, At: time.Now().Add(time.Second)})
	f.sendSessionEnd(t) // the thread went idle: ox-bridge.ts ended its session

	// Devon replies in the same thread. ox-bridge.ts writes the prompt, then
	// primes quietly with the thread's URL.
	f.appendSidecar(t, ampSidecarLine{Type: "user", Content: followUpPrompt, At: time.Now()})
	second := f.prime(t)
	f.appendSidecar(t, ampSidecarLine{Type: "assistant", Content: followUpReply, At: time.Now().Add(time.Second)})
	f.sendSessionEnd(t)

	sessions := f.uploaded(t)
	require.Len(t, sessions, 2, "the reply lands as a session of its own")
	firstRaw, secondRaw := sessions[first.SessionID].raw, sessions[second.SessionID].raw
	assert.Contains(t, firstRaw, orbPrompt)
	assert.Contains(t, firstRaw, orbReply)
	assert.NotContains(t, firstRaw, followUpPrompt)
	assert.Contains(t, secondRaw, followUpPrompt, "the session must hold the prompt sent after the idle end")
	assert.Contains(t, secondRaw, followUpReply)
	assert.NotContains(t, secondRaw, orbPrompt, "no turn lands in both sessions")
	assert.NotContains(t, secondRaw, orbReply, "no turn lands in both sessions")
	assert.Equal(t, first.SessionID, sessions[second.SessionID].continuedFrom,
		"the reply's session is linked as continued from the first")

	// The daemon's tail watcher starts at the same offset, so a thread that
	// records in tail mode starts at the reply's prompt too.
	sidecar, err := os.ReadFile(f.sidecar)
	require.NoError(t, err)
	require.True(t, second.StartOffsetKnown)
	assert.True(t, strings.HasPrefix(string(sidecar[second.StartOffset:]), `{"content":"`+followUpPrompt),
		"the continued recording starts at the reply's prompt, got %q", sidecar[second.StartOffset:])
}
