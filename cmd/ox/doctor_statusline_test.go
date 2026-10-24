package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	claude "github.com/sageox/ox/internal/hooks/claude"
)

// TestCheckClaudeStatusLine detects and repairs the coworker's project status
// line against their setting. Failure prevented: a repo whose hooks predate
// the status line never gets it; doctor "repairs" a coworker's custom
// statusLine away; a committed statusLine forces the row on everyone; or
// `statusline off` leaves a key behind that keeps a blank row.
func TestCheckClaudeStatusLine(t *testing.T) {
	setLocalStatusLine := func(t *testing.T, gitRoot string, raw string) {
		t.Helper()
		_, m, err := readSettingsFileRaw(getLocalClaudeSettingsPath(gitRoot))
		require.NoError(t, err)
		if raw == "" {
			delete(m, "statusLine")
		} else {
			m["statusLine"] = json.RawMessage(raw)
		}
		data, err := json.Marshal(m)
		require.NoError(t, err)
		writeLocalSettings(t, gitRoot, string(data))
	}

	t.Run("missing is repaired", func(t *testing.T) {
		isolateUserConfig(t)
		gitRoot := t.TempDir()
		require.NoError(t, InstallProjectClaudeHooks(gitRoot))
		setLocalStatusLine(t, gitRoot, "")

		res := checkClaudeStatusLineAt(gitRoot, false)
		assert.False(t, res.passed, "missing status line is reported")
		assert.Contains(t, res.message, "missing")

		res = checkClaudeStatusLineAt(gitRoot, true)
		assert.True(t, res.passed)
		assert.Equal(t, claude.StatusLineCurrent, claude.ClassifyStatusLine(readLocalSettingsMap(t, gitRoot)))
		assert.True(t, checkClaudeStatusLineAt(gitRoot, false).passed)
	})

	t.Run("stale ox command is repaired", func(t *testing.T) {
		isolateUserConfig(t)
		gitRoot := t.TempDir()
		require.NoError(t, InstallProjectClaudeHooks(gitRoot))
		setLocalStatusLine(t, gitRoot, `{"type":"command","command":"ox statusline"}`)

		assert.False(t, checkClaudeStatusLineAt(gitRoot, false).passed)
		assert.True(t, checkClaudeStatusLineAt(gitRoot, true).passed)
		assert.Equal(t, claude.StatusLineCurrent, claude.ClassifyStatusLine(readLocalSettingsMap(t, gitRoot)))
	})

	t.Run("foreign is an informational pass and untouched", func(t *testing.T) {
		isolateUserConfig(t)
		gitRoot := t.TempDir()
		require.NoError(t, InstallProjectClaudeHooks(gitRoot))
		setLocalStatusLine(t, gitRoot, `{"type":"command","command":"starship statusline"}`)

		res := checkClaudeStatusLineAt(gitRoot, true)
		assert.True(t, res.passed)
		assert.False(t, res.warning)
		assert.Contains(t, res.message, "custom statusLine")
		assert.Equal(t, claude.StatusLineForeign, claude.ClassifyStatusLine(readLocalSettingsMap(t, gitRoot)))
	})

	t.Run("ox statusLine in shared settings.json is moved out", func(t *testing.T) {
		isolateUserConfig(t)
		gitRoot := t.TempDir()
		require.NoError(t, InstallProjectClaudeHooks(gitRoot))
		m := readProjectSettingsMap(t, gitRoot)
		m["statusLine"] = json.RawMessage(`{"type":"command","command":"ox statusline"}`)
		data, err := json.Marshal(m)
		require.NoError(t, err)
		writeProjectSettings(t, gitRoot, string(data))

		res := checkClaudeStatusLineAt(gitRoot, false)
		assert.False(t, res.passed)
		assert.Contains(t, res.message, "shared settings.json")

		assert.True(t, checkClaudeStatusLineAt(gitRoot, true).passed)
		assert.NotContains(t, readProjectSettingsMap(t, gitRoot), "statusLine")
		assert.Equal(t, claude.StatusLineCurrent, claude.ClassifyStatusLine(readLocalSettingsMap(t, gitRoot)))
	})

	t.Run("off: absent passes, present is removed", func(t *testing.T) {
		isolateUserConfig(t)
		gitRoot := t.TempDir()
		require.NoError(t, InstallProjectClaudeHooks(gitRoot))
		require.NoError(t, SetConfigValue("statusline", "off", ConfigLevelUser, ""))
		// the key is still there: the setting was changed from elsewhere
		setLocalStatusLine(t, gitRoot, `{"type":"command","command":"ox statusline"}`)

		res := checkClaudeStatusLineAt(gitRoot, false)
		assert.False(t, res.passed)
		assert.Contains(t, res.message, "off but still installed")

		res = checkClaudeStatusLineAt(gitRoot, true)
		assert.True(t, res.passed)
		_, localMap, err := readSettingsFileRaw(getLocalClaudeSettingsPath(gitRoot))
		require.NoError(t, err)
		assert.Equal(t, claude.StatusLineMissing, claude.ClassifyStatusLine(localMap))

		res = checkClaudeStatusLineAt(gitRoot, false)
		assert.True(t, res.passed)
		assert.Contains(t, res.message, "status line off")
	})

	t.Run("no ox hooks is skipped", func(t *testing.T) {
		isolateUserConfig(t)
		gitRoot := t.TempDir()
		writeProjectSettings(t, gitRoot, `{"model":"opus"}`)
		assert.True(t, checkClaudeStatusLineAt(gitRoot, true).skipped)
		assert.NotContains(t, readProjectSettingsMap(t, gitRoot), "statusLine", "doctor does not add ox to a repo without ox hooks")
		assert.NoFileExists(t, getLocalClaudeSettingsPath(gitRoot))
	})
}
