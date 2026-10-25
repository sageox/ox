package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/sessionid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. Identity at birth ---

// TestStartRecording_MintsSessionIDAtBirth verifies the durable ses_<UUIDv7>
// exists from t=0 and survives the disk round-trip.
// Failure prevented: conversation URLs (/c/<ses_id>) circulated during a live
// session (commit trailers, PR bodies) pointing at an ID that never existed
// because it was only minted at stop.
func TestStartRecording_MintsSessionIDAtBirth(t *testing.T) {
	cacheDir := t.TempDir()
	projectRoot := setupRecordingTest(t, cacheDir)

	sessionFile := filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(sessionFile, []byte("{}\n"), 0644))

	state, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID:     "OxSeS1",
		AdapterName: "claude-code",
		SessionFile: sessionFile,
		Username:    "testuser",
	})
	require.NoError(t, err)
	require.NotNil(t, state)

	assert.True(t, sessionid.IsValidSessionID(state.SessionID),
		"StartRecording must mint a valid ses_ ID at birth, got %q", state.SessionID)

	// disk round-trip: the ID every later reader (hook, prime, stop) sees is
	// the exact minted value, never regenerated
	reloaded, err := LoadRecordingStateForAgent(projectRoot, "OxSeS1")
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	assert.Equal(t, state.SessionID, reloaded.SessionID)
}

func TestStartRecording_ContinuationIdentity(t *testing.T) {
	cacheDir := t.TempDir()
	projectRoot := setupRecordingTest(t, cacheDir)

	validPrior := sessionid.GenerateSessionID()
	state, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID:                "OxCont",
		AdapterName:            "claude-code",
		Username:               "testuser",
		ContinuedFromSessionID: validPrior,
	})
	require.NoError(t, err)
	assert.Equal(t, validPrior, state.ContinuedFromSessionID)

	_, err = StopRecording(projectRoot, state.AgentID)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(state.SessionPath))

	invalid, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID:                "OxCont",
		AdapterName:            "claude-code",
		Username:               "testuser",
		ContinuedFromSessionID: "not-a-session-id",
	})
	require.NoError(t, err)
	assert.Empty(t, invalid.ContinuedFromSessionID, "untrusted marker values must not create cross-links")
}

// TestStartRecording_EveryRecordingGetsItsOwnID verifies the other half of
// the birth contract: the ID is minted PER RECORDING, never shared or carried
// over. TestStartRecording_MintsSessionIDAtBirth only proves one recording
// gets a valid ID — a StartRecording that returned a package-level constant,
// or reused whatever the previous recording left on disk, would still pass it.
//
// Failure prevented: two distinct recordings resolving to one /c/<ses_id>
// conversation. Every consumer treats the ID as a unique key — the server-side
// dedup key, the SageOx-Session commit trailer, plan provenance — so a
// collision silently merges two unrelated sessions rather than erroring.
func TestStartRecording_EveryRecordingGetsItsOwnID(t *testing.T) {
	cacheDir := t.TempDir()
	projectRoot := setupRecordingTest(t, cacheDir)

	sessionFile := filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(sessionFile, []byte("{}\n"), 0644))

	// The repeated OxSaMe entries are the case that matters: a single agent
	// recording, stopping, and recording again. Session directory names are
	// minute-granular and include the agent ID, so consecutive starts by one
	// agent can land on the SAME directory name — the shape most likely to
	// carry an identity forward. (Two concurrent recordings for one agent are
	// already impossible; StartRecording rejects that outright.)
	seen := make(map[string]string)
	for _, agentID := range []string{"OxSaMe", "OxOthr", "OxSaMe", "OxSaMe"} {
		state, err := StartRecording(projectRoot, StartRecordingOptions{
			AgentID:     agentID,
			AdapterName: "claude-code",
			SessionFile: sessionFile,
			Username:    "testuser",
		})
		require.NoError(t, err)
		require.NotNil(t, state)
		require.True(t, sessionid.IsValidSessionID(state.SessionID),
			"every recording must mint a valid ses_ ID, got %q", state.SessionID)

		prior, dup := seen[state.SessionID]
		require.False(t, dup,
			"recording for agent %s reused the ID minted for agent %s: %s",
			agentID, prior, state.SessionID)
		seen[state.SessionID] = agentID

		_, err = StopRecording(projectRoot, agentID)
		require.NoError(t, err)
	}
	assert.Len(t, seen, 4, "each StartRecording must contribute a distinct ID")
}

// TestRecordingState_LegacyJSONWithoutSessionID verifies recordings started
// under an older binary (no session_id field) load with an empty ID so every
// emitter falls back to the name-based URL instead of crashing or inventing
// an identity.
// Failure prevented: mid-upgrade recordings break the commit hook or get a
// fabricated /c/ link that resolves to nothing.
func TestRecordingState_LegacyJSONWithoutSessionID(t *testing.T) {
	projectRoot := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	sessionsDir := filepath.Join(projectRoot, "sessions", "2026-01-01T00-00-user-OxLEG1")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))
	legacy := `{"agent_id":"OxLEG1","started_at":"2026-01-01T00:00:00Z","session_path":"` + sessionsDir + `","workspace_path":"` + projectRoot + `"}`
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, ".recording.json"), []byte(legacy), 0o644))

	state, err := LoadRecordingStateForAgent(projectRoot, "OxLEG1")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Empty(t, state.SessionID, "legacy recording must load with empty SessionID (fallback to name URL)")
}

