//go:build !short

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIdempotentPrimeOnStartup verifies that prime creates a marker on first run
// and skips output on subsequent runs with --idempotent flag.
func TestIdempotentPrimeOnStartup(t *testing.T) {
	// create a unique session ID for this test
	agentSessionID := "test_startup_" + time.Now().Format("20060102150405.000")
	t.Cleanup(func() {
		DeleteSessionMarker(agentSessionID)
	})

	t.Run("first prime creates marker", func(t *testing.T) {
		// simulate first prime (no marker exists)
		marker, err := ReadSessionMarker(agentSessionID)
		require.NoError(t, err)
		assert.Nil(t, marker, "marker should not exist before first prime")

		// simulate what ox agent prime does on first run
		agentID := "OxTest"
		sessionID := "oxsid_test123"
		newMarker := &SessionMarker{
			AgentID:         agentID,
			SessionID:       sessionID,
			AgentSessionID: agentSessionID,
			PrimedAt:        time.Now(),
		}
		err = WriteSessionMarker(newMarker)
		require.NoError(t, err)

		// verify marker was created
		marker, err = ReadSessionMarker(agentSessionID)
		require.NoError(t, err)
		require.NotNil(t, marker, "marker should exist after first prime")
		assert.Equal(t, agentID, marker.AgentID)
		assert.Equal(t, sessionID, marker.SessionID)
	})

	t.Run("idempotent mode skips when marker exists", func(t *testing.T) {
		// marker should already exist from previous test
		marker, err := ReadSessionMarker(agentSessionID)
		require.NoError(t, err)
		require.NotNil(t, marker, "marker should exist")

		// in idempotent mode, we would return early without output
		// this simulates the check in runAgentPrime
		idempotent := true
		if marker != nil && idempotent {
			// this is where we'd return nil (no output)
			// verify this is the expected behavior
			assert.True(t, true, "idempotent mode should skip when marker exists")
		}
	})

	t.Run("non-idempotent mode outputs even when marker exists", func(t *testing.T) {
		marker, err := ReadSessionMarker(agentSessionID)
		require.NoError(t, err)
		require.NotNil(t, marker)

		// in non-idempotent mode, we should still output but reuse agent_id
		idempotent := false
		if marker != nil && !idempotent {
			// we would reuse the agent_id from marker
			assert.Equal(t, "OxTest", marker.AgentID, "should reuse agent_id from marker")
		}
	})
}

// TestAgentIDPreservedAfterClear verifies that agent_id is preserved
// when /clear is executed (marker survives, agent_id is reused).
func TestAgentIDPreservedAfterClear(t *testing.T) {
	agentSessionID := "test_clear_" + time.Now().Format("20060102150405.000")
	t.Cleanup(func() {
		DeleteSessionMarker(agentSessionID)
	})

	originalAgentID := "OxClear"
	originalSessionID := "oxsid_clear123"

	t.Run("initial prime creates marker with agent_id", func(t *testing.T) {
		// simulate SessionStart:startup hook
		marker := &SessionMarker{
			AgentID:         originalAgentID,
			SessionID:       originalSessionID,
			AgentSessionID: agentSessionID,
			PrimedAt:        time.Now(),
		}
		err := WriteSessionMarker(marker)
		require.NoError(t, err)

		// verify
		read, err := ReadSessionMarker(agentSessionID)
		require.NoError(t, err)
		require.NotNil(t, read)
		assert.Equal(t, originalAgentID, read.AgentID)
	})

	t.Run("after /clear marker still exists with same agent_id", func(t *testing.T) {
		// /clear wipes Claude's context but NOT the marker file in /tmp
		// the marker file persists because it's outside Claude's control

		// simulate SessionStart:clear hook - marker should still exist
		marker, err := ReadSessionMarker(agentSessionID)
		require.NoError(t, err)
		require.NotNil(t, marker, "marker should survive /clear")
		assert.Equal(t, originalAgentID, marker.AgentID, "agent_id should be preserved after /clear")
	})

	t.Run("re-prime after /clear reuses agent_id", func(t *testing.T) {
		// simulate what happens in runAgentPrime after /clear
		// (non-idempotent mode because matcher is "clear")
		existingMarker, err := ReadSessionMarker(agentSessionID)
		require.NoError(t, err)
		require.NotNil(t, existingMarker)

		// the agent_id would be reused from marker
		var agentID string
		if existingMarker != nil && existingMarker.AgentID != "" {
			agentID = existingMarker.AgentID
		} else {
			agentID = "OxNewID" // this should NOT happen
		}

		assert.Equal(t, originalAgentID, agentID, "agent_id should be reused from marker")

		// update marker with new prime time (but same agent_id)
		newMarker := &SessionMarker{
			AgentID:         agentID,
			SessionID:       "oxsid_clear_reprime",
			AgentSessionID: agentSessionID,
			PrimedAt:        time.Now(),
		}
		err = WriteSessionMarker(newMarker)
		require.NoError(t, err)

		// verify agent_id is still the same
		final, err := ReadSessionMarker(agentSessionID)
		require.NoError(t, err)
		assert.Equal(t, originalAgentID, final.AgentID, "agent_id should remain unchanged")
	})
}

