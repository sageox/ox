package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	claude "github.com/sageox/ox/internal/hooks/claude"
)

// TestCheckClaudeStatusLine detects and repairs the project status line.
// Failure prevented: a repo whose hooks predate the status line never gets
// it, or doctor "repairs" a coworker's custom statusLine away.
func TestCheckClaudeStatusLine(t *testing.T) {
	t.Run("missing is repaired", func(t *testing.T) {
		gitRoot := t.TempDir()
		require.NoError(t, InstallProjectClaudeHooks(gitRoot))
		m := readProjectSettingsMap(t, gitRoot)
		delete(m, "statusLine")
		data, err := json.Marshal(m)
		require.NoError(t, err)
		writeProjectSettings(t, gitRoot, string(data))

		res := checkClaudeStatusLineAt(gitRoot, false)
		assert.False(t, res.passed, "missing status line is reported")
		assert.Contains(t, res.message, "missing")

		res = checkClaudeStatusLineAt(gitRoot, true)
		assert.True(t, res.passed)
		assert.Equal(t, claude.StatusLineCurrent, claude.ClassifyStatusLine(readProjectSettingsMap(t, gitRoot)))
		assert.True(t, checkClaudeStatusLineAt(gitRoot, false).passed)
	})

	t.Run("stale ox command is repaired", func(t *testing.T) {
		gitRoot := t.TempDir()
		require.NoError(t, InstallProjectClaudeHooks(gitRoot))
		m := readProjectSettingsMap(t, gitRoot)
		m["statusLine"] = json.RawMessage(`{"type":"command","command":"ox statusline"}`)
		data, err := json.Marshal(m)
		require.NoError(t, err)
		writeProjectSettings(t, gitRoot, string(data))

		assert.False(t, checkClaudeStatusLineAt(gitRoot, false).passed)
		assert.True(t, checkClaudeStatusLineAt(gitRoot, true).passed)
		assert.Equal(t, claude.StatusLineCurrent, claude.ClassifyStatusLine(readProjectSettingsMap(t, gitRoot)))
	})

	t.Run("foreign is an informational pass and untouched", func(t *testing.T) {
		gitRoot := t.TempDir()
		require.NoError(t, InstallProjectClaudeHooks(gitRoot))
		m := readProjectSettingsMap(t, gitRoot)
		m["statusLine"] = json.RawMessage(`{"type":"command","command":"starship statusline"}`)
		data, err := json.Marshal(m)
		require.NoError(t, err)
		writeProjectSettings(t, gitRoot, string(data))

		res := checkClaudeStatusLineAt(gitRoot, true)
		assert.True(t, res.passed)
		assert.False(t, res.warning)
		assert.Contains(t, res.message, "custom statusLine")
		assert.Equal(t, claude.StatusLineForeign, claude.ClassifyStatusLine(readProjectSettingsMap(t, gitRoot)))
	})

	t.Run("no ox hooks is skipped", func(t *testing.T) {
		gitRoot := t.TempDir()
		writeProjectSettings(t, gitRoot, `{"model":"opus"}`)
		assert.True(t, checkClaudeStatusLineAt(gitRoot, true).skipped)
		assert.NotContains(t, readProjectSettingsMap(t, gitRoot), "statusLine", "doctor does not add ox to a repo without ox hooks")
	})
}
