package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/constants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExtractOxCommands_AgentHookFallback proves the off-PATH diagnostic added
// to every hook command's else-branch (internal/constants/agent.go's
// oxNotOnPathFallback) does not introduce a false positive in the doctor's
// hook-command validator. Before this test existed, the constraint was
// checked by hand; this pins it so a future edit to the fallback text can't
// silently reintroduce a false "invalid command" warning.
func TestExtractOxCommands_AgentHookFallback(t *testing.T) {
	commands := []string{
		constants.OxPrimeCommand,
		constants.OxPrimeCommandClaudeCode,
		constants.OxPrimeCommandClaudeCodeIdempotent,
		constants.OxPrimeCommandGemini,
		constants.OxPrimeCommandAmp,
		constants.OxPrimeCommandPi,
		constants.HookCommand(constants.OxHookCommandClaudeCodeTemplate, "SessionStart"),
		constants.HookCommand(constants.OxHookCommandCodexTemplate, "SessionStart"),
		constants.HookCommand(constants.OxHookCommandGeminiTemplate, "SessionStart"),
	}

	for _, cmd := range commands {
		extracted := extractOxCommands(cmd)
		require.NotEmpty(t, extracted, "expected at least one ox command extracted from %q", cmd)
		for _, e := range extracted {
			assert.True(t, isValidOxCommand(e),
				"hook command fallback text produced a false-positive invalid command %q from %q", e, cmd)
		}
	}
}

// TestCheckHookCommands_AgentHookFallback drives the real checkHookCommands
// doctor check end-to-end against a settings.json using the updated
// Claude Code hook constant, proving the longer off-PATH else-branch still
// reads as a clean, valid hook to `ox doctor`.
func TestCheckHookCommands_AgentHookFallback(t *testing.T) {
	tempHome := t.TempDir()
	claudeDir := filepath.Join(tempHome, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0755))

	settings := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{
				map[string]interface{}{
					"matcher": "",
					"hooks": []interface{}{
						map[string]interface{}{
							"type":    "command",
							"command": constants.OxPrimeCommandClaudeCode,
						},
					},
				},
			},
		},
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0644))

	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	result := checkHookCommands()

	assert.True(t, result.passed, "expected passed result, got passed=%v warning=%v detail=%s", result.passed, result.warning, result.detail)
	assert.False(t, result.warning, "expected no warning for the off-PATH fallback text, got detail=%s", result.detail)
}

// TestOffPathFallback_DoesNotRecommendMutableInstaller verifies both generated
// hook fallbacks offer safe release routes without mutable installer guidance.
func TestOffPathFallback_DoesNotRecommendMutableInstaller(t *testing.T) {
	fallbacks := map[string]string{
		"agent hook": constants.OxPrimeCommandClaudeCode,
		"git hook":   oxGitHookNotOnPathFallback,
	}

	for name, fallback := range fallbacks {
		t.Run(name, func(t *testing.T) {
			assert.Contains(t, fallback, "brew install sageox/tap/ox")
			assert.Contains(t, fallback, "github.com/sageox/ox/releases/latest")
			assert.NotContains(t, fallback, "curl")
			assert.NotContains(t, fallback, "github.com/sageox/ox#install")
		})
	}
}

// TestOffPathFallback_FishRecoveryLineSurvivesSpaces runs both off-PATH
// fallbacks under a fish $SHELL with ox installed in a directory containing a
// space. The zsh/bash branches interpolate inside `export PATH="..."`, so a
// spaced directory survives being copied; the fish branch emitted the
// directory bare, so fish received three arguments and added two wrong
// directories — the one recovery instruction an off-PATH user is given
// silently failed for exactly the user who needed it. Drives `sh` rather than
// asserting on the constant so the shell's own quoting is what's tested.
func TestOffPathFallback_FishRecoveryLineSurvivesSpaces(t *testing.T) {
	binDir := filepath.Join(t.TempDir(), "Go Tools", "bin")
	require.NoError(t, os.MkdirAll(binDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "ox"), []byte("#!/bin/sh\n"), 0755))

	cases := []struct {
		name   string
		script string
	}{
		{"agent hook", constants.OxPrimeCommandClaudeCode},
		// The git-hook fallback is an else-branch; give it the `if` it expects.
		{"git hook", "if command -v ox >/dev/null 2>&1; then :\n" + oxGitHookNotOnPathFallback},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("sh", "-c", tc.script)
			cmd.Env = []string{
				// no ox on PATH: this is the branch under test
				"PATH=/usr/bin:/bin",
				"HOME=" + t.TempDir(),
				"GOBIN=" + binDir,
				"SHELL=/usr/bin/fish",
			}
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "fallback exited non-zero: %s", out)

			assert.Contains(t, string(out), `fish_add_path -- "`+binDir+`"`,
				"fish recovery line must pass the install directory to fish as one argument, got:\n%s", out)
		})
	}
}

