package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/require"
)

// TestEverRegisteredFromRecording decides whether aborting a session is allowed
// to contact the server at all.
//
// Under `session_publishing: manual` a session is never registered, so an abort
// request would be the first thing the server ever hears about it — discarding a
// session must not be what finally announces it. But the converse is worse if
// got wrong: skipping the tombstone for a session that WAS published leaves an
// "in progress" page up forever for something the user explicitly threw away.
// So unknown state must fail toward notifying.
func TestEverRegisteredFromRecording(t *testing.T) {
	write := func(t *testing.T, rs any) string {
		t.Helper()
		dir := t.TempDir()
		p := filepath.Join(dir, ".recording.json")
		b, err := json.Marshal(rs)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(p, b, 0o644))
		return p
	}

	t.Run("deferred means never announced", func(t *testing.T) {
		p := write(t, session.RecordingState{
			SessionID:                  "ses_01920000-0000-7000-8000-0000000000e1",
			LifecycleRegistrationState: "deferred",
		})
		require.False(t, everRegisteredFromRecording(p),
			"a deferred session was never announced; its abort must not announce it")
	})

	t.Run("pending was already attempted", func(t *testing.T) {
		p := write(t, session.RecordingState{LifecycleRegistrationState: "pending"})
		require.True(t, everRegisteredFromRecording(p),
			"registration was attempted, so the server may know — tombstone it")
	})

	t.Run("registered must be tombstoned", func(t *testing.T) {
		p := write(t, session.RecordingState{LifecycleRegistrationState: "registered"})
		require.True(t, everRegisteredFromRecording(p))
	})

	// The two unknown-state cases both fail toward notifying, on purpose.
	t.Run("missing file fails toward notifying", func(t *testing.T) {
		require.True(t, everRegisteredFromRecording(filepath.Join(t.TempDir(), "absent.json")),
			"cannot prove it was never registered — tombstone rather than strand a live page")
	})

	t.Run("corrupt file fails toward notifying", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, ".recording.json")
		require.NoError(t, os.WriteFile(p, []byte("{not json"), 0o644))
		require.True(t, everRegisteredFromRecording(p))
	})
}
