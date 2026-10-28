package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/constants"
	claude "github.com/sageox/ox/internal/hooks/claude"
)

// isolateUserConfig points the user config at a scratch file so statusline
// on/off in a test never reads or writes the developer's own settings.
func isolateUserConfig(t *testing.T) {
	t.Helper()
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	// applyStatuslineSetting resolves the git root from the working directory;
	// keep it away from the developer's real checkout.
	t.Chdir(t.TempDir())
}

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

func readLocalSettingsMap(t *testing.T, gitRoot string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(getLocalClaudeSettingsPath(gitRoot))
	require.NoError(t, err)
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &m))
	return m
}

func writeLocalSettings(t *testing.T, gitRoot, body string) {
	t.Helper()
	path := getLocalClaudeSettingsPath(gitRoot)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

// TestInstallProjectClaudeHooks_StatusLine covers the installer's statusLine
// merge into the coworker's settings.local.json. Failure prevented: ox
// overwrites a coworker's own status bar, drops other local keys, writes the
// per-coworker row into the committed settings.json, or never installs its
// activity line at all.
func TestInstallProjectClaudeHooks_StatusLine(t *testing.T) {
	foreign := `{"type":"command","command":"~/bin/my-status.sh","padding":1}`
	tests := []struct {
		name        string
		existing    string // "" = no settings.local.json
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
			isolateUserConfig(t)
			gitRoot := t.TempDir()
			if tt.existing != "" {
				writeLocalSettings(t, gitRoot, tt.existing)
			}
			require.NoError(t, InstallProjectClaudeHooks(gitRoot))
			first, err := os.ReadFile(getLocalClaudeSettingsPath(gitRoot))
			require.NoError(t, err)

			m := readLocalSettingsMap(t, gitRoot)
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
			assert.NotContains(t, readProjectSettingsMap(t, gitRoot), "statusLine", "the committed settings.json never carries the per-coworker row")
			assert.True(t, HasProjectClaudeHooks(gitRoot), "hooks are still installed alongside")

			// idempotent: a second install changes nothing
			require.NoError(t, InstallProjectClaudeHooks(gitRoot))
			second, err := os.ReadFile(getLocalClaudeSettingsPath(gitRoot))
			require.NoError(t, err)
			assert.Equal(t, string(first), string(second))
		})
	}
}

// TestInstallProjectClaudeHooks_MovesStatusLineOutOfSharedSettings migrates
// an ox statusLine that an earlier build wrote into the committed
// settings.json. Failure prevented: a committed row that forces the status
// line on every coworker, including those who turned it off.
func TestInstallProjectClaudeHooks_MovesStatusLineOutOfSharedSettings(t *testing.T) {
	isolateUserConfig(t)
	gitRoot := t.TempDir()
	writeProjectSettings(t, gitRoot, `{"statusLine":{"type":"command","command":"`+constants.OxStatuslineCommandClaudeCode+`","refreshInterval":15},"model":"opus"}`)
	require.NoError(t, InstallProjectClaudeHooks(gitRoot))

	shared := readProjectSettingsMap(t, gitRoot)
	assert.NotContains(t, shared, "statusLine")
	assert.JSONEq(t, `"opus"`, string(shared["model"]))
	assert.Equal(t, claude.StatusLineCurrent, claude.ClassifyStatusLine(readLocalSettingsMap(t, gitRoot)))

	// a project's own statusLine in settings.json is not ox's to move
	writeProjectSettings(t, gitRoot, `{"statusLine":{"type":"command","command":"starship statusline"}}`)
	require.NoError(t, InstallProjectClaudeHooks(gitRoot))
	assert.Contains(t, string(readProjectSettingsMap(t, gitRoot)["statusLine"]), "starship")
}

