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

	"github.com/pelletier/go-toml/v2"
	"github.com/sageox/ox/internal/pet"
	"github.com/sageox/ox/internal/pet/pettest"
	"github.com/sageox/ox/internal/version"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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
	// codexHome is $CODEX_HOME; it does not exist until a test creates it,
	// so Codex is not detected by default.
	codexHome string
}

// setupPetEnv isolates XDG data and the Codex home, and puts a fake claude
// first on PATH.
func setupPetEnv(t *testing.T, withClaude bool) petEnv {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake claude is a POSIX shell script")
	}
	root := t.TempDir()
	// tiny sheets: writing the pets takes milliseconds, not a full render
	orig := newPetInstaller
	newPetInstaller = func() pet.Installer {
		in := orig()
		in.Frames = pettest.Frames
		return in
	}
	origCodex := newCodexPetInstaller
	newCodexPetInstaller = func() pet.CodexInstaller {
		in := origCodex()
		in.Sheet = pettest.CodexSheet
		return in
	}
	t.Cleanup(func() {
		newPetInstaller = orig
		newCodexPetInstaller = origCodex
	})
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	env := petEnv{state: filepath.Join(root, "state"), dir: pet.Dir(), codexHome: filepath.Join(root, "codex")}
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

// runPet runs a pet subcommand, with one --host flag per host given.
func runPet(t *testing.T, sub string, hosts ...string) (string, string, error) {
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
	for _, h := range hosts {
		require.NoError(t, target.Flags().Set("host", h))
	}
	defer resetHostFlag(target)
	t.Cleanup(func() {
		target.SetOut(nil)
		target.SetErr(nil)
	})
	err := target.RunE(target, nil)
	return out.String(), errOut.String(), err
}

func resetHostFlag(c *cobra.Command) {
	f := c.Flags().Lookup("host")
	_ = f.Value.(pflag.SliceValue).Replace(nil)
	f.Changed = false
}

// writeCodexConfig puts a config.toml in the Codex home, which also makes
// Codex detected.
func (e petEnv) writeCodexConfig(t *testing.T, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(e.codexHome, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(e.codexHome, "config.toml"), []byte(content), 0o600))
}

func (e petEnv) codexPet(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.codexHome, "config.toml"))
	if os.IsNotExist(err) {
		return ""
	}
	require.NoError(t, err)
	var cfg struct {
		TUI struct {
			Pet string `toml:"pet"`
		} `toml:"tui"`
	}
	require.NoError(t, toml.Unmarshal(data, &cfg))
	return cfg.TUI.Pet
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

	out, errOut, err := runPet(t, "enable", petHostClaude)
	require.NoError(t, err)
	assert.Contains(t, errOut, "isn't on PATH")
	assert.Contains(t, out, "/plugin marketplace add "+env.dir)
	assert.Contains(t, out, "/plugin install ox-cli-oxy@ox-cli-pets")
	assert.FileExists(t, filepath.Join(env.dir, ".claude-plugin", "plugin.json"))
}

// runPetJSON runs a pet subcommand with the global --json flag set.
func runPetJSON(t *testing.T, sub string, hosts ...string) (string, error) {
	t.Helper()
	target := map[string]*cobra.Command{"enable": petEnableCmd, "disable": petDisableCmd, "status": petStatusCmd}[sub]
	target.InheritedFlags() // merges the root's persistent flags, --json among them
	require.NoError(t, target.Flags().Set("json", "true"))
	defer func() { _ = target.Flags().Set("json", "false") }()
	out, _, err := runPet(t, sub, hosts...)
	return out, err
}

// petJSON is the --json document, decoded strictly enough to tell a null
// host from an empty one.
type petJSON struct {
	ClaudeCode *pet.Result      `json:"claude_code"`
	Codex      *pet.CodexResult `json:"codex"`
	Guidance   string           `json:"guidance"`
}