// --- B. Raw-header crash-safe carrier ---

// TestParseStoreMeta_SessionIDKeyDisambiguation verifies the overloaded
// "session_id" header key parses correctly in BOTH directions: ox headers
// carry a ses_-prefixed recording ID; the alternative format (e.g. manual
// session capture, third-party raws) uses the same key as an AGENT
// identifier.
// Failure prevented: a daemon orphan-finalize misreading an agent id as the
// recording identity (dangling /c/ links), or an alternative-format agent id
// vanishing because it was mistaken for a recording ID.
// TestResolveSessionID_Precedence pins the single source of truth for the
// finalize-time ID choice shared by stop, recover, and daemon finalize.
// Failure prevented: any of the three paths drifting to a different
// precedence (e.g. start-minted overriding a preserved republish ID),
// rotating the identity out from under /c/ links already in git history.
func TestResolveSessionID_Precedence(t *testing.T) {
	tests := []struct {
		name        string
		preserved   string
		startMinted string
		want        string
	}{
		{"preserved wins over start-minted", "ses_prior", "ses_birth", "ses_prior"},
		{"start-minted stands when nothing preserved", "", "ses_birth", "ses_birth"},
		{"preserved alone", "ses_prior", "", "ses_prior"},
		{"neither: caller mints fresh", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ResolveSessionID(tt.preserved, tt.startMinted))
		})
	}
}

// TestReadHeaderSessionID covers the crash-safe-carrier read used by recover
// and abort-by-name when .recording.json is already gone.
// Failure prevented: a recover/abort of a crashed session failing to find the
// start-minted ID (fresh re-mint → dangling /c/ links) or misreading a
// foreign-format agent id as a recording identity.
func TestReadHeaderSessionID(t *testing.T) {
	sesID := "ses_01890a5d-ac96-774b-bcce-b302099a8057"

	writeRaw := func(t *testing.T, firstLine string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "raw.jsonl")
		require.NoError(t, os.WriteFile(path, []byte(firstLine+"\n{\"type\":\"entry\"}\n"), 0o644))
		return path
	}

	t.Run("ox header format carries the id", func(t *testing.T) {
		path := writeRaw(t, `{"type":"header","metadata":{"version":"1.0","agent_id":"OxA1b2","session_id":"`+sesID+`"}}`)
		assert.Equal(t, sesID, ReadHeaderSessionID(path))
	})
	t.Run("_meta header format carries the id", func(t *testing.T) {
		path := writeRaw(t, `{"_meta":{"schema_version":"1","agent_id":"OxA1b2","session_id":"`+sesID+`"}}`)
		assert.Equal(t, sesID, ReadHeaderSessionID(path))
	})
	t.Run("foreign _meta agent-id style yields empty, never an agent id", func(t *testing.T) {
		path := writeRaw(t, `{"_meta":{"schema_version":"1","session_id":"manual"}}`)
		assert.Empty(t, ReadHeaderSessionID(path))
	})
	t.Run("legacy header without session_id yields empty", func(t *testing.T) {
		path := writeRaw(t, `{"type":"header","metadata":{"version":"1.0","agent_id":"OxA1b2"}}`)
		assert.Empty(t, ReadHeaderSessionID(path))
	})
	t.Run("first line not a header yields empty", func(t *testing.T) {
		path := writeRaw(t, `{"type":"entry","content":"hi"}`)
		assert.Empty(t, ReadHeaderSessionID(path))
	})
	t.Run("missing file yields empty", func(t *testing.T) {
		assert.Empty(t, ReadHeaderSessionID(filepath.Join(t.TempDir(), "nope.jsonl")))
	})
}

func TestParseStoreMeta_SessionIDKeyDisambiguation(t *testing.T) {
	sesID := "ses_01890a5d-ac96-774b-bcce-b302099a8057"

	tests := []struct {
		name          string
		header        map[string]any
		wantAgentID   string
		wantSessionID string
	}{
		{
			name:          "ox header carries both agent_id and ses_ recording id",
			header:        map[string]any{"version": "1.0", "agent_id": "OxA1b2", "session_id": sesID},
			wantAgentID:   "OxA1b2",
			wantSessionID: sesID,
		},
		{
			name:          "alternative format session_id is an agent identifier",
			header:        map[string]any{"schema_version": "1", "session_id": "manual"},
			wantAgentID:   "manual",
			wantSessionID: "",
		},
		{
			name:          "ses_-valued session_id without agent_id is a recording id, not an agent",
			header:        map[string]any{"version": "1.0", "session_id": sesID},
			wantAgentID:   "",
			wantSessionID: sesID,
		},
		{
			name:          "legacy ox header without session_id",
			header:        map[string]any{"version": "1.0", "agent_id": "OxA1b2"},
			wantAgentID:   "OxA1b2",
			wantSessionID: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta := ParseStoreMeta(tt.header)
			require.NotNil(t, meta)
			assert.Equal(t, tt.wantAgentID, meta.AgentID)
			assert.Equal(t, tt.wantSessionID, meta.SessionID)
		})
	}
}
