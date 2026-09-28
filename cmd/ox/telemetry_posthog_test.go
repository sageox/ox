package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/updatenotice"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: hooks that fire on every tool call and turn, or git
// plumbing that runs on every fetch and commit, drown out the commands people
// choose to run — or a real command is dropped with them.
func TestSkipPostHogCommand(t *testing.T) {
	tests := []struct {
		path string
		skip bool
	}{
		{"agent hook PostToolUse", true},
		{"agent hook UserPromptSubmit", true},
		{"agent hook Stop", true},
		{"agent hook PreCompact", true},
		{"agent hook", true},
		{"git-credential-helper", true},
		{"hooks commit-msg", true},
		{"hooks post-commit", true},
		{"hooks post-rewrite", true},
		{"hooks pre-push", true},
		{"agent hook SessionStart", false},
		{"agent hook SessionEnd", false},
		{"agent hooks", false},
		{"hooks list", false},
		{"agent prime", false},
		{"agent session stop", false},
		{"status", false},
		{"plan viz lint", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			assert.Equal(t, tt.skip, skipPostHogCommand(tt.path))
		})
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return false }

func TestPostHogErrorKind(t *testing.T) {
	var netErr net.Error = timeoutErr{}
	tests := []struct {
		name     string
		err      error
		exitCode int
		want     string
	}{
		{"success", nil, 0, ""},
		{"interrupted", errors.New("interrupted"), 130, "interrupted"},
		{"canceled", fmt.Errorf("sync: %w", context.Canceled), 1, "interrupted"},
		{"unauthorized", fmt.Errorf("list teams: %w", api.ErrUnauthorized), 1, "auth"},
		{"malformed env token", fmt.Errorf("load: %w", auth.ErrEnvTokenMalformed), 1, "auth"},
		{"version unsupported", fmt.Errorf("prime: %w", api.ErrVersionUnsupported), 1, "version_unsupported"},
		{"deadline", fmt.Errorf("fetch: %w", context.DeadlineExceeded), 1, "timeout"},
		{"network", fmt.Errorf("dial: %w", netErr), 1, "network"},
		{"usage", errors.New("usage error"), 2, "usage"},
		{"anything else", errors.New("/Users/someone/secret-project: boom"), 1, "other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, postHogErrorKind(tt.err, tt.exitCode))
		})
	}
}

// Failure prevented: arguments, flag values, paths, or error text (which can
// hold secrets, customer names, or file paths) reach PostHog.
func TestPostHogCommandProps_SendNothingTheUserTyped(t *testing.T) {
	withIsolatedConfig(t)
	root := &cobra.Command{Use: "ox"}
	search := &cobra.Command{Use: "search", Run: func(*cobra.Command, []string) {}}
	search.Flags().String("repo", "", "")
	search.Flags().Bool("json", false, "")
	search.Flags().Int("limit", 10, "")
	code := &cobra.Command{Use: "code"}
	root.AddCommand(code)
	code.AddCommand(search)
	require.NoError(t, search.ParseFlags([]string{"--repo", "acme-secret-repo", "--json", "sk-live-TOKEN", "/Users/someone/private"}))

	c := &cli.Context{
		Cmd:              search,
		CommandStartTime: time.Now().Add(-1500 * time.Millisecond),
		Err:              errors.New("open /Users/someone/private: sk-live-TOKEN rejected"),
	}
	props := postHogCommandProps(c, c.CommandPath(search), 1)

	encoded, err := json.Marshal(props)
	require.NoError(t, err)
	for _, typed := range []string{"acme-secret-repo", "sk-live-TOKEN", "/Users/someone", "rejected"} {
		assert.NotContains(t, string(encoded), typed)
	}
	assert.Equal(t, "code search", props["command"])
	assert.Equal(t, []string{"json", "repo"}, props["flags"], "flag names only, never values")
	assert.Equal(t, false, props["success"])
	assert.Equal(t, 1, props["exit_code"])
	assert.Equal(t, "other", props["error_kind"])
	assert.GreaterOrEqual(t, props["duration_ms"], int64(1500))
	assert.Equal(t, false, props["repo_initialized"])
	assert.NotContains(t, props, "repo_id", "outside a repository there is none")
	assert.NotContains(t, props, "team_id")
	assert.Contains(t, props, "actor")
	assert.Contains(t, props, "agent_type")
	assert.Contains(t, props, "ephemeral")
}

