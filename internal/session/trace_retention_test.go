package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/require"
)

// Failure prevented: pruning one project's traces while another project still
// needs them, including the marker-free gap between SessionEnd and finalize.
func TestTraceRetentionProtectsPendingRecordingsAcrossStorageLocations(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("OX_XDG_DISABLE", "")
	const native = "8e6b16e6-a5eb-4d3d-821b-434421449f03"
	for _, where := range []string{"ledger", "xdg"} {
		for _, state := range []string{"active", "stopped", "orphan", "finalized", "empty-marker", "empty-manifest", "null-manifest", "corrupt"} {
			t.Run(where+"/"+state, func(t *testing.T) {
				base := t.TempDir()
				t.Setenv("XDG_DATA_HOME", filepath.Join(base, "data"))
				t.Setenv("XDG_CACHE_HOME", filepath.Join(base, "cache"))
				var sessions string
				if where == "ledger" {
					sessions = filepath.Join(paths.LedgerSessionCacheBase("repo_test", "test.sageox.ai"), "sessions")
				} else {
					sessions = filepath.Join(paths.SessionCacheDir("repo_test"), "sessions")
				}
				dir := filepath.Join(sessions, "one")
				require.NoError(t, os.MkdirAll(dir, 0700))
				marker := `{"agent_session_id":"` + native + `","native_sessions":[{"id":"` + native + `"}]}`
				switch state {
				case "stopped":
					marker = `{"stopped_at":"2026-09-01T00:00:00Z","native_sessions":[{"id":"` + native + `"}]}`
				case "corrupt":
					marker = `{broken`
				case "empty-marker":
					marker = `{}`
				}
				if state == "active" || state == "stopped" || state == "corrupt" || state == "empty-marker" {
					require.NoError(t, os.WriteFile(filepath.Join(dir, recordingFile), []byte(marker), 0600))
				}
				if state != "active" && state != "stopped" && state != "corrupt" {
					require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte("{\"type\":\"header\",\"metadata\":{}}\n{\"type\":\"footer\",\"native_sessions\":[{\"id\":\""+native+"\"}]}\n"), 0600))
				}
				if state == "empty-manifest" || state == "null-manifest" {
					data := "{}"
					if state == "null-manifest" {
						data = "null"
					}
					require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte(data), 0600))
				}
				if state == "finalized" {
					manifest := filepath.Join(paths.LedgersDataDir("repo_test", "test.sageox.ai"), "sessions", "one")
					require.NoError(t, os.MkdirAll(manifest, 0700))
					require.NoError(t, os.WriteFile(filepath.Join(manifest, "meta.json"), []byte(`{"version":"1","draft":false,"files":{"raw.jsonl":{"storage":"lfs","oid":"sha256:test","size":10}}}`), 0600))
				}
				// Test the discovered target paths in isolation: the broader public
				// function additionally includes alternate cache roots on this machine.
				protected := map[string]bool{}
				err := protectTraceReferences(sessions, []string{filepath.Join(paths.LedgersDataDir("repo_test", "test.sageox.ai"), "sessions")}, protected)
				if state == "corrupt" {
					require.Error(t, err)
					return
				}
				require.NoError(t, err)
				require.Equal(t, state != "finalized", protected[native])
			})
		}
	}
}

func TestTraceRetentionDoesNotTreatUnreadableReferencesAsEmpty(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "one")
	require.NoError(t, os.MkdirAll(filepath.Join(sessionDir, recordingFile), 0700))
	require.Error(t, protectTraceReferences(dir, nil, map[string]bool{}))
}

func TestTraceRetentionRawCarrierBothHeaderDialects(t *testing.T) {
	for _, field := range []string{"metadata", "_meta"} {
		t.Run(field, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "raw.jsonl")
			require.NoError(t, os.WriteFile(path, []byte(`{"`+field+`":{"native_sessions":[{"id":"native-id"}]}}`+"\n"), 0600))
			protected := map[string]bool{}
			require.NoError(t, protectRawNativeIDs(path, protected))
			require.True(t, protected["native-id"])
		})
	}
}

// The spool is global: an unreadable manifest in another project's Ledger
// must stop pruning and identify that manifest, not blame the active recording.
func TestTraceRetentionReportsConflictedManifest(t *testing.T) {
	for _, location := range []string{"cache", "ledger"} {
		t.Run(location, func(t *testing.T) {
			cache := t.TempDir()
			ledger := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(cache, "pending"), 0700))
			base := cache
			if location == "ledger" {
				base = ledger
				require.NoError(t, os.Mkdir(filepath.Join(base, "pending"), 0700))
			}
			path := filepath.Join(base, "pending", "meta.json")
			conflict := "{\n<<<<<<< Updated upstream\n\"summary_attempts\": 2\n=======\n\"summary_attempts\": 3\n>>>>>>> Stashed changes\n}"
			require.NoError(t, os.WriteFile(path, []byte(conflict), 0600))
			err := protectTraceReferences(cache, []string{ledger}, map[string]bool{})
			require.ErrorContains(t, err, path)
			require.ErrorContains(t, err, "unresolved Git conflict markers")
		})
	}
}