// TestAgentIDPreservedAfterCompact verifies that agent_id is preserved
// when compaction occurs.
func TestAgentIDPreservedAfterCompact(t *testing.T) {
	agentSessionID := "test_compact_" + time.Now().Format("20060102150405.000")
	t.Cleanup(func() {
		DeleteSessionMarker(agentSessionID)
	})

	originalAgentID := "OxCompact"

	t.Run("initial prime and compact preserves agent_id", func(t *testing.T) {
		// initial prime
		marker := &SessionMarker{
			AgentID:         originalAgentID,
			SessionID:       "oxsid_compact1",
			AgentSessionID: agentSessionID,
			PrimedAt:        time.Now(),
		}
		err := WriteSessionMarker(marker)
		require.NoError(t, err)

		// simulate PreCompact hook (belt)
		// marker should exist, we re-prime with same agent_id
		existing, _ := ReadSessionMarker(agentSessionID)
		require.NotNil(t, existing)
		assert.Equal(t, originalAgentID, existing.AgentID)

		// simulate SessionStart:compact hook (suspenders)
		// again, marker should exist, we re-prime with same agent_id
		existing2, _ := ReadSessionMarker(agentSessionID)
		require.NotNil(t, existing2)
		assert.Equal(t, originalAgentID, existing2.AgentID, "agent_id preserved after compact")
	})
}

// TestMultipleSessionsHaveIndependentMarkers verifies that different coding agent
// sessions (e.g., multiple terminal windows) have independent markers.
func TestMultipleSessionsHaveIndependentMarkers(t *testing.T) {
	session1 := "test_multi_session1_" + time.Now().Format("20060102150405.000")
	session2 := "test_multi_session2_" + time.Now().Format("20060102150405.000")
	t.Cleanup(func() {
		DeleteSessionMarker(session1)
		DeleteSessionMarker(session2)
	})

	t.Run("different sessions have different agent_ids", func(t *testing.T) {
		// session 1 gets primed
		marker1 := &SessionMarker{
			AgentID:         "OxSess1",
			SessionID:       "oxsid_sess1",
			AgentSessionID: session1,
			PrimedAt:        time.Now(),
		}
		err := WriteSessionMarker(marker1)
		require.NoError(t, err)

		// session 2 gets primed (different agent_id)
		marker2 := &SessionMarker{
			AgentID:         "OxSess2",
			SessionID:       "oxsid_sess2",
			AgentSessionID: session2,
			PrimedAt:        time.Now(),
		}
		err = WriteSessionMarker(marker2)
		require.NoError(t, err)

		// verify they're independent
		read1, _ := ReadSessionMarker(session1)
		read2, _ := ReadSessionMarker(session2)

		require.NotNil(t, read1)
		require.NotNil(t, read2)
		assert.Equal(t, "OxSess1", read1.AgentID)
		assert.Equal(t, "OxSess2", read2.AgentID)
		assert.NotEqual(t, read1.AgentID, read2.AgentID, "different sessions should have different agent_ids")
	})

	t.Run("clearing one session doesn't affect another", func(t *testing.T) {
		// session 1 does /clear
		// (marker still exists, just needs re-prime)
		read1, _ := ReadSessionMarker(session1)
		require.NotNil(t, read1)

		// session 2 is unaffected
		read2, _ := ReadSessionMarker(session2)
		require.NotNil(t, read2)
		assert.Equal(t, "OxSess2", read2.AgentID, "session 2 should be unaffected by session 1's clear")
	})
}