// TestInstallProjectClaudeHooks_StatusLineOff installs nothing when the
// coworker has turned the status line off, and removes an existing ox entry.
// Failure prevented: `ox config set statusline off` leaving a statusLine key
// behind, which keeps a blank row in Claude Code.
func TestInstallProjectClaudeHooks_StatusLineOff(t *testing.T) {
	isolateUserConfig(t)
	require.NoError(t, SetConfigValue("statusline", "off", ConfigLevelUser, ""))
	gitRoot := t.TempDir()
	writeLocalSettings(t, gitRoot, `{"statusLine":{"type":"command","command":"ox statusline"},"model":"opus"}`)
	require.NoError(t, InstallProjectClaudeHooks(gitRoot))
	assert.JSONEq(t, `{"model":"opus"}`, string(mustRead(t, getLocalClaudeSettingsPath(gitRoot))))

	// nothing else in the file: it goes away rather than lingering as "{}"
	writeLocalSettings(t, gitRoot, `{"statusLine":{"type":"command","command":"ox statusline"}}`)
	require.NoError(t, InstallProjectClaudeHooks(gitRoot))
	assert.NoFileExists(t, getLocalClaudeSettingsPath(gitRoot))
}

// TestConfigSetStatusline_AppliesToCurrentProject proves `ox config set
// statusline off|on` takes effect in the project it is run from, without a
// doctor run. Failure prevented: the coworker turns the line off and the row
// stays until something else rewrites settings.local.json.
func TestConfigSetStatusline_AppliesToCurrentProject(t *testing.T) {
	isolateUserConfig(t)
	gitRoot := t.TempDir()
	initTestGitRepo(t, gitRoot)
	t.Chdir(gitRoot)
	require.NoError(t, InstallProjectClaudeHooks(gitRoot))
	require.Equal(t, claude.StatusLineCurrent, claude.ClassifyStatusLine(readLocalSettingsMap(t, gitRoot)))

	require.NoError(t, SetConfigValue("statusline", "off", ConfigLevelUser, ""))
	_, localMap, err := readSettingsFileRaw(getLocalClaudeSettingsPath(gitRoot))
	require.NoError(t, err)
	assert.Equal(t, claude.StatusLineMissing, claude.ClassifyStatusLine(localMap), "off removes the key, so Claude Code drops the row")

	require.NoError(t, SetConfigValue("statusline", "on", ConfigLevelUser, ""))
	assert.Equal(t, claude.StatusLineCurrent, claude.ClassifyStatusLine(readLocalSettingsMap(t, gitRoot)))

	require.NoError(t, UnsetConfigValue("statusline", ConfigLevelUser, ""))
	assert.Equal(t, claude.StatusLineCurrent, claude.ClassifyStatusLine(readLocalSettingsMap(t, gitRoot)), "default is on")
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

// TestInstallProjectClaudeHooks_NoUserLevelStatusLine keeps the status line
// out of ~/.claude/settings.json. Failure prevented: ox silently replacing a
// coworker's personal status bar in every project.
func TestInstallProjectClaudeHooks_NoUserLevelStatusLine(t *testing.T) {
	isolateUserConfig(t)
	gitRoot := t.TempDir()
	require.NoError(t, InstallProjectClaudeHooks(gitRoot))
	userPath, err := getClaudeSettingsPath()
	require.NoError(t, err)
	assert.NoFileExists(t, userPath, "installing project hooks must not create user-level settings")
}

// TestCleanupLocalSettingsOxHooks_KeepsStatusLine removes stale ox hooks from
// settings.local.json but leaves the ox statusLine, which lives there on
// purpose. Failure prevented: every install deleting the row it just wrote.
func TestCleanupLocalSettingsOxHooks_KeepsStatusLine(t *testing.T) {
	gitRoot := t.TempDir()
	writeLocalSettings(t, gitRoot, `{"statusLine":{"type":"command","command":"ox statusline"},"model":"opus"}`)
	require.NoError(t, cleanupLocalSettingsOxHooks(gitRoot))
	assert.JSONEq(t, `{"statusLine":{"type":"command","command":"ox statusline"},"model":"opus"}`, string(mustRead(t, getLocalClaudeSettingsPath(gitRoot))))
}
