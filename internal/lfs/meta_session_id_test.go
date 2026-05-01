package lfs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. SessionID round-trip and omitempty wire format ---

// TestSessionMeta_SessionIDPersisted writes a meta.json with a SessionID and
// reads it back, asserting the value survives the JSON round-trip.
// Failure prevented: a future refactor that drops the JSON tag or struct
// field would silently lose the per-recording identifier without any
// existing meta_test.go assertion failing.
func TestSessionMeta_SessionIDPersisted(t *testing.T) {
	tmpDir := t.TempDir()
	sessionDir := filepath.Join(tmpDir, "2026-05-01T10-00-ryan-Oxabcd")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	const wantID = "ses_01950000-0000-7abc-8def-0123456789ab"
	meta := NewSessionMeta("2026-05-01T10-00-ryan-Oxabcd", "ryan", "Oxabcd", "claude-code", time.Now()).
		SessionID(wantID).
		Build()

	require.NoError(t, WriteSessionMetaOnly(sessionDir, meta))

	got, err := ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.Equal(t, wantID, got.SessionID)
}

// TestSessionMeta_SessionIDOmitemptyForLegacy asserts that meta.json files
// produced before SessionID existed unmarshal cleanly with SessionID == "",
// and that empty SessionID is omitted on marshal.
// Failure prevented: dropping `omitempty` would write empty session_id
// fields into every legacy meta.json on rewrite, which would (a) be
// distinguishable from genuinely-pre-rollout files and (b) clutter the
// JSON with empty strings.
func TestSessionMeta_SessionIDOmitemptyForLegacy(t *testing.T) {
	// legacy meta.json that predates the field
	legacy := `{
  "version": "1.0",
  "session_name": "2025-12-01T08-00-ryan-OxLegc",
  "username": "ryan",
  "agent_id": "OxLegc",
  "agent_type": "claude-code",
  "created_at": "2025-12-01T08:00:00Z",
  "files": {}
}`

	var got SessionMeta
	require.NoError(t, json.Unmarshal([]byte(legacy), &got))
	assert.Equal(t, "", got.SessionID, "legacy meta.json should unmarshal with empty SessionID")

	// re-marshal: empty SessionID must NOT appear in the output
	out, err := json.Marshal(&got)
	require.NoError(t, err)
	assert.NotContains(t, string(out), `"session_id"`, "empty SessionID must be omitted on marshal")
}

// TestSessionMeta_SessionIDPreservedAcrossMutate verifies that a no-op
// MutateSessionMeta does not touch SessionID.
// Failure prevented: a refactor that switches the mutate path to a "build
// fresh from JSONL header" approach would silently regenerate SessionID
// per finalize, breaking dedup across machines.
func TestSessionMeta_SessionIDPreservedAcrossMutate(t *testing.T) {
	tmpDir := t.TempDir()
	sessionDir := filepath.Join(tmpDir, "session")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	const wantID = "ses_01950000-0000-7abc-8def-0123456789ab"
	meta := NewSessionMeta("session", "ryan", "Ox1234", "claude-code", time.Now()).
		SessionID(wantID).
		Build()
	require.NoError(t, WriteSessionMetaOnly(sessionDir, meta))

	// no-op mutate: change Title only
	require.NoError(t, MutateSessionMeta(context.Background(), sessionDir, func(m *SessionMeta) (*SessionMeta, error) {
		require.NotNil(t, m)
		assert.Equal(t, wantID, m.SessionID, "mutate sees existing SessionID")
		m.Title = "updated title"
		return m, nil
	}))

	got, err := ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.Equal(t, wantID, got.SessionID, "SessionID survives mutate")
	assert.Equal(t, "updated title", got.Title)
}

// --- B. EffectiveSessionID semantics ---