func decodePetJSON(t *testing.T, out string) petJSON {
	t.Helper()
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(out), &raw), out)
	for _, key := range []string{"claude_code", "codex", "guidance"} {
		assert.Contains(t, raw, key, "the --json document always has %q", key)
	}
	var doc petJSON
	require.NoError(t, json.Unmarshal([]byte(out), &doc))
	return doc
}

// TestPet_JSONReportsTheOutcome verifies each subcommand prints one --json
// document holding each host's result (null for a host not acted on) and
// guidance an AI coworker can relay.
func TestPet_JSONReportsTheOutcome(t *testing.T) {
	env := setupPetEnv(t, true)
	env.writeCodexConfig(t, "")
	for _, step := range []struct {
		sub       string
		installed bool
		codexPet  bool
	}{{"enable", true, true}, {"status", true, true}, {"disable", false, false}} {
		out, err := runPetJSON(t, step.sub)
		require.NoError(t, err, step.sub)
		doc := decodePetJSON(t, out)
		require.NotNil(t, doc.ClaudeCode, step.sub)
		require.NotNil(t, doc.Codex, step.sub)
		assert.Equal(t, step.installed, doc.ClaudeCode.Installed, step.sub)
		assert.Equal(t, env.dir, doc.ClaudeCode.Dir, step.sub)
		assert.Equal(t, step.codexPet, doc.Codex.Selected, step.sub)
		assert.Equal(t, pet.CodexDir(env.codexHome), doc.Codex.Dir, step.sub)
		assert.Contains(t, doc.Guidance, "Claude Code", step.sub)
	}

	// one host named: the other is null, not an empty object
	_, err := runPetJSON(t, "enable", petHostCodex)
	require.NoError(t, err)
	out, err := runPetJSON(t, "status", petHostCodex)
	require.NoError(t, err)
	doc := decodePetJSON(t, out)
	assert.Nil(t, doc.ClaudeCode)
	require.NotNil(t, doc.Codex)
	assert.Contains(t, doc.Guidance, "Oxy is Codex's current terminal pet.", "an up-to-date Codex pet still gets guidance")
	assert.Contains(t, doc.Guidance, "Claude Code options", "guidance says what Codex decides on its own")
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
	_, _, err := runPet(t, "enable", petHostClaude)
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

// TestPet_HostSelectsWhereOxyGoes drives `ox pet enable` with each --host
// combination, and with none, on a machine with both a claude CLI and a Codex
// home, and checks which host ends up with Oxy.
// Failure prevented: `--host codex` also reinstalls the Claude Code plugin
// (or the reverse), or a plain `ox pet enable` skips a host the coworker has.
func TestPet_HostSelectsWhereOxyGoes(t *testing.T) {
	tests := []struct {
		name       string
		hosts      []string
		wantClaude bool
		wantCodex  bool
	}{
		{name: "default is every detected host", wantClaude: true, wantCodex: true},
		{name: "claude-code only", hosts: []string{petHostClaude}, wantClaude: true},
		{name: "codex only", hosts: []string{petHostCodex}, wantCodex: true},
		{name: "both named", hosts: []string{petHostCodex, petHostClaude}, wantClaude: true, wantCodex: true},
		{name: "repeated host", hosts: []string{petHostCodex, petHostCodex}, wantCodex: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := setupPetEnv(t, true)
			env.writeCodexConfig(t, "model = \"gpt-5\"\n")

			out, _, err := runPet(t, "enable", tt.hosts...)
			require.NoError(t, err)

			if tt.wantClaude {
				assert.Contains(t, out, "thinking line")
				assert.FileExists(t, filepath.Join(env.state, "installed"))
			} else {
				assert.NotContains(t, out, "thinking line")
				assert.Empty(t, env.log(t), "claude must not be called")
				assert.NoDirExists(t, env.dir)
			}
			if tt.wantCodex {
				assert.Contains(t, out, "Codex's terminal pet")
				assert.FileExists(t, filepath.Join(pet.CodexDir(env.codexHome), "pet.json"))
				assert.Equal(t, pet.CodexPetSelector, env.codexPet(t))
			} else {
				assert.NotContains(t, out, "Codex")
				assert.NoDirExists(t, pet.CodexDir(env.codexHome))
				assert.Empty(t, env.codexPet(t))
			}
		})
	}
}