// offPathFallbacks are both generated off-PATH fallbacks, each wrapped so it
// runs on its own under sh. The git-hook fallback is an else-branch; it gets
// the `if` it expects.
func offPathFallbacks() []struct{ name, script string } {
	return []struct{ name, script string }{
		{"agent hook", constants.OxPrimeCommandClaudeCode},
		{"git hook", "if command -v ox >/dev/null 2>&1; then :\n" + oxGitHookNotOnPathFallback},
	}
}

// runOffPathFallback runs script under sh with ox installed only in binDir,
// which is off PATH, and returns what it printed.
func runOffPathFallback(t *testing.T, script, binDir, shell, home string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "ox"), []byte("#!/bin/sh\n"), 0o755))
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "GOBIN=" + binDir, "SHELL=" + shell}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "fallback exited non-zero: %s", out)
	return string(out)
}

// TestOffPathFallback_PathLineEscapesDirectory pastes the printed PATH line
// into a startup file, sources it under a real sh, and checks the install
// directory lands on PATH byte for byte with nothing in its name run. The
// fallbacks used to put the directory inside double quotes unescaped, so a
// directory named with $, a backtick or " ran code every time the startup
// file was read (issue #1162).
func TestOffPathFallback_PathLineEscapesDirectory(t *testing.T) {
	names := []string{
		"Go Tools",
		"it's",
		"$(touch PWNED)",
		"`touch PWNED`",
		`a"; touch PWNED; "`,
		`back\slash\"; touch PWNED; #`,
		"$HOME",
	}
	for _, fb := range offPathFallbacks() {
		for _, shell := range []string{"/bin/zsh", "/bin/bash", "/bin/ksh"} {
			for _, name := range names {
				t.Run(fb.name+" "+filepath.Base(shell)+" "+name, func(t *testing.T) {
					binDir := filepath.Join(t.TempDir(), name, "bin")
					out := runOffPathFallback(t, fb.script, binDir, shell, t.TempDir())

					var line string
					for _, l := range strings.Split(out, "\n") {
						if strings.HasPrefix(l, "    export PATH=") {
							line = strings.TrimPrefix(l, "    ")
						}
					}
					require.NotEmpty(t, line, "no PATH line printed:\n%s", out)

					work := t.TempDir()
					require.NoError(t, os.WriteFile(filepath.Join(work, "rc"), []byte(line+"\n"), 0o644))
					src := exec.Command("sh", "-c", `PATH=/base; . ./rc; printf '%s' "$PATH"`)
					src.Dir = work
					src.Env = []string{"HOME=" + work}
					got, err := src.CombinedOutput()
					require.NoError(t, err, "sourcing %q failed: %s", line, got)

					assert.Equal(t, "/base:"+binDir, string(got), "line %q", line)
					assert.NoFileExists(t, filepath.Join(work, "PWNED"), "line %q ran code", line)
				})
			}
		}
	}
}

// TestOffPathFallback_FishLineEscapesDirectory checks the fish line escapes
// \, " and $ and leaves a backtick alone, which fish does not expand.
func TestOffPathFallback_FishLineEscapesDirectory(t *testing.T) {
	for _, fb := range offPathFallbacks() {
		t.Run(fb.name, func(t *testing.T) {
			root := t.TempDir()
			binDir := filepath.Join(root, `a"b$c`+"`d`"+`\e`, "bin")
			out := runOffPathFallback(t, fb.script, binDir, "/usr/bin/fish", t.TempDir())
			want := `fish_add_path -- "` + root + `/a\"b\$c` + "`d`" + `\\e/bin"`
			assert.Contains(t, out, want)
		})
	}
}

// TestOffPathFallback_BashNamesTheFileANewTerminalReads covers issue #1162:
// a macOS terminal opens a login bash, which reads the first existing of
// ~/.bash_profile, ~/.bash_login and ~/.profile and never ~/.bashrc on its
// own, so the macOS advice names that file. Linux terminals open non-login
// shells, which read ~/.bashrc.
func TestOffPathFallback_BashNamesTheFileANewTerminalReads(t *testing.T) {
	cases := []struct {
		name     string
		existing []string
		darwin   string
	}{
		{"no startup files", nil, "~/.bash_profile"},
		{"only profile", []string{".profile"}, "~/.profile"},
		{"bash_login and profile", []string{".bash_login", ".profile"}, "~/.bash_login"},
		{"all three", []string{".profile", ".bash_login", ".bash_profile"}, "~/.bash_profile"},
	}
	for _, fb := range offPathFallbacks() {
		for _, tc := range cases {
			t.Run(fb.name+" "+tc.name, func(t *testing.T) {
				home := t.TempDir()
				for _, f := range tc.existing {
					require.NoError(t, os.WriteFile(filepath.Join(home, f), nil, 0o644))
				}
				out := runOffPathFallback(t, fb.script, filepath.Join(t.TempDir(), "bin"), "/bin/bash", home)
				want := "~/.bashrc"
				if runtime.GOOS == "darwin" {
					want = tc.darwin
				}
				assert.Contains(t, out, "Add this line to "+want+":\n")
			})
		}
	}
}