// TestEffectiveSessionID_PrefersExplicit verifies that when SessionID is set,
// it is returned verbatim — the synthetic fallback is NOT consulted.
// Failure prevented: a bug that always returned the synthesized value
// would erase real ses_<UUIDv7>s in favor of v5-derived ones, scrambling
// every post-rollout recording's identity.
func TestEffectiveSessionID_PrefersExplicit(t *testing.T) {
	const explicit = "ses_01950000-0000-7abc-8def-0123456789ab"
	m := &SessionMeta{
		SessionID:   explicit,
		RepoID:      "repo_test",
		SessionName: "name",
	}
	assert.Equal(t, explicit, m.EffectiveSessionID())
}

// TestEffectiveSessionID_LegacyDeterministic asserts that EffectiveSessionID
// is a pure function of (RepoID, SessionName) for legacy sessions: the same
// inputs always produce the same output across processes and runs.
// Failure prevented: nondeterminism here would cause client and server to
// disagree on legacy IDs, silently breaking dedup, lookups, and any cached
// reference to a legacy session.
func TestEffectiveSessionID_LegacyDeterministic(t *testing.T) {
	m1 := &SessionMeta{RepoID: "repo_alpha", SessionName: "session-1"}
	m2 := &SessionMeta{RepoID: "repo_alpha", SessionName: "session-1"}

	got1 := m1.EffectiveSessionID()
	got2 := m2.EffectiveSessionID()
	assert.Equal(t, got1, got2, "same (RepoID, SessionName) must yield identical EffectiveSessionID")

	// shape check: ses_ prefix + valid UUID
	assert.True(t, strings.HasPrefix(got1, "ses_"), "must have ses_ prefix: %s", got1)
	uuidPart := strings.TrimPrefix(got1, "ses_")
	parsed, err := uuid.Parse(uuidPart)
	require.NoError(t, err, "must parse as UUID")
	assert.Equal(t, uuid.Version(5), parsed.Version(), "must be UUIDv5")
}

// TestEffectiveSessionID_LegacyDistinctInputs ensures different inputs
// produce different outputs — collisions would silently merge unrelated
// recordings.
func TestEffectiveSessionID_LegacyDistinctInputs(t *testing.T) {
	tests := []struct {
		a, b *SessionMeta
		desc string
	}{
		{
			&SessionMeta{RepoID: "repo_a", SessionName: "s1"},
			&SessionMeta{RepoID: "repo_b", SessionName: "s1"},
			"different RepoID",
		},
		{
			&SessionMeta{RepoID: "repo_a", SessionName: "s1"},
			&SessionMeta{RepoID: "repo_a", SessionName: "s2"},
			"different SessionName",
		},
		{
			&SessionMeta{RepoID: "", SessionName: "s1"},
			&SessionMeta{RepoID: "", SessionName: "s2"},
			"empty RepoID, different SessionName (still distinct)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			assert.NotEqual(t, tt.a.EffectiveSessionID(), tt.b.EffectiveSessionID())
		})
	}
}

// TestEffectiveSessionID_NamespaceStability is the load-bearing test that
// pins the legacy fallback to a specific concrete value. If
// legacySessionNamespace is ever changed (or the derivation algorithm
// changes), this test fails — alerting the engineer that they are about
// to invalidate every legacy recording's identity in every ledger
// everywhere.
//
// Failure prevented: silent rotation of the namespace UUID, which would
// be a catastrophic compatibility break with no compile-time signal.
func TestEffectiveSessionID_NamespaceStability(t *testing.T) {
	m := &SessionMeta{
		RepoID:      "repo_01936d5a-0000-7abc-8def-0123456789ab",
		SessionName: "2025-12-01T08-00-ryan-OxLegc",
	}
	// pre-computed expected output for these exact inputs against the
	// hard-coded legacySessionNamespace 5e6238b7-9403-4ee4-b5ec-8a6d37a5de14.
	// If this assertion ever needs to change, you are about to break every
	// legacy recording in every ledger — STOP and read meta.go's namespace
	// constant doc before proceeding.
	const expected = "ses_836bb195-7bd0-59c6-bd13-6a20076d1cc0"
	got := m.EffectiveSessionID()
	assert.Equal(t, expected, got,
		"legacySessionNamespace appears to have changed; this would invalidate every legacy session ID across all ledgers")
}
