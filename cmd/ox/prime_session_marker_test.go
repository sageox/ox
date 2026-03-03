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

func TestSessionMarkerDir(t *testing.T) {
	dir := SessionMarkerDir()
	assert.Contains(t, dir, "sageox")
	assert.True(t, strings.HasSuffix(dir, "sessions"))
}

func TestMarkerPath(t *testing.T) {
	t.Run("simple session ID", func(t *testing.T) {
		path := markerPath("abc123")
		assert.Equal(t, filepath.Join(SessionMarkerDir(), "abc123.json"), path)
	})

	t.Run("sanitizes path traversal", func(t *testing.T) {
		path := markerPath("../../../etc/passwd")
		assert.True(t, strings.HasPrefix(path, SessionMarkerDir()))
		assert.NotContains(t, path, "..")
	})

	t.Run("sanitizes slashes", func(t *testing.T) {
		path := markerPath("path/to/session")
		assert.NotContains(t, path, "/to/")
	})
}

func TestSessionMarkerReadWrite(t *testing.T) {
	t.Run("write and read marker", func(t *testing.T) {
		sessionID := "test_" + time.Now().Format("20060102150405.000")
		marker := &SessionMarker{
			AgentID:        "OxTest",
			SessionID:      "oxsid_test123",
			AgentSessionID: sessionID,
			PrimedAt:       time.Now().Truncate(time.Second),
		}

		// write
		err := WriteSessionMarker(marker)
		require.NoError(t, err)
		t.Cleanup(func() {
			DeleteSessionMarker(sessionID)
		})

		// read back
		read, err := ReadSessionMarker(sessionID)
		require.NoError(t, err)
		require.NotNil(t, read)

		assert.Equal(t, marker.AgentID, read.AgentID)
		assert.Equal(t, marker.SessionID, read.SessionID)
		assert.Equal(t, marker.AgentSessionID, read.AgentSessionID)
		assert.Equal(t, marker.PrimedAt.Unix(), read.PrimedAt.Unix())
	})

	t.Run("read non-existent marker returns nil", func(t *testing.T) {
		read, err := ReadSessionMarker("nonexistent_session_id")
		assert.NoError(t, err)
		assert.Nil(t, read)
	})

	t.Run("read empty session ID returns nil", func(t *testing.T) {
		read, err := ReadSessionMarker("")
		assert.NoError(t, err)
		assert.Nil(t, read)
	})

	t.Run("delete marker", func(t *testing.T) {
		sessionID := "test_delete_" + time.Now().Format("20060102150405.000")
		marker := &SessionMarker{
			AgentID:        "OxDel",
			AgentSessionID: sessionID,
			PrimedAt:       time.Now(),
		}

		// write
		err := WriteSessionMarker(marker)
		require.NoError(t, err)

		// verify exists
		read, err := ReadSessionMarker(sessionID)
		require.NoError(t, err)
		require.NotNil(t, read)

		// delete
		err = DeleteSessionMarker(sessionID)
		require.NoError(t, err)

		// verify gone
		read, err = ReadSessionMarker(sessionID)
		require.NoError(t, err)
		assert.Nil(t, read)
	})

	t.Run("delete non-existent marker is no-op", func(t *testing.T) {
		err := DeleteSessionMarker("nonexistent_delete_test")
		assert.NoError(t, err)
	})

	t.Run("delete empty session ID is no-op", func(t *testing.T) {
		err := DeleteSessionMarker("")
		assert.NoError(t, err)
	})
}

func TestSessionMarkerJSONFormat(t *testing.T) {
	// verify the marker file is in JSON format
	sessionID := "test_format_" + time.Now().Format("20060102150405.000")
	marker := &SessionMarker{
		AgentID:        "OxFmt",
		SessionID:      "oxsid_format123",
		AgentSessionID: sessionID,
		PrimedAt:       time.Unix(1700000000, 0),
	}

	err := WriteSessionMarker(marker)
	require.NoError(t, err)
	t.Cleanup(func() {
		DeleteSessionMarker(sessionID)
	})

	// read raw file content
	path := markerPath(sessionID)
	content, err := os.ReadFile(path)
	require.NoError(t, err)

	// verify JSON format
	assert.Contains(t, string(content), `"agent_id": "OxFmt"`)
	assert.Contains(t, string(content), `"session_id": "oxsid_format123"`)
	assert.Contains(t, string(content), `"agent_session_id"`)
}