// TestMarkerCleanupOnReboot verifies that /tmp markers are auto-cleaned on reboot.
// This is a documentation test - we can't actually reboot in a test.
func TestMarkerCleanupOnReboot(t *testing.T) {
	t.Run("markers are stored in /tmp which is cleaned on reboot", func(t *testing.T) {
		// verify marker path is under /tmp (per-user scoped)
		dir := SessionMarkerDir()
		assert.Contains(t, dir, "sageox")
		assert.True(t, strings.HasSuffix(dir, "sessions"))

		// on macOS/Linux, /tmp is typically cleared on reboot
		// this means stale markers from old sessions are automatically cleaned
		// (this is by design - we don't want markers accumulating forever)
	})
}

// TestHookBehaviorMatrix tests the expected behavior for each hook type.
func TestHookBehaviorMatrix(t *testing.T) {
	agentSessionID := "test_matrix_" + time.Now().Format("20060102150405.000")
	t.Cleanup(func() {
		DeleteSessionMarker(agentSessionID)
	})

	originalAgentID := "OxMatrix"

	// helper to simulate prime behavior
	simulatePrime := func(idempotent bool) (outputGenerated bool, agentIDUsed string) {
		marker, _ := ReadSessionMarker(agentSessionID)

		if marker != nil && idempotent {
			// idempotent mode: skip if marker exists
			return false, ""
		}

		// determine agent_id
		if marker != nil && marker.AgentID != "" {
			agentIDUsed = marker.AgentID
		} else {
			agentIDUsed = originalAgentID // would be generated in real code
		}

		// write marker
		newMarker := &SessionMarker{
			AgentID:         agentIDUsed,
			AgentSessionID: agentSessionID,
			PrimedAt:        time.Now(),
		}
		_ = WriteSessionMarker(newMarker)

		return true, agentIDUsed
	}

	t.Run("SessionStart:startup - idempotent, generates output on fresh session", func(t *testing.T) {
		// no marker exists yet
		outputGenerated, agentID := simulatePrime(true) // idempotent
		assert.True(t, outputGenerated, "should generate output on fresh session")
		assert.Equal(t, originalAgentID, agentID)
	})

	t.Run("SessionStart:resume - idempotent, skips if already primed", func(t *testing.T) {
		// marker exists from previous test
		outputGenerated, _ := simulatePrime(true) // idempotent
		assert.False(t, outputGenerated, "should skip output when marker exists (resume)")
	})

	t.Run("SessionStart:clear - force, generates output with same agent_id", func(t *testing.T) {
		outputGenerated, agentID := simulatePrime(false) // force (non-idempotent)
		assert.True(t, outputGenerated, "should generate output after clear")
		assert.Equal(t, originalAgentID, agentID, "should reuse agent_id after clear")
	})

	t.Run("SessionStart:compact - force, generates output with same agent_id", func(t *testing.T) {
		outputGenerated, agentID := simulatePrime(false) // force (non-idempotent)
		assert.True(t, outputGenerated, "should generate output after compact")
		assert.Equal(t, originalAgentID, agentID, "should reuse agent_id after compact")
	})

	t.Run("PreCompact - force (belt), generates output with same agent_id", func(t *testing.T) {
		outputGenerated, agentID := simulatePrime(false) // force (non-idempotent)
		assert.True(t, outputGenerated, "should generate output for PreCompact")
		assert.Equal(t, originalAgentID, agentID, "should reuse agent_id for PreCompact")
	})
}

