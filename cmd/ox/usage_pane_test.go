package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/claudemod"
	"github.com/sageox/ox/internal/usagepane"
	"github.com/sageox/ox/internal/version"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeUsagePaneClaudeScript stands in for the claude CLI for the usage pane:
// it logs each invocation and keeps marketplace/install state in files,
// answering the --json lists the way the real CLI does. `plugin validate`
// checks the mod folder really holds a manifest. With an "old" marker file it
// behaves like a claude CLI that predates the plugin subcommand.
const fakeUsagePaneClaudeScript = `#!/bin/sh
echo "$*" >> "$FAKE_CLAUDE_STATE/log"
if [ -f "$FAKE_CLAUDE_STATE/old" ]; then echo "error: unknown command 'plugin'"; exit 1; fi
case "$*" in
  "plugin validate "*)
    [ -f "$3/.claude-plugin/plugin.json" ] || { echo "missing files"; exit 1; } ;;
  "plugin marketplace list --json")
    if [ -f "$FAKE_CLAUDE_STATE/marketplace" ]; then echo '[{"name":"ox-cli-usage"}]'; else echo '[]'; fi ;;
  "plugin marketplace add "*) : > "$FAKE_CLAUDE_STATE/marketplace" ;;
  "plugin marketplace remove "*) rm -f "$FAKE_CLAUDE_STATE/marketplace" ;;
  "plugin list --json")
    if [ -f "$FAKE_CLAUDE_STATE/installed" ]; then echo '[{"id":"ox-cli-usage-pane@ox-cli-usage","version":"'"$(cat "$FAKE_CLAUDE_STATE/installed")"'"}]'; else echo '[]'; fi ;;
  "plugin install "*) grep -o '"version": *"[^"]*"' "$FAKE_CLAUDE_MOD/.claude-plugin/plugin.json" | sed 's/.*"\([^"]*\)"$/\1/' > "$FAKE_CLAUDE_STATE/installed" ;;
  "plugin uninstall "*) rm -f "$FAKE_CLAUDE_STATE/installed" ;;
esac
`

type usagePaneEnv struct {
	state string
	dir   string
}

// setupUsagePaneEnv isolates XDG data and puts a fake claude first on PATH.
func setupUsagePaneEnv(t *testing.T, withClaude bool) usagePaneEnv {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake claude is a POSIX shell script")
	}
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("OX_XDG_DISABLE", "")
	env := usagePaneEnv{state: filepath.Join(root, "state"), dir: usagepane.Dir()}
	require.NoError(t, os.MkdirAll(env.state, 0o755))
	t.Setenv("FAKE_CLAUDE_STATE", env.state)
	t.Setenv("FAKE_CLAUDE_MOD", env.dir)

	bin := filepath.Join(root, "bin")
	require.NoError(t, os.MkdirAll(bin, 0o755))
	if withClaude {
		require.NoError(t, os.WriteFile(filepath.Join(bin, "claude"), []byte(fakeUsagePaneClaudeScript), 0o755))
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	if !withClaude {
		if _, err := exec.LookPath("claude"); err == nil {
			t.Skip("a claude CLI is installed system-wide; can't simulate its absence")
		}
	}
	return env
}

func (e usagePaneEnv) log(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.state, "log"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

var usagePaneSubcommands = map[string]*cobra.Command{
	"enable":  usagePaneEnableCmd,
	"disable": usagePaneDisableCmd,
	"status":  usagePaneStatusCmd,
}

func runUsagePane(t *testing.T, sub string) (string, string, error) {
	t.Helper()
	target := usagePaneSubcommands[sub]
	var out, errOut bytes.Buffer
	target.SetOut(&out)
	target.SetErr(&errOut)
	target.SetContext(context.Background())
	t.Cleanup(func() {
		target.SetOut(nil)
		target.SetErr(nil)
	})
	err := target.RunE(target, nil)
	return out.String(), errOut.String(), err
}

// TestUsagePaneEnable_InstallsIntoClaudeCode drives `ox usage pane enable`
// against a claude CLI on PATH: the mod lands at the ox data path stamped
// with this ox's version, and Claude Code was asked to validate, add the
// marketplace, and install ox-cli-usage-pane@ox-cli-usage — in order.
// Failure prevented: enable reports success while Claude Code never got the
// plugin, so /ox-usage does nothing.
func TestUsagePaneEnable_InstallsIntoClaudeCode(t *testing.T) {
	env := setupUsagePaneEnv(t, true)

	out, _, err := runUsagePane(t, "enable")
	require.NoError(t, err)
	assert.Contains(t, out, "/reload-plugins")
	assert.Contains(t, out, "/ox-usage")

	assert.Equal(t, []string{
		"plugin validate " + env.dir,
		"plugin marketplace list --json",
		"plugin marketplace add " + env.dir,
		"plugin list --json",
		"plugin install ox-cli-usage-pane@ox-cli-usage --scope user",
	}, env.log(t))
	installed, err := os.ReadFile(filepath.Join(env.state, "installed"))
	require.NoError(t, err)
	assert.Equal(t, version.Version, strings.TrimSpace(string(installed)))

	status, _, err := runUsagePane(t, "status")
	require.NoError(t, err)
	assert.Contains(t, status, "installed in Claude Code")
}

// TestUsagePaneDisable_RemovesFromClaudeCode verifies disable uninstalls the
// plugin, drops the marketplace, and deletes the mod folder.
// Failure prevented: the pane keeps loading after the coworker turned it off.
func TestUsagePaneDisable_RemovesFromClaudeCode(t *testing.T) {
	env := setupUsagePaneEnv(t, true)
	_, _, err := runUsagePane(t, "enable")
	require.NoError(t, err)

	_, _, err = runUsagePane(t, "disable")
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(env.state, "installed"))
	assert.NoFileExists(t, filepath.Join(env.state, "marketplace"))
	assert.NoDirExists(t, env.dir)

	status, _, err := runUsagePane(t, "status")
	require.NoError(t, err)
	assert.Contains(t, status, "isn't enabled")
}