func TestSessionMarkerUpdateLastNotified(t *testing.T) {
	sessionID := "test_update_" + time.Now().Format("20060102150405.000")
	marker := &SessionMarker{
		AgentID:        "OxUpd",
		AgentSessionID: sessionID,
		PrimedAt:       time.Now(),
	}

	err := WriteSessionMarker(marker)
	require.NoError(t, err)
	t.Cleanup(func() {
		DeleteSessionMarker(sessionID)
	})

	// update last notified
	newTime := time.Now().Add(1 * time.Hour)
	err = marker.UpdateLastNotified(newTime)
	require.NoError(t, err)

	// verify the in-memory value was updated
	assert.Equal(t, newTime.Unix(), marker.LastNotified.Unix())

	// read back and verify persisted
	read, err := ReadSessionMarker(sessionID)
	require.NoError(t, err)
	assert.Equal(t, newTime.Unix(), read.LastNotified.Unix())
}

func TestIsAgentHookContext(t *testing.T) {
	// save original env
	origProjectDir := os.Getenv("CLAUDE_PROJECT_DIR")
	origClaudeCode := os.Getenv("CLAUDECODE")
	origEntrypoint := os.Getenv("CLAUDE_CODE_ENTRYPOINT")
	t.Cleanup(func() {
		os.Setenv("CLAUDE_PROJECT_DIR", origProjectDir)
		os.Setenv("CLAUDECODE", origClaudeCode)
		os.Setenv("CLAUDE_CODE_ENTRYPOINT", origEntrypoint)
	})

	t.Run("detects CLAUDE_PROJECT_DIR", func(t *testing.T) {
		os.Setenv("CLAUDE_PROJECT_DIR", "/some/project")
		os.Unsetenv("CLAUDECODE")
		os.Unsetenv("CLAUDE_CODE_ENTRYPOINT")
		assert.True(t, IsAgentHookContext())
	})

	t.Run("detects CLAUDECODE=1", func(t *testing.T) {
		os.Unsetenv("CLAUDE_PROJECT_DIR")
		os.Setenv("CLAUDECODE", "1")
		os.Unsetenv("CLAUDE_CODE_ENTRYPOINT")
		assert.True(t, IsAgentHookContext())
	})

	t.Run("detects CLAUDE_CODE_ENTRYPOINT", func(t *testing.T) {
		os.Unsetenv("CLAUDE_PROJECT_DIR")
		os.Unsetenv("CLAUDECODE")
		os.Setenv("CLAUDE_CODE_ENTRYPOINT", "cli")
		assert.True(t, IsAgentHookContext())
	})

	t.Run("returns false when no agent env vars", func(t *testing.T) {
		os.Unsetenv("CLAUDE_PROJECT_DIR")
		os.Unsetenv("CLAUDECODE")
		os.Unsetenv("CLAUDE_CODE_ENTRYPOINT")
		assert.False(t, IsAgentHookContext())
	})
}

func TestWriteToAgentEnvFile(t *testing.T) {
	t.Run("no-op when CLAUDE_ENV_FILE not set", func(t *testing.T) {
		origEnv := os.Getenv("CLAUDE_ENV_FILE")
		os.Unsetenv("CLAUDE_ENV_FILE")
		t.Cleanup(func() {
			if origEnv != "" {
				os.Setenv("CLAUDE_ENV_FILE", origEnv)
			}
		})

		err := WriteToAgentEnvFile(map[string]string{"KEY": "value"})
		assert.NoError(t, err)
	})

	t.Run("writes to env file when set", func(t *testing.T) {
		tmpDir := t.TempDir()
		envFile := filepath.Join(tmpDir, "env")

		origEnv := os.Getenv("CLAUDE_ENV_FILE")
		os.Setenv("CLAUDE_ENV_FILE", envFile)
		t.Cleanup(func() {
			if origEnv != "" {
				os.Setenv("CLAUDE_ENV_FILE", origEnv)
			} else {
				os.Unsetenv("CLAUDE_ENV_FILE")
			}
		})

		err := WriteToAgentEnvFile(map[string]string{
			"AGENT_ENV":       "claude-code",
			"SAGEOX_AGENT_ID": "OxEnv1",
		})
		require.NoError(t, err)

		// verify content
		content, err := os.ReadFile(envFile)
		require.NoError(t, err)
		assert.Contains(t, string(content), `export AGENT_ENV="claude-code"`)
		assert.Contains(t, string(content), `export SAGEOX_AGENT_ID="OxEnv1"`)
	})
}