// TestPetEnable_CodexOnly verifies a Codex-only machine gets Oxy's pet folder
// and has him selected in config.toml, with the rest of the config kept.
// Failure prevented: Oxy's files land but Codex keeps showing another pet, or
// enabling him rewrites settings the coworker set by hand.
func TestPetEnable_CodexOnly(t *testing.T) {
	env := setupPetEnv(t, false)
	env.writeCodexConfig(t, "model = \"gpt-5\"\n\n[tui]\npet = \"custom:other\"\n")

	out, _, err := runPet(t, "enable", petHostCodex)
	require.NoError(t, err)
	assert.Contains(t, out, "Oxy is Codex's terminal pet.")
	assert.Contains(t, out, "/pets")

	got, err := pet.CodexWrittenVersion(pet.CodexDir(env.codexHome))
	require.NoError(t, err)
	assert.Equal(t, version.Version, got)
	assert.Equal(t, pet.CodexPetSelector, env.codexPet(t))
	cfg, err := os.ReadFile(filepath.Join(env.codexHome, "config.toml"))
	require.NoError(t, err)
	assert.Contains(t, string(cfg), `model = "gpt-5"`)

	status, _, err := runPet(t, "status")
	require.NoError(t, err)
	assert.Contains(t, status, "Oxy is Codex's terminal pet.")

	_, _, err = runPet(t, "disable")
	require.NoError(t, err)
	assert.NoDirExists(t, pet.CodexDir(env.codexHome))
	assert.Empty(t, env.codexPet(t))
}

// TestPet_DetectsHosts verifies the default host set follows what is on the
// machine: a CLI on PATH or the host's folder.
func TestPet_DetectsHosts(t *testing.T) {
	t.Run("nothing found points at --host", func(t *testing.T) {
		env := setupPetEnv(t, false)
		out, _, err := runPet(t, "enable")
		require.NoError(t, err)
		assert.Contains(t, out, "--host")
		assert.NoDirExists(t, env.dir)
		assert.NoDirExists(t, env.codexHome)

		js, err := runPetJSON(t, "status")
		require.NoError(t, err)
		doc := decodePetJSON(t, js)
		assert.Nil(t, doc.ClaudeCode)
		assert.Nil(t, doc.Codex)
		assert.Contains(t, doc.Guidance, "--host")
	})
	t.Run("codex on PATH", func(t *testing.T) {
		setupPetEnv(t, false)
		bin := filepath.SplitList(os.Getenv("PATH"))[0]
		require.NoError(t, os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\n"), 0o755))
		assert.Equal(t, []string{petHostCodex}, detectPetHosts())
	})
	t.Run("codex home exists", func(t *testing.T) {
		env := setupPetEnv(t, true)
		require.NoError(t, os.MkdirAll(env.codexHome, 0o755))
		assert.Equal(t, []string{petHostClaude, petHostCodex}, detectPetHosts())
	})
	t.Run("claude mod folder without the claude CLI", func(t *testing.T) {
		env := setupPetEnv(t, false)
		require.NoError(t, os.MkdirAll(env.dir, 0o755))
		assert.Equal(t, []string{petHostClaude}, detectPetHosts())
	})
}

// TestPet_UnknownHostIsRejected verifies a mistyped host fails and names the
// accepted ones, instead of silently doing nothing.
func TestPet_UnknownHostIsRejected(t *testing.T) {
	env := setupPetEnv(t, true)
	for _, sub := range []string{"enable", "disable", "status"} {
		_, _, err := runPet(t, sub, "claude")
		require.Error(t, err, sub)
		assert.Contains(t, err.Error(), `"claude"`)
		assert.Contains(t, err.Error(), "claude-code or codex")
	}
	assert.Empty(t, env.log(t))
}