// TestUsagePaneStatus_SaysWhatToDo verifies status names the next step for
// each half-finished state.
func TestUsagePaneStatus_SaysWhatToDo(t *testing.T) {
	env := setupUsagePaneEnv(t, true)
	_, _, err := runUsagePane(t, "enable")
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(env.state, "installed"), []byte("0.0.1\n"), 0o644))
	out, _, err := runUsagePane(t, "status")
	require.NoError(t, err)
	assert.Contains(t, out, "Run `ox usage pane enable` to refresh it")

	require.NoError(t, os.Remove(filepath.Join(env.state, "installed")))
	out, _, err = runUsagePane(t, "status")
	require.NoError(t, err)
	assert.Contains(t, out, "hasn't installed them")
}

// TestUsagePaneEnable_WithoutClaudeCLI verifies a machine without the claude
// CLI still gets the mod, plus the two slash commands that finish the install.
func TestUsagePaneEnable_WithoutClaudeCLI(t *testing.T) {
	env := setupUsagePaneEnv(t, false)

	out, errOut, err := runUsagePane(t, "enable")
	require.NoError(t, err)
	assert.Contains(t, errOut, "isn't on PATH")
	assert.Contains(t, out, "/plugin marketplace add "+env.dir)
	assert.Contains(t, out, "/plugin install ox-cli-usage-pane@ox-cli-usage")
	assert.FileExists(t, filepath.Join(env.dir, ".claude-plugin", "plugin.json"))

	out, _, err = runUsagePane(t, "disable")
	require.NoError(t, err)
	assert.NoDirExists(t, env.dir)
	assert.Contains(t, out, "/plugin uninstall ox-cli-usage-pane@ox-cli-usage")
	assert.Contains(t, out, "/plugin marketplace remove ox-cli-usage")
}

// TestUsagePane_JSONReportsTheOutcome verifies each subcommand's --json
// payload is the installer's result, for an AI coworker or script to read.
func TestUsagePane_JSONReportsTheOutcome(t *testing.T) {
	env := setupUsagePaneEnv(t, true)
	for _, step := range []struct {
		sub       string
		installed bool
		files     bool
	}{{"enable", true, true}, {"status", true, true}, {"disable", false, false}} {
		target := usagePaneSubcommands[step.sub]
		target.InheritedFlags() // merges the root's persistent flags, --json among them
		require.NoError(t, target.Flags().Set("json", "true"))
		out, _, err := runUsagePane(t, step.sub)
		_ = target.Flags().Set("json", "false")
		require.NoError(t, err, step.sub)

		var res claudemod.Result
		require.NoError(t, json.Unmarshal([]byte(out), &res), step.sub)
		assert.Equal(t, step.installed, res.Installed, step.sub)
		assert.Equal(t, step.files, res.FilesPresent, step.sub)
		assert.Equal(t, env.dir, res.Dir, step.sub)
		assert.Equal(t, usagepane.PluginRef, res.Plugin, step.sub)
		assert.True(t, res.ClaudeFound, step.sub)
	}
}

// TestUsagePane_HelpNamesThePluginAndCommands verifies the long help tells a
// coworker where the pane comes from and how to open and close it.
func TestUsagePane_HelpNamesThePluginAndCommands(t *testing.T) {
	assert.Contains(t, usagePaneCmd.Long, usagepane.PluginRef)
	assert.Contains(t, usagePaneCmd.Long, "/ox-usage opens it")
	assert.Contains(t, usagePaneCmd.Long, "/ox-usage off closes it")
	assert.Equal(t, usageCmd, usagePaneCmd.Parent())
}
