package agentwork

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: an imported transcript is untrusted text. Summarizing it
// with the daemon's permission bypass and full toolset would let instructions
// inside an old session run tools on the coworker's machine.

func fakeCLI(t *testing.T, name, body string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\n"+body), 0o755))
	return script
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

func TestClaudeRunnerIsolated(t *testing.T) {
	isolatedArgs := []string{"--output-format", "stream-json", "--verbose", "--safe-mode", "--tools", "", "--no-session-persistence", "--model", "claude-haiku-4-5", "-p"}
	tests := []struct {
		name        string
		help        string
		ifSupported bool // the daemon's request; import uses Isolated
		wantErr     string
		want        []string
		wantEnv     string
	}{
		{
			name: "no tools, no permission bypass, nothing persisted",
			help: "--safe-mode --tools --no-session-persistence",
			want: isolatedArgs, wantEnv: "disabled",
		},
		{
			name:    "a Claude that cannot isolate is refused, never widened",
			help:    "--tools --no-session-persistence",
			wantErr: "--safe-mode",
		},
		{
			name: "the daemon isolates a Claude that can", ifSupported: true,
			help: "--safe-mode --tools --no-session-persistence",
			want: isolatedArgs, wantEnv: "disabled",
		},
		{
			// Failure prevented: Claude Code older than 2.1.169 (no --safe-mode)
			// stops the daemon from summarizing sessions at all.
			name: "the daemon keeps summarizing with a Claude too old to isolate", ifSupported: true,
			help: "--tools --no-session-persistence",
			want: []string{"--output-format", "stream-json", "--verbose", "--permission-mode", "bypassPermissions", "--model", "claude-haiku-4-5", "-p"},
		},
	}
	t.Setenv("OX_SESSION_RECORDING", "")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			argsFile, envFile := filepath.Join(dir, "args"), filepath.Join(dir, "env")
			script := fakeCLI(t, "claude", `if [ "$1" = "--help" ]; then echo "`+tt.help+`"; exit; fi
for a in "$@"; do printf '%s\n' "$a" >> "`+argsFile+`"; done
printf '%s\n' "$OX_SESSION_RECORDING" > "`+envFile+`"
printf '%s\n' '{"type":"result","result":"ok"}'
`)
			r := &ClaudeRunner{binaryPath: script, logger: slog.Default()}
			_, err := r.Run(context.Background(), RunRequest{Prompt: "p", Model: "claude-haiku-4-5", Isolated: !tt.ifSupported, IsolateIfSupported: tt.ifSupported})
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.NoFileExists(t, argsFile, "no prompt-bearing run after a failed probe")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, readLines(t, argsFile))
			assert.Equal(t, []string{tt.wantEnv}, readLines(t, envFile))
		})
	}
}

func TestClaudeRunnerDefaultKeepsDaemonContract(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	script := fakeCLI(t, "claude", `for a in "$@"; do printf '%s\n' "$a" >> "`+argsFile+`"; done
printf '%s\n' '{"type":"result","result":"ok"}'
`)
	r := &ClaudeRunner{binaryPath: script, logger: slog.Default()}
	_, err := r.Run(context.Background(), RunRequest{Prompt: "p"})
	require.NoError(t, err)
	assert.Equal(t, []string{"--output-format", "stream-json", "--verbose", "--permission-mode", "bypassPermissions", "-p"}, readLines(t, argsFile))
}

func TestCodexRunnerIsolated(t *testing.T) {
	const fullHelp = "--sandbox --ephemeral --color --config --ignore-user-config --ignore-rules --skip-git-repo-check --disable"
	const features = "shell_tool stable true\nunified_exec stable true\nplugins stable true\nfuture_thing stable true\n"
	const isolatedOut = "exec\n--sandbox\nread-only\n--ephemeral\n--color\nnever\n-c\nfeatures.hooks=false\n" +
		"--ignore-user-config\n--ignore-rules\n--skip-git-repo-check\n" +
		"--disable\nplugins\n--disable\nshell_tool\n--disable\nunified_exec\n-"
	tests := []struct {
		name        string
		help        string
		features    string
		ifSupported bool // the daemon's request; import uses Isolated
		wantErr     string
		want        string
	}{
		{
			name: "ignores user config and rules, disables every tool feature this Codex lists",
			help: fullHelp, features: features,
			want: isolatedOut,
		},
		{
			name: "the daemon isolates a Codex that can", ifSupported: true,
			help: fullHelp, features: features,
			want: isolatedOut,
		},
		{
			name: "the daemon keeps summarizing with a Codex too old to isolate", ifSupported: true,
			help: "--sandbox --ephemeral --color --config --disable", features: features,
			want: "exec\n--sandbox\nread-only\n--ephemeral\n--color\nnever\n-c\nfeatures.hooks=false\n-",
		},
		{
			name: "a Codex that cannot isolate is refused, never widened",
			help: "--sandbox --ephemeral --color --config --disable", features: features,
			wantErr: "--ignore-user-config",
		},
		{
			name: "a Codex whose shell cannot be disabled by name is refused",
			help: fullHelp, features: "shell_tool stable true\nplugins stable true\n",
			wantErr: "unified_exec",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			probes, prompted := filepath.Join(dir, "probes"), filepath.Join(dir, "prompted")
			script := fakeCLI(t, "codex", `if [ "$2" = "--help" ]; then echo x >> "`+probes+`"; echo "`+tt.help+`"; exit; fi
if [ "$1" = "features" ]; then printf '`+tt.features+`'; exit; fi
touch "`+prompted+`"
printf '%s\n' "$@"
`)
			r := &CodexRunner{binaryPath: script, logger: slog.Default()}
			result, err := r.Run(context.Background(), RunRequest{Prompt: "p", Isolated: !tt.ifSupported, IsolateIfSupported: tt.ifSupported})
			assert.Len(t, readLines(t, probes), 1, "exec --help is probed once per run")
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.NoFileExists(t, prompted, "no prompt-bearing run after a failed probe")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, result.Output)
		})
	}
}