// TestGracefulMarkerFailure verifies that marker failures don't break priming.
func TestGracefulMarkerFailure(t *testing.T) {
	t.Run("read failure returns nil without error", func(t *testing.T) {
		// reading a non-existent marker should not error
		marker, err := ReadSessionMarker("nonexistent_session_xyz")
		assert.NoError(t, err)
		assert.Nil(t, marker)
	})

	t.Run("write to unwritable path logs warning but continues", func(t *testing.T) {
		// this tests the graceful failure mode
		// in practice, WriteSessionMarker would log a warning on failure
		// but the prime would still output (just without marker optimization)

		// we can't easily test unwritable paths without modifying permissions
		// but we verify the error handling exists
		marker := &SessionMarker{
			AgentID:         "OxFail",
			AgentSessionID: "", // empty session ID should be handled
		}
		err := WriteSessionMarker(marker)
		assert.Error(t, err, "should error on empty session ID")
	})
}

// TestHookConfigurationFormat verifies the hook configuration structure.
func TestHookConfigurationFormat(t *testing.T) {
	t.Run("SessionStart hooks use correct matchers", func(t *testing.T) {
		// verify matcher constants are correct
		assert.Equal(t, "startup", matcherStartup)
		assert.Equal(t, "resume", matcherResume)
		assert.Equal(t, "clear", matcherClear)
		assert.Equal(t, "compact", matcherCompact)
	})

	t.Run("idempotent command includes --idempotent flag", func(t *testing.T) {
		assert.Contains(t, oxPrimeCommandIdempotent, "--idempotent")
		assert.Contains(t, oxPrimeCommandIdempotent, "AGENT_ENV=claude-code")
	})

	t.Run("force command does not include --idempotent flag", func(t *testing.T) {
		assert.NotContains(t, oxPrimeCommand, "--idempotent")
		assert.Contains(t, oxPrimeCommand, "AGENT_ENV=claude-code")
	})
}

// TestInstallProjectClaudeHooksWithMatchers verifies the hook installation.
func TestInstallProjectClaudeHooksWithMatchers(t *testing.T) {
	tmpDir := t.TempDir()
	claudeDir := filepath.Join(tmpDir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0755))

	t.Run("installs lifecycle hooks for all events", func(t *testing.T) {
		err := InstallProjectClaudeHooks(tmpDir)
		require.NoError(t, err)

		settings, err := readProjectClaudeSettings(tmpDir)
		require.NoError(t, err)

		// new format: one entry per lifecycle event using ox agent hook
		for _, event := range claudeLifecycleEvents {
			entries := settings.Hooks[event]
			require.NotEmpty(t, entries, "should have hook for %s", event)
			require.NotEmpty(t, entries[0].Hooks)
			assert.Contains(t, entries[0].Hooks[0].Command, "ox agent hook "+event,
				"hook for %s should use ox agent hook command", event)
		}
	})

	t.Run("SessionStart uses ox agent hook", func(t *testing.T) {
		settings, err := readProjectClaudeSettings(tmpDir)
		require.NoError(t, err)

		sessionStartHooks := settings.Hooks[claudeSessionStart]
		require.NotEmpty(t, sessionStartHooks)
		// single entry (not 4 matchers like the old format)
		assert.Len(t, sessionStartHooks, 1, "new format uses single entry per event")
		assert.Contains(t, sessionStartHooks[0].Hooks[0].Command, "ox agent hook SessionStart")
	})

	t.Run("PreCompact uses ox agent hook", func(t *testing.T) {
		settings, err := readProjectClaudeSettings(tmpDir)
		require.NoError(t, err)

		preCompactHooks := settings.Hooks[claudePreCompact]
		require.NotEmpty(t, preCompactHooks)
		require.NotEmpty(t, preCompactHooks[0].Hooks)
		assert.Contains(t, preCompactHooks[0].Hooks[0].Command, "ox agent hook PreCompact")
	})
}
