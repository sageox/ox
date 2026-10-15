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

	"github.com/sageox/ox/internal/pet"
	"github.com/sageox/ox/internal/pet/pettest"
	"github.com/sageox/ox/internal/version"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClaudeScript stands in for the claude CLI: it logs each invocation and
// keeps marketplace/install state in files, answering the --json lists the
// way the real CLI does. `plugin validate` checks the mod folder really holds
// a manifest and a hooks module, so a write that silently failed is caught.
const fakeClaudeScript = `#!/bin/sh
echo "$*" >> "$FAKE_CLAUDE_STATE/log"
case "$*" in
  "plugin validate "*)
    [ -f "$3/.claude-plugin/plugin.json" ] && [ -f "$3/hooks/register.tsx" ] && [ -f "$3/hooks/frames.ts" ] || { echo "missing files"; exit 1; } ;;
  "plugin marketplace list --json")
    if [ -f "$FAKE_CLAUDE_STATE/marketplace" ]; then echo '[{"name":"ox-cli-pets"}]'; else echo '[]'; fi ;;
  "plugin marketplace add "*) : > "$FAKE_CLAUDE_STATE/marketplace" ;;
  "plugin marketplace remove "*) rm -f "$FAKE_CLAUDE_STATE/marketplace" ;;
  "plugin list --json")
    if [ -f "$FAKE_CLAUDE_STATE/installed" ]; then echo '[{"id":"ox-cli-oxy@ox-cli-pets","version":"'"$(cat "$FAKE_CLAUDE_STATE/installed")"'"}]'; else echo '[]'; fi ;;
  "plugin install "*) grep -o '"version": *"[^"]*"' "$FAKE_CLAUDE_MOD/.claude-plugin/plugin.json" | sed 's/.*"\([^"]*\)"$/\1/' > "$FAKE_CLAUDE_STATE/installed" ;;
  "plugin uninstall "*) rm -f "$FAKE_CLAUDE_STATE/installed" ;;
esac
`

type petEnv struct {
	state string
	dir   string
}

// setupPetEnv isolates XDG data and puts a fake claude first on PATH.
func setupPetEnv(t *testing.T, withClaude bool) petEnv {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake claude is a POSIX shell script")
	}
	root := t.TempDir()
	// a tiny sheet: writing the mod takes milliseconds, not a full render
	orig := newPetInstaller
	newPetInstaller = func() pet.Installer {
		in := orig()
		in.Frames = pettest.Frames
		return in
	}
	t.Cleanup(func() { newPetInstaller = orig })
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("OX_XDG_DISABLE", "")
	env := petEnv{state: filepath.Join(root, "state"), dir: pet.Dir()}
	require.NoError(t, os.MkdirAll(env.state, 0o755))
	t.Setenv("FAKE_CLAUDE_STATE", env.state)
	t.Setenv("FAKE_CLAUDE_MOD", env.dir)

	bin := filepath.Join(root, "bin")
	require.NoError(t, os.MkdirAll(bin, 0o755))
	if withClaude {
		require.NoError(t, os.WriteFile(filepath.Join(bin, "claude"), []byte(fakeClaudeScript), 0o755))
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	if !withClaude {
		if _, err := exec.LookPath("claude"); err == nil {
			t.Skip("a claude CLI is installed system-wide; can't simulate its absence")
		}
	}
	return env
}