func TestClaudeUsabilityHonorsClaudeConfigDir(t *testing.T) {
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"), 0o755))
	config := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(config, ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"x"}}`), 0o600))
	t.Setenv("PATH", bin)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", config)

	got := CheckAgentUsability("claude")
	assert.True(t, got.Installed)
	assert.True(t, got.Authenticated, "the login lives in CLAUDE_CONFIG_DIR, not the home directory")
}

// Failure prevented: a CLI whose capabilities cannot be confirmed treated as
// isolated, or a CLI that never answers stalling the import.
func TestIsolationProbesFailClosed(t *testing.T) {
	const fullHelp = "--sandbox --ephemeral --color --config --ignore-user-config --ignore-rules --skip-git-repo-check --disable"
	big := `head -c 300000 /dev/zero | tr '\\0' x`
	tests := []struct {
		name, cli, body, wantErr string
	}{
		{"a Claude that fails its help probe", "claude", `if [ "$1" = "--help" ]; then exit 3; fi`, "cannot verify Claude Code isolation"},
		{"a Codex whose help overflows", "codex", `if [ "$2" = "--help" ]; then ` + big + `; exit; fi`, "help output exceeds limit"},
		{"a Codex that cannot list its features", "codex", `if [ "$2" = "--help" ]; then echo "` + fullHelp + `"; exit; fi
if [ "$1" = "features" ]; then exit 2; fi`, "cannot list Codex features"},
		{"a Codex whose feature list overflows", "codex", `if [ "$2" = "--help" ]; then echo "` + fullHelp + `"; exit; fi
if [ "$1" = "features" ]; then ` + big + `; exit; fi`, "output exceeds limit"},
		{"a Codex that never lists its features", "codex", `if [ "$2" = "--help" ]; then echo "` + fullHelp + `"; exit; fi
if [ "$1" = "features" ]; then exec sleep 5; fi`, "did not answer within"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prior := codexProbeTimeout
			codexProbeTimeout = 300 * time.Millisecond
			t.Cleanup(func() { codexProbeTimeout = prior })
			prompted := filepath.Join(t.TempDir(), "prompted")
			script := fakeCLI(t, tt.cli, tt.body+"\ntouch \""+prompted+"\"\n")
			// A probe that failed proves nothing about the CLI, so the daemon's
			// best-effort request must not fall back to running unisolated either.
			for _, req := range []RunRequest{{Prompt: "p", Isolated: true}, {Prompt: "p", IsolateIfSupported: true}} {
				var err error
				if tt.cli == "claude" {
					_, err = (&ClaudeRunner{binaryPath: script, logger: slog.Default()}).Run(context.Background(), req)
				} else {
					_, err = (&CodexRunner{binaryPath: script, logger: slog.Default()}).Run(context.Background(), req)
				}
				require.ErrorContains(t, err, tt.wantErr)
				assert.NoFileExists(t, prompted, "no prompt-bearing run after a failed probe")
			}
		})
	}
}

// Failure prevented: a coworker logged in to Claude Code reported as not
// logged in, so their sessions are never summarized.
func TestClaudeUsabilityReadsTheHomeLogin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows: the home directory does not come from HOME")
	}
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"), 0o755))
	t.Setenv("PATH", bin)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"x"}}`), 0o600))
	t.Setenv("HOME", home)
	assert.True(t, CheckAgentUsability("claude").Authenticated)

	t.Setenv("HOME", "")
	got := CheckAgentUsability("claude")
	assert.False(t, got.Authenticated)
	assert.Equal(t, "unable to check", got.AuthDetail)
}
