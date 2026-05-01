package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/sessionid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSessionMetaBase_StampsValidSessionID asserts that every meta.json
// constructed via the canonical CLI builder carries a fresh ses_<UUIDv7>.
//
// Failure prevented: a refactor that drops the `.SessionID(...)` chain
// (e.g., during a builder reorganization) would silently emit legacy-
// shaped meta.json on every newly recorded session, undoing the rollout
// and forcing every consumer back onto the synthetic fallback.
func TestSessionMetaBase_StampsValidSessionID(t *testing.T) {
	tmp := t.TempDir()
	// sessionMetaBase calls getRepoIDOrDefault, which probes the project
	// root. Pass an empty/temp path; the helper tolerates the default
	// branch and we only care about SessionID here.
	projectRoot := filepath.Join(tmp, "project")
	require.NoError(t, os.MkdirAll(projectRoot, 0o755))

	for i := 0; i < 5; i++ {
		meta := sessionMetaBase("session-name", "user", "Ox1234", "claude-code", time.Now(), projectRoot).Build()
		require.NotNil(t, meta)
		assert.True(t, sessionid.IsValidSessionID(meta.SessionID),
			"sessionMetaBase must stamp a valid ses_<UUIDv7> on every call, got: %q", meta.SessionID)
	}
}

// TestSessionMetaBase_SessionIDsAreUnique guards against a refactor that
// computes a single static ID and reuses it (e.g., a global var captured
// at package init).
func TestSessionMetaBase_SessionIDsAreUnique(t *testing.T) {
	tmp := t.TempDir()
	projectRoot := filepath.Join(tmp, "project")
	require.NoError(t, os.MkdirAll(projectRoot, 0o755))

	seen := make(map[string]bool)
	const n = 50
	for i := 0; i < n; i++ {
		meta := sessionMetaBase("session-name", "user", "Ox1234", "claude-code", time.Now(), projectRoot).Build()
		assert.False(t, seen[meta.SessionID], "duplicate SessionID generated: %q", meta.SessionID)
		seen[meta.SessionID] = true
	}
	assert.Len(t, seen, n)
}