// Failure prevented: usage can't be counted per team, or the team's name
// reaches PostHog along with its ID.
func TestPostHogCommandProps_TagsTheRepositoryAndTeam(t *testing.T) {
	withIsolatedConfig(t)
	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".sageox"), 0o755))
	require.NoError(t, config.SaveProjectConfig(repo, &config.ProjectConfig{
		RepoID:   "repo_test123",
		TeamID:   "team_test456",
		TeamName: "Acme Skunkworks",
	}))
	cmd := &cobra.Command{Use: "status"}

	props := postHogCommandProps(&cli.Context{Cmd: cmd, CommandStartTime: time.Now(), ProjectRoot: repo}, "status", 0)

	assert.Equal(t, true, props["repo_initialized"])
	assert.Equal(t, "repo_test123", props["repo_id"])
	assert.Equal(t, "team_test456", props["team_id"])
	encoded, err := json.Marshal(props)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "Acme Skunkworks")
}

// Failure prevented: a bare .sageox/ directory, such as a legacy ~/.sageox
// above every repository in $HOME, reports each command as run in an
// initialized repository.
func TestPostHogCommandProps_SageoxDirectoryAloneIsNotInitialized(t *testing.T) {
	withIsolatedConfig(t)
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".sageox"), 0o755))
	cmd := &cobra.Command{Use: "status"}

	props := postHogCommandProps(&cli.Context{Cmd: cmd, CommandStartTime: time.Now(), ProjectRoot: home}, "status", 0)

	assert.Equal(t, false, props["repo_initialized"])
	assert.NotContains(t, props, "repo_id")
}

// Failure prevented: a successful command is reported with an error kind.
func TestPostHogCommandProps_SuccessHasNoErrorKind(t *testing.T) {
	withIsolatedConfig(t)
	cmd := &cobra.Command{Use: "status"}
	c := &cli.Context{Cmd: cmd, CommandStartTime: time.Now()}

	props := postHogCommandProps(c, "status", 0)

	assert.Equal(t, true, props["success"])
	assert.NotContains(t, props, "error_kind")
	assert.Equal(t, []string{}, props["flags"])
}

func TestShowTelemetryNoticeOnce_ShownOnceToAPersonAtATerminal(t *testing.T) {
	withIsolatedConfig(t)
	atTerminal(t)

	var first, second bytes.Buffer
	showTelemetryNoticeOnce(&first)
	showTelemetryNoticeOnce(&second)

	assert.Contains(t, first.String(), "ox sends usage data")
	assert.Contains(t, first.String(), "ox config set telemetry off")
	assert.Empty(t, second.String(), "the notice appears once per install")
}

// Failure prevented: the notice lands in an AI coworker's transcript, and the
// person at a terminal later never sees it because the flag was already used.
func TestShowTelemetryNoticeOnce_WaitsForAPerson(t *testing.T) {
	withIsolatedConfig(t)
	atTerminal(t)

	var piped, atTTY bytes.Buffer
	updatenotice.StderrIsTTY = func() bool { return false }
	showTelemetryNoticeOnce(&piped)
	updatenotice.StderrIsTTY = func() bool { return true }
	showTelemetryNoticeOnce(&atTTY)

	assert.Empty(t, piped.String())
	assert.Contains(t, atTTY.String(), "ox sends usage data")
}

// Failure prevented: a config that cannot be saved shows the notice on every
// run, since the flag that stops it is never stored.
func TestShowTelemetryNoticeOnce_NotShownWhenItCannotBeRemembered(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a directory the user can read but not write; os.Chmod does not do that on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root writes through directory permissions")
	}
	withIsolatedConfig(t)
	atTerminal(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("tips_enabled: true\n"), 0o600))
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	t.Setenv(config.EnvUserConfig, cfgPath)
	_, err := config.LoadUserConfig()
	require.NoError(t, err, "loading must work, so the failure under test is the save")

	var stderr bytes.Buffer
	showTelemetryNoticeOnce(&stderr)

	assert.Empty(t, stderr.String())
}