func (e petEnv) log(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.state, "log"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func runPet(t *testing.T, sub string) (string, string, error) {
	t.Helper()
	target := petEnableCmd
	switch sub {
	case "disable":
		target = petDisableCmd
	case "status":
		target = petStatusCmd
	}
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

// TestPetEnable_InstallsOxyIntoClaudeCode drives `ox pet enable` against a
// claude CLI on PATH and checks the customer outcome: the mod is on disk at
// the ox data path, stamped with this ox's version, and Claude Code was asked
// to validate, add the marketplace, and install ox-cli-oxy@ox-cli-pets — in order.
// Failure prevented: `ox pet enable` reports success while Claude Code never
// got the plugin, so Oxy never appears.
func TestPetEnable_InstallsOxyIntoClaudeCode(t *testing.T) {
	env := setupPetEnv(t, true)

	out, _, err := runPet(t, "enable")
	require.NoError(t, err)
	assert.Contains(t, out, "thinking line")

	assert.Equal(t, []string{
		"plugin validate " + env.dir,
		"plugin marketplace list --json",
		"plugin marketplace add " + env.dir,
		"plugin list --json",
		"plugin install ox-cli-oxy@ox-cli-pets --scope user",
	}, env.log(t))
	installed, err := os.ReadFile(filepath.Join(env.state, "installed"))
	require.NoError(t, err)
	assert.Equal(t, version.Version, strings.TrimSpace(string(installed)))

	status, _, err := runPet(t, "status")
	require.NoError(t, err)
	assert.Contains(t, status, "installed in Claude Code")
}

// TestPetDisable_RemovesOxyFromClaudeCode verifies disable uninstalls the
// plugin, drops the marketplace, and deletes the mod folder.
// Failure prevented: Oxy keeps loading after the coworker turned it off.
func TestPetDisable_RemovesOxyFromClaudeCode(t *testing.T) {
	env := setupPetEnv(t, true)
	_, _, err := runPet(t, "enable")
	require.NoError(t, err)

	_, _, err = runPet(t, "disable")
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(env.state, "installed"))
	assert.NoFileExists(t, filepath.Join(env.state, "marketplace"))
	assert.NoDirExists(t, env.dir)

	status, _, err := runPet(t, "status")
	require.NoError(t, err)
	assert.Contains(t, status, "isn't enabled")
}

// TestPetEnable_WithoutClaudeCLI verifies a machine without the claude CLI
// still gets the mod, plus the two slash commands that finish the install.
func TestPetEnable_WithoutClaudeCLI(t *testing.T) {
	env := setupPetEnv(t, false)

	out, errOut, err := runPet(t, "enable")
	require.NoError(t, err)
	assert.Contains(t, errOut, "isn't on PATH")
	assert.Contains(t, out, "/plugin marketplace add "+env.dir)
	assert.Contains(t, out, "/plugin install ox-cli-oxy@ox-cli-pets")
	assert.FileExists(t, filepath.Join(env.dir, ".claude-plugin", "plugin.json"))
}

// runPetJSON runs a pet subcommand with the global --json flag set.
func runPetJSON(t *testing.T, sub string) (string, error) {
	t.Helper()
	target := map[string]*cobra.Command{"enable": petEnableCmd, "disable": petDisableCmd, "status": petStatusCmd}[sub]
	target.InheritedFlags() // merges the root's persistent flags, --json among them
	require.NoError(t, target.Flags().Set("json", "true"))
	t.Cleanup(func() { _ = target.Flags().Set("json", "false") })
	out, _, err := runPet(t, sub)
	return out, err
}

// TestPet_JSONReportsTheOutcome verifies each subcommand's --json payload is
// the installer's result, for an AI coworker or script to read.
func TestPet_JSONReportsTheOutcome(t *testing.T) {
	env := setupPetEnv(t, true)
	for _, step := range []struct {
		sub       string
		installed bool
		files     bool
	}{{"enable", true, true}, {"status", true, true}, {"disable", false, false}} {
		out, err := runPetJSON(t, step.sub)
		require.NoError(t, err, step.sub)
		var res pet.Result
		require.NoError(t, json.Unmarshal([]byte(out), &res), step.sub)
		assert.Equal(t, step.installed, res.Installed, step.sub)
		assert.Equal(t, env.dir, res.Dir, step.sub)
	}
}

// TestPetStatus_SaysWhatToDo verifies status names the next step for each
// half-finished state.
// Failure prevented: a coworker sees "Oxy isn't working" with no clue whether
// to enable, refresh, or install.
func TestPetStatus_SaysWhatToDo(t *testing.T) {
	env := setupPetEnv(t, true)
	_, _, err := runPet(t, "enable")
	require.NoError(t, err)

	// Claude Code still records an older ox's version
	require.NoError(t, os.WriteFile(filepath.Join(env.state, "installed"), []byte("0.0.1\n"), 0o644))
	out, _, err := runPet(t, "status")
	require.NoError(t, err)
	assert.Contains(t, out, "Run `ox pet enable` to refresh it")

	// files written but Claude Code never installed them
	require.NoError(t, os.Remove(filepath.Join(env.state, "installed")))
	out, _, err = runPet(t, "status")
	require.NoError(t, err)
	assert.Contains(t, out, "hasn't installed them")
}

// TestPet_ClaudeFailuresSurface verifies a failing claude CLI turns into a
// command error naming the step, for enable, disable and status alike.
func TestPet_ClaudeFailuresSurface(t *testing.T) {
	env := setupPetEnv(t, true)
	_, _, err := runPet(t, "enable")
	require.NoError(t, err)
	failing := "#!/bin/sh\necho broken; exit 2\n"
	bin := filepath.Dir(firstOnPath(t, "claude"))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "claude"), []byte(failing), 0o755))
	for _, sub := range []string{"enable", "status", "disable"} {
		_, _, err := runPet(t, sub)
		assert.Error(t, err, sub)
	}
	_ = env
}

// TestPetDisable_WithoutClaudeCLI verifies disable without the claude CLI
// removes the files and prints the two commands left to run.
func TestPetDisable_WithoutClaudeCLI(t *testing.T) {
	env := setupPetEnv(t, false)
	_, _, err := runPet(t, "enable")
	require.NoError(t, err)
	out, _, err := runPet(t, "disable")
	require.NoError(t, err)
	assert.NoDirExists(t, env.dir)
	assert.Contains(t, out, "/plugin uninstall ox-cli-oxy@ox-cli-pets")
}

func firstOnPath(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	require.NoError(t, err)
	return p
}
