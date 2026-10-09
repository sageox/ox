package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCursorSessionMarkerRejectsInvalidMutationWithoutOverwriting(t *testing.T) {
	for _, failure := range []string{"empty identity", "nil update", "malformed JSON", "identity conflict", "identity changed", "unserializable timestamp", "unreadable marker", "unavailable lock"} {
		t.Run(failure, func(t *testing.T) {
			isolateSessionMarkerDir(t)
			id := cursorConversationID
			path := markerPath(id)
			initial := []byte(`{"agent_session_id":"` + id + `","agent_id":"Oxprior"}`)
			require.NoError(t, os.WriteFile(path, initial, 0o600))
			update := func(marker *SessionMarker) error { marker.AgentID = "Oxreplacement"; return nil }
			want := ""
			switch failure {
			case "empty identity":
				id, want = "", "agent session ID is required"
			case "nil update":
				update, want = nil, "marker update is required"
			case "malformed JSON":
				initial = []byte("{")
				require.NoError(t, os.WriteFile(path, initial, 0o600))
				want = "failed to parse marker"
			case "identity conflict":
				initial = []byte(`{"agent_session_id":"other-native-session"}`)
				require.NoError(t, os.WriteFile(path, initial, 0o600))
				want = "native session identity conflict"
			case "identity changed":
				update = func(marker *SessionMarker) error { marker.AgentSessionID = "other-native-session"; return nil }
				want = "changed native session identity"
			case "unserializable timestamp":
				update = func(marker *SessionMarker) error {
					marker.PrimedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
					return nil
				}
				want = "failed to marshal marker"
			case "unreadable marker":
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Mkdir(path, 0o700))
				want = "failed to read marker"
			case "unavailable lock":
				if err := os.Symlink(filepath.Join(t.TempDir(), "missing", "lock"), path+".lock"); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				want = "failed to acquire session marker lock"
			}
			marker, err := UpdateSessionMarker(id, update)
			require.ErrorContains(t, err, want)
			require.Nil(t, marker)
			if failure != "unreadable marker" {
				after, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, initial, after)
			}
		})
	}
}

func TestCursorSessionMarkerInitializesLegacyIdentity(t *testing.T) {
	isolateSessionMarkerDir(t)
	path := markerPath(cursorConversationID)
	require.NoError(t, os.WriteFile(path, []byte(`{"agent_id":"Oxprior"}`), 0o600))
	marker, err := UpdateSessionMarker(cursorConversationID, func(marker *SessionMarker) error {
		require.Equal(t, cursorConversationID, marker.AgentSessionID)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, "Oxprior", marker.AgentID)
	stored, err := ReadSessionMarker(cursorConversationID)
	require.NoError(t, err)
	require.Equal(t, cursorConversationID, stored.AgentSessionID)
}

func TestCursorSessionMarkerRefusesUnavailableDirectory(t *testing.T) {
	isolateSessionMarkerDir(t)
	path := SessionMarkerDir()
	require.NoError(t, os.RemoveAll(path))
	require.NoError(t, os.WriteFile(path, []byte("unrelated file"), 0o600))
	_, err := UpdateSessionMarker(cursorConversationID, func(*SessionMarker) error { t.Fatal("invalid directory reached mutation"); return nil })
	require.ErrorContains(t, err, "failed to create marker directory")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "unrelated file", string(data))
}

func TestCursorSessionMarkerWriteFailurePreservesCommittedIdentity(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix file permissions without root privileges")
	}
	isolateSessionMarkerDir(t)
	path := markerPath(cursorConversationID)
	before := []byte(`{"agent_id":"Oxprior","agent_session_id":"` + cursorConversationID + `"}`)
	require.NoError(t, os.WriteFile(path, before, 0o600))
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(path), 0o700) })
	_, err := UpdateSessionMarker(cursorConversationID, func(marker *SessionMarker) error {
		marker.AgentID = "Oxreplacement"
		return os.Chmod(filepath.Dir(path), 0o500)
	})
	require.ErrorContains(t, err, "failed to write marker")
	require.NoError(t, os.Chmod(filepath.Dir(path), 0o700))
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
}
