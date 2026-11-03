package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Exercise both project cleanup entry points and the daemon's directory entry
// point. All three must use the scanned directory, never serialized paths.
func recordingPathCleaners() map[string]func(string, string) {
	return map[string]func(string, string){
		"project ghosts": func(project, _ string) { CleanupGhostSessions(project) },
		"daemon ghosts":  func(_, dir string) { CleanupGhostSessionsInDir(dir) },
		"stale stubs":    func(project, _ string) { cleanupStaleEmptyRecordings(project) },
	}
}

func TestRecordingCleanupIgnoresSerializedSessionPath(t *testing.T) {
	for name, cleanup := range recordingPathCleaners() {
		t.Run(name, func(t *testing.T) {
			for _, target := range []string{"git", "traversal", "root", "symlink"} {
				t.Run(target, func(t *testing.T) {
					project, sessionsDir := setupRecordingTestWithSessionsBase(t, t.TempDir())
					sessionDir := filepath.Join(sessionsDir, "ghost")
					state := ghostState(t, sessionDir)
					state.StartedAt = time.Now().Add(-72 * time.Hour)
					victim := filepath.Join(project, ".git")
					serializedPath := victim
					switch target {
					case "traversal":
						victim = filepath.Join(filepath.Dir(sessionsDir), "outside")
						serializedPath = sessionDir + "/../../outside"
					case "root":
						victim = sessionsDir
						serializedPath = sessionsDir
					case "symlink":
						link := filepath.Join(project, "redirect")
						require.NoError(t, os.Symlink(project, link))
						serializedPath = filepath.Join(link, ".git")
					}
					require.NoError(t, os.MkdirAll(victim, 0755))
					sentinel := filepath.Join(victim, "keep")
					require.NoError(t, os.WriteFile(sentinel, []byte("user data"), 0600))
					require.NoError(t, os.Symlink(victim, filepath.Join(sessionDir, "linked-content")))
					state.SessionPath = serializedPath
					require.NoError(t, os.WriteFile(recordingStatePath(sessionDir), mustMarshal(t, state), 0600))

					cleanup(project, sessionsDir)

					content, err := os.ReadFile(sentinel)
					require.NoError(t, err, "cleanup must not touch the serialized target")
					assert.Equal(t, "user data", string(content))
					assert.NoDirExists(t, sessionDir, "the actual empty cache stub is still reclaimable")
				})
			}
		})
	}
}

func TestRecordingCleanupClassifiesScannedDirectory(t *testing.T) {
	for name, cleanup := range recordingPathCleaners() {
		t.Run(name, func(t *testing.T) {
			project, sessionsDir := setupRecordingTestWithSessionsBase(t, t.TempDir())
			sessionDir := filepath.Join(sessionsDir, "recoverable")
			state := ghostState(t, sessionDir)
			state.StartedAt = time.Now().Add(-72 * time.Hour)
			state.SessionPath = t.TempDir() // no raw.jsonl here
			require.NoError(t, os.WriteFile(recordingStatePath(sessionDir), mustMarshal(t, state), 0600))
			raw := []byte("{\"type\":\"user\",\"content\":\"keep me\"}\n")
			rawPath := filepath.Join(sessionDir, "raw.jsonl")
			require.NoError(t, os.WriteFile(rawPath, raw, 0600))

			cleanup(project, sessionsDir)

			content, err := os.ReadFile(rawPath)
			require.NoError(t, err)
			assert.Equal(t, raw, content)
			assert.FileExists(t, recordingStatePath(sessionDir))
			assert.DirExists(t, state.SessionPath)
		})
	}
}

func TestRecordingCleanupRejectsSymlinkEscapes(t *testing.T) {
	for name, cleanup := range recordingPathCleaners() {
		t.Run(name, func(t *testing.T) {
			for _, linkAt := range []string{"root", "session", "marker", "raw"} {
				t.Run(linkAt, func(t *testing.T) {
					project, sessionsDir := setupRecordingTestWithSessionsBase(t, t.TempDir())
					outside := t.TempDir()
					outsideSession := filepath.Join(outside, "ghost")
					state := ghostState(t, outsideSession)
					state.StartedAt = time.Now().Add(-72 * time.Hour)
					require.NoError(t, os.WriteFile(recordingStatePath(outsideSession), mustMarshal(t, state), 0600))
					sessionDir := filepath.Join(sessionsDir, "ghost")
					switch linkAt {
					case "root":
						require.NoError(t, os.MkdirAll(filepath.Dir(sessionsDir), 0755))
						require.NoError(t, os.Symlink(outside, sessionsDir))
					case "session":
						require.NoError(t, os.MkdirAll(sessionsDir, 0755))
						require.NoError(t, os.Symlink(outsideSession, sessionDir))
					case "marker":
						require.NoError(t, os.MkdirAll(sessionDir, 0755))
						require.NoError(t, os.Symlink(recordingStatePath(outsideSession), recordingStatePath(sessionDir)))
					case "raw":
						require.NoError(t, os.MkdirAll(sessionDir, 0755))
						require.NoError(t, os.WriteFile(recordingStatePath(sessionDir), mustMarshal(t, state), 0600))
						require.NoError(t, os.WriteFile(filepath.Join(outsideSession, "raw.jsonl"), []byte("{\"type\":\"header\"}\n"), 0600))
						require.NoError(t, os.Symlink(filepath.Join(outsideSession, "raw.jsonl"), filepath.Join(sessionDir, "raw.jsonl")))
					}

					cleanup(project, sessionsDir)

					assert.FileExists(t, recordingStatePath(outsideSession))
					assert.FileExists(t, recordingStatePath(sessionDir), "unverifiable paths must fail closed")
				})
			}
		})
	}
}

func TestRecordingCleanupLeavesLegacyRepositorySessionsAlone(t *testing.T) {
	project := setupRecordingTest(t, t.TempDir())
	sessionDir := filepath.Join(project, "sessions", "ghost")
	state := ghostState(t, sessionDir)
	state.StartedAt = time.Now().Add(-72 * time.Hour)
	require.NoError(t, os.WriteFile(recordingStatePath(sessionDir), mustMarshal(t, state), 0600))

	assert.Zero(t, CleanupGhostSessions(project).Removed)
	cleanupStaleEmptyRecordings(project)

	assert.FileExists(t, recordingStatePath(sessionDir), "legacy fallback is read-only even with an honest SessionPath")
}
