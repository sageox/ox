package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSessionMetaBase_StampsGivenSessionID asserts that the builder stamps
// exactly the SessionID the caller resolved and passed in — it must never
// mint its own.
//
// Failure prevented: a regression reintroducing an internal fallback mint
// (the pre-fix shape) would let a call site that forgets to resolve a
// durable ID silently get a fresh one instead of a compile error, exactly
// the bug that let doctor's orphan retry-upload rotate SessionIDs on every
// retry (ox-5n8e).
func TestSessionMetaBase_StampsGivenSessionID(t *testing.T) {
	tmp := t.TempDir()
	// sessionMetaBase calls getRepoIDOrDefault, which probes the project
	// root. Pass an empty/temp path; the helper tolerates the default
	// branch and we only care about SessionID here.
	projectRoot := filepath.Join(tmp, "project")
	require.NoError(t, os.MkdirAll(projectRoot, 0o755))

	given := "ses_01890a5d-ac96-774b-bcce-b302099a8057"
	meta := sessionMetaBase("session-name", "user", "Ox1234", "claude-code", time.Now(), projectRoot, given).Build()
	require.NotNil(t, meta)
	assert.Equal(t, given, meta.SessionID,
		"sessionMetaBase must stamp exactly the SessionID it was given, never substitute its own")
}

// TestResolveOrMintSessionID_MintsUniqueValidIDs guards the single mint
// chokepoint (session.ResolveOrMintSessionID) against a refactor that
// computes a single static ID and reuses it (e.g., a global var captured at
// package init), or emits a malformed ID.
func TestResolveOrMintSessionID_MintsUniqueValidIDs(t *testing.T) {
	seen := make(map[string]bool)
	const n = 50
	for i := 0; i < n; i++ {
		id := session.ResolveOrMintSessionID("", "")
		assert.True(t, sessionid.IsValidSessionID(id), "must mint a valid ses_<UUIDv7>, got %q", id)
		assert.False(t, seen[id], "duplicate SessionID generated: %q", id)
		seen[id] = true
	}
	assert.Len(t, seen, n)
}
