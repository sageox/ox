package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/constants"
)

func readProjectSettingsMap(t *testing.T, gitRoot string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(getSharedClaudeSettingsPath(gitRoot))
	require.NoError(t, err)
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &m))
	return m
}

func writeProjectSettings(t *testing.T, gitRoot, body string) {
	t.Helper()
	path := getSharedClaudeSettingsPath(gitRoot)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

func statusLineOf(t *testing.T, m map[string]json.RawMessage) map[string]any {
	t.Helper()
	raw, ok := m["statusLine"]
	require.True(t, ok, "statusLine must be present")
	var v map[string]any
	require.NoError(t, json.Unmarshal(raw, &v))
	return v
}

// TestInstallProjectClaudeHooks_StatusLine covers the installer's statusLine
// merge. Failure prevented: ox overwrites a coworker's own status bar, drops
// other settings keys, or never installs its activity line at all.
func TestInstallProjectClaudeHooks_StatusLine(t *testing.T) {
	foreign := `{"type":"command","command":"~/bin/my-status.sh","padding":1}`
	tests := []struct {
		name        string
		existing    string // "" = no settings file
		wantCommand string
		wantForeign bool
	}{
		{name: "fresh project", existing: "", wantCommand: constants.OxStatuslineCommandClaudeCode},
		{name: "keeps other keys", existing: `{"permissions":{"allow":["Bash(ls:*)"]},"model":"opus"}`, wantCommand: constants.OxStatuslineCommandClaudeCode},
		{name: "never overwrites a foreign statusLine", existing: `{"statusLine":` + foreign + `}`, wantForeign: true},
		{name: "upgrades a stale ox statusLine, keeping padding", existing: `{"statusLine":{"type":"command","command":"ox statusline","padding":2}}`, wantCommand: constants.OxStatuslineCommandClaudeCode},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gitRoot := t.TempDir()
			if tt.existing != "" {
				writeProjectSettings(t, gitRoot, tt.existing)
			}
			require.NoError(t, InstallProjectClaudeHooks(gitRoot))
			first, err := os.ReadFile(getSharedClaudeSettingsPath(gitRoot))
			require.NoError(t, err)

			m := readProjectSettingsMap(t, gitRoot)
			sl := statusLineOf(t, m)
			if tt.wantForeign {
				assert.JSONEq(t, foreign, string(m["statusLine"]))
			} else {
				assert.Equal(t, "command", sl["type"])
				assert.Equal(t, tt.wantCommand, sl["command"])
				assert.EqualValues(t, constants.OxStatuslineRefreshSeconds, sl["refreshInterval"])
			}
			if tt.name == "keeps other keys" {
				assert.JSONEq(t, `{"allow":["Bash(ls:*)"]}`, string(m["permissions"]))
				assert.JSONEq(t, `"opus"`, string(m["model"]))
			}
			if tt.name == "upgrades a stale ox statusLine, keeping padding" {
				assert.EqualValues(t, 2, sl["padding"])
			}
			assert.True(t, HasProjectClaudeHooks(gitRoot), "hooks are still installed alongside")

			// idempotent: a second install changes nothing
			require.NoError(t, InstallProjectClaudeHooks(gitRoot))
			second, err := os.ReadFile(getSharedClaudeSettingsPath(gitRoot))
			require.NoError(t, err)
			assert.Equal(t, string(first), string(second))
		})
	}
}

// TestInstallProjectClaudeHooks_NoUserLevelStatusLine keeps the status line
// out of ~/.claude/settings.json. Failure prevented: ox silently replacing a
// coworker's personal status bar in every project.
func TestInstallProjectClaudeHooks_NoUserLevelStatusLine(t *testing.T) {
	gitRoot := t.TempDir()
	require.NoError(t, InstallProjectClaudeHooks(gitRoot))
	userPath, err := getClaudeSettingsPath()
	require.NoError(t, err)
	assert.NoFileExists(t, userPath, "installing project hooks must not create user-level settings")
}

// TestCleanupLocalSettingsOxHooks_RemovesOxStatusLine removes an ox statusLine
// left in settings.local.json, which would shadow the shared one, and keeps a
// foreign one. Failure prevented: a stale local copy pinning an old command.
func TestCleanupLocalSettingsOxHooks_RemovesOxStatusLine(t *testing.T) {
	gitRoot := t.TempDir()
	localPath := getLocalClaudeSettingsPath(gitRoot)
	require.NoError(t, os.MkdirAll(filepath.Dir(localPath), 0o755))

	require.NoError(t, os.WriteFile(localPath, []byte(`{"statusLine":{"type":"command","command":"ox statusline"},"model":"opus"}`), 0o644))
	require.NoError(t, cleanupLocalSettingsOxHooks(gitRoot))
	data, err := os.ReadFile(localPath)
	require.NoError(t, err)
	assert.JSONEq(t, `{"model":"opus"}`, string(data))

	require.NoError(t, os.WriteFile(localPath, []byte(`{"statusLine":{"type":"command","command":"starship"}}`), 0o644))
	require.NoError(t, cleanupLocalSettingsOxHooks(gitRoot))
	data, err = os.ReadFile(localPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "starship")
}