// Failure prevented: an opted-out user, or a hook, still triggers telemetry
// work at exit; or an ordinary command never does.
func TestCapturePostHogCommand_RespectsOptOutAndSkips(t *testing.T) {
	root := &cobra.Command{Use: "ox"}
	status := &cobra.Command{Use: "status"}
	agent := &cobra.Command{Use: "agent"}
	root.AddCommand(status, agent)

	tests := []struct {
		name            string
		doNotTrack      string
		sageoxTelemetry string
		turnedOff       bool // `ox config set telemetry off` ran as this command
		startedByOx     bool
		cmd             *cobra.Command
		path            string
		wantNotice      bool
	}{
		{name: "ordinary command", cmd: status, wantNotice: true},
		{name: "opted out with DO_NOT_TRACK=1", doNotTrack: "1", cmd: status},
		{name: "opted out with SAGEOX_TELEMETRY=false", sageoxTelemetry: "false", cmd: status},
		{name: "turned off by the command itself", turnedOff: true, cmd: status},
		{name: "started by another ox", startedByOx: true, cmd: status},
		{name: "hook on every tool call", cmd: agent, path: "agent hook PostToolUse"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withIsolatedConfig(t)
			atTerminal(t)
			t.Setenv("DO_NOT_TRACK", tt.doNotTrack)
			t.Setenv("SAGEOX_TELEMETRY", tt.sageoxTelemetry)
			if tt.turnedOff {
				userCfg, err := config.LoadUserConfig()
				require.NoError(t, err)
				userCfg.SetTelemetryEnabled(false)
				require.NoError(t, config.SaveUserConfig(userCfg))
			}
			savedCtx, savedStarted := cliCtx, startedByOx
			t.Cleanup(func() { cliCtx, startedByOx = savedCtx, savedStarted })
			startedByOx = tt.startedByOx
			cliCtx = &cli.Context{
				Cmd:              tt.cmd,
				CommandStartTime: time.Now(),
			}
			if tt.path != "" {
				cliCtx.SetCommandPath(tt.path)
			}

			var stderr bytes.Buffer
			capturePostHogCommand(0, &stderr)

			if tt.wantNotice {
				assert.Contains(t, stderr.String(), "ox sends usage data")
			} else {
				assert.Empty(t, stderr.String())
			}
		})
	}
}

// Failure prevented: every `ox agent ...` invocation is reported as "agent",
// so no one can tell a hook firing from a session stop.
func TestRenameDispatcherSpan_NamesTheAgentCommand(t *testing.T) {
	tests := []struct {
		parts []string
		want  string
	}{
		{[]string{"hook", "PostToolUse"}, "agent hook PostToolUse"},
		{[]string{"session", "stop"}, "agent session stop"},
		{[]string{"query"}, "agent query"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			saved := cliCtx
			t.Cleanup(func() { cliCtx = saved })
			cliCtx = &cli.Context{}

			renameDispatcherSpan(tt.parts...)

			assert.Equal(t, tt.want, cliCtx.CommandPath(agentCmd))
		})
	}
}

// Failure prevented: a bug in telemetry turns a command that succeeded into a
// crash with exit code 2 and a stack trace.
func TestCapturePostHogCommand_APanicDoesNotEscape(t *testing.T) {
	withIsolatedConfig(t)
	atTerminal(t)
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("SAGEOX_TELEMETRY", "")
	savedCtx, savedStarted := cliCtx, startedByOx
	t.Cleanup(func() { cliCtx, startedByOx = savedCtx, savedStarted })
	startedByOx = false
	root := &cobra.Command{Use: "ox"}
	status := &cobra.Command{Use: "status"}
	root.AddCommand(status)
	cliCtx = &cli.Context{Cmd: status, CommandStartTime: time.Now()}

	// The notice is due and its writer is nil, so printing it panics.
	assert.NotPanics(t, func() { capturePostHogCommand(0, nil) })
}
