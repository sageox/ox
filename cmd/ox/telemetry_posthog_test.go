package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/conversation/read"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/errkind"
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

// Failure prevented: a daemon that is down, doctor reporting failed checks,
// or a mistyped subcommand is filed as a network failure or as "other",
// hiding which failures are ox's to fix.
func TestPostHogErrorKind(t *testing.T) {
	var netErr net.Error = timeoutErr{}
	// What the daemon client returns when nothing listens on its socket.
	daemonDown := daemon.NewClientWithSocket(filepath.Join(t.TempDir(), "d.sock")).Ping()
	require.Error(t, daemonDown)
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
		{"daemon down", fmt.Errorf("daemon sync: %w", daemonDown), 1, "daemon"},
		{"deadline", fmt.Errorf("fetch: %w", context.DeadlineExceeded), 1, "timeout"},
		{"network", fmt.Errorf("dial: %w", netErr), 1, "network"},
		{"connection refused", fmt.Errorf("fetch: %w", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}), 1, "network"},
		{"usage", errors.New("usage error"), 2, "usage"},
		{"kind ox attached", fmt.Errorf("doctor: %w", errkind.Errorf(errkind.ChecksFailed, "some checks failed")), 1, "checks_failed"},
		{"anything else", errors.New("/Users/someone/secret-project: boom"), 1, "other"},
		{"--json failure keeps its cause", cli.Silent(fmt.Errorf("daemon sync: %w", daemonDown)), 1, "daemon"},
		{"--json failure with no kind", cli.Silent(errors.New("boom")), 1, "other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, postHogErrorKind(tt.err, tt.exitCode))
		})
	}
}

// Failure prevented: an AI coworker guessing at `ox agent` subcommands is
// reported as the command failing, and its retries mark the install wedged.
func TestPostHogErrorKind_AgentDispatcherMistakesAreUsage(t *testing.T) {
	projectRoot := pauseResumeProject(t)
	const agentID = "OxUse1"
	registerTestInstance(t, projectRoot, agentID)

	for _, tt := range []struct {
		args   []string
		detail string
	}{
		{[]string{"Ox12345", "session", "stop"}, "invalid agent ID"},
		{[]string{"no-such-command"}, "unknown command or invalid agent_id: %s"},
		{[]string{agentID}, "missing command after agent_id"},
		{[]string{agentID, "no-such-command"}, "unknown command: %s"},
		{[]string{agentID, "session"}, "session requires a subcommand"},
		{[]string{agentID, "session", "status"}, "unknown session command: %s"},
		{[]string{agentID, "session", "html"}, "session html command has been removed; use the web viewer at sageox.ai"},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			err := runAgentDispatcher(&cobra.Command{}, tt.args)
			require.Error(t, err)
			assert.Equal(t, "usage", postHogErrorKind(err, 1), "error: %v", err)
			assert.Equal(t, tt.detail, postHogErrorDetail(err))
		})
	}
}

// Failure prevented: a coworker who has not run `ox login` or `ox init`, or
// whose credentials the server rejected, is counted as "other" instead of at
// the step where setup stopped.
func TestPostHogErrorKind_SetupFailures(t *testing.T) {
	withIsolatedConfig(t) // no stored credentials for any endpoint
	t.Setenv("SAGEOX_TOKEN", "")
	const ep = "https://test.sageox.local"

	initialized := t.TempDir()
	initGitRepo(t, initialized)
	require.NoError(t, os.MkdirAll(filepath.Join(initialized, ".sageox"), 0o755))
	require.NoError(t, config.SaveProjectConfig(initialized, &config.ProjectConfig{
		RepoID: "repo_test123", TeamID: "team_test456", Endpoint: ep,
	}))
	uninitialized := t.TempDir()
	initGitRepo(t, uninitialized)
	// An expired token whose refresh cannot reach the server.
	const unreachable = "http://127.0.0.1:1"
	refreshing := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(refreshing, ".sageox"), 0o755))
	require.NoError(t, config.SaveProjectConfig(refreshing, &config.ProjectConfig{
		RepoID: "repo_test123", TeamID: "team_test456", Endpoint: unreachable,
	}))
	require.NoError(t, auth.SaveTokenForEndpoint(unreachable, &auth.StoredToken{
		AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(-time.Hour),
	}))
	// ox import finds the team's context checkout before it asks for
	// credentials.
	require.NoError(t, os.MkdirAll(config.DefaultTeamContextPath("team_test456", ep), 0o755))
	prevImportFlags := importFlags
	importFlags = importFlagsT{}
	t.Cleanup(func() { importFlags = prevImportFlags })

	teamMembers := &cobra.Command{}
	teamMembers.Flags().String("team", "team_test456", "")
	teamMembers.Flags().Bool("json", false, "")

	tests := []struct {
		name string
		dir  string // working directory and project root
		run  func() error
		want errkind.Kind
	}{
		{"kb describe, credentials rejected", initialized, func() error {
			return handleKBDescribeError(io.Discard, api.ErrUnauthorized, "bubble", false)
		}, errkind.Auth},
		{"kb search, credentials rejected", initialized, func() error {
			return handleKBSearchError(io.Discard, api.ErrUnauthorized, false)
		}, errkind.Auth},
		{"team context URL", initialized, func() error {
			_, err := fetchTeamContextURLWithError("team_test456", ep)
			return err
		}, errkind.NotLoggedIn},
		{"ledger URL", initialized, func() error {
			_, err := fetchLedgerURLWithError(ep)
			return err
		}, errkind.NotLoggedIn},
		{"agent query", initialized, func() error {
			_, err := queryTeamContext(&queryArgs{query: "q"}, initialized, "OxUse1", "claude")
			return err
		}, errkind.NotLoggedIn},
		{"team members", initialized, func() error { return runTeamMembers(teamMembers, nil) }, errkind.NotLoggedIn},
		{"agent query, refresh unreachable", refreshing, func() error {
			_, err := queryTeamContext(&queryArgs{query: "q"}, refreshing, "OxUse1", "claude")
			return err
		}, errkind.Network},
		{"team members, refresh unreachable", refreshing, func() error { return runTeamMembers(teamMembers, nil) }, errkind.Network},
		{"import", initialized, func() error {
			_, _, _, _, err := resolveImportContext(context.Background())
			return err
		}, errkind.NotLoggedIn},
		{"agent session start", uninitialized, func() error { return runAgentSessionStart(nil, nil) }, errkind.NotInitialized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(tt.dir)
			t.Setenv("OX_PROJECT_ROOT", tt.dir)
			err := tt.run()
			require.Error(t, err)
			assert.Equal(t, string(tt.want), postHogErrorKind(err, 1), "error: %v", err)
		})
	}
}

// Failure prevented: every failure of one kind looks alike in PostHog, or a
// value filled into an error message (a typed argument, a path) reaches it.
func TestPostHogErrorDetail(t *testing.T) {
	_, missingFile := os.Open(filepath.Join(t.TempDir(), "sk-live-TOKEN"))
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"kind declared on a sentinel", fmt.Errorf("list teams: %w", api.ErrUnauthorized), "authentication required: run 'ox login' first"},
		{"file error, not its path", fmt.Errorf("read config: %w", missingFile), "syscall.Errno: no such file or directory"},
		{"typed cause", fmt.Errorf("fetch: %w", context.DeadlineExceeded), "context.deadlineExceededError"},
		{"OS error in a join", errors.Join(errors.New("sk-live-TOKEN rejected"), missingFile), "syscall.Errno: no such file or directory"},
		{"wrapped plain message", fmt.Errorf("sync: %w", errors.New("sk-live-TOKEN rejected")), ""},
		{"join of plain messages", errors.Join(errors.New("sk-live-TOKEN"), errors.New("b")), ""},
		{"--json failure with no kind names the wrapper, not the cause", cli.Silent(errors.New("sk-live-TOKEN rejected")), "cli.silentCause"},
		{"nil", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := postHogErrorDetail(tt.err)
			assert.Equal(t, tt.want, got)
			assert.NotContains(t, got, "sk-live-TOKEN")
		})
	}

	long := postHogErrorDetail(errkind.WithDetail(errkind.Other, strings.Repeat("—", 100), errors.New("x")))
	assert.LessOrEqual(t, len(long), postHogMaxDetail)
	assert.True(t, utf8.ValidString(long), "cut on a character boundary")
}

// Failure prevented: doctor's detail names a check (whose name can hold a
// repository name or path), repeats a category, or misses a failed child.
func TestFailedCheckCategories(t *testing.T) {
	categories := []checkCategory{
		{name: "Authentication", checks: []checkResult{{passed: true}}},
		{name: "Daemon", checks: []checkResult{{name: "acme-secret repo", passed: false}}},
		{name: "Updates", checks: []checkResult{{skipped: true}}},
		{name: "Sessions", checks: []checkResult{{passed: true, children: []checkResult{{passed: false}}}}},
		{name: "Daemon", checks: []checkResult{{passed: false}}},
	}
	assert.Equal(t, []string{"Daemon", "Sessions"}, failedCheckCategories(categories))
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
	assert.NotContains(t, props, "error_detail", "a plain message names no failure")
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

// Failure prevented: a command records how it ended (whether a review was
// approved or abandoned, which its exit code cannot say) and the event leaves
// it out — or an outcome named like a property the event already carries
// replaces it, and success rates quietly go wrong.
func TestPostHogCommandProps_CarriesHowTheCommandEnded(t *testing.T) {
	withIsolatedConfig(t)
	c := &cli.Context{Cmd: &cobra.Command{Use: "review"}, CommandStartTime: time.Now()}
	c.SetOutcome("review_outcome", "approved")
	c.SetOutcome("highlights", 1)
	c.SetOutcome("success", false)

	props := postHogCommandProps(c, "plan review", 0)

	assert.Equal(t, "approved", props["review_outcome"])
	assert.Equal(t, 1, props["highlights"])
	assert.Equal(t, "plan review", props["command"])
	assert.Equal(t, true, props["success"], "an outcome never replaces a property the event carries")
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

// TestCobraRejection_NamesTheMistakeNotWhatWasTyped covers every rejection
// cobra and pflag make before ox runs a command.
//
// Failure prevented: a flag value, path, or misspelled secret a person typed
// reaching PostHog through error_detail; or a rejection filed under the
// catch-all, hiding which mistake people make.
func TestCobraRejection_NamesTheMistakeNotWhatWasTyped(t *testing.T) {
	tests := []struct {
		msg  string
		want string
	}{
		{`unknown command "sk-secret" for "ox"`, "unknown command"},
		{"unknown flag: --token=sk-secret", "unknown flag"},
		{"unknown shorthand flag: 'x' in -xsk-secret", "unknown flag"},
		{"flag needs an argument: --config", "flag needs an argument"},
		{"bad flag syntax: ---sk-secret", "unknown flag"},
		{`invalid argument "sk-secret" for "--limit" flag: strconv.ParseInt: parsing "sk-secret": invalid syntax`, "invalid argument"},
		{`required flag(s) "file" not set`, "required flag missing"},
		{"accepts 1 arg(s), received 2", "wrong number of arguments"},
		{"requires at least 1 arg(s), only received 0", "wrong number of arguments"},
		{"if any flags in the group [a b] are set none of the others can be; [a b] were all set", "conflicting flags"},
		{"at least one of the flags in the group [a b] is required", "conflicting flags"},
		{"something new from cobra: sk-secret", "rejected invocation"},
	}
	for _, tt := range tests {
		got := cobraRejection(errors.New(tt.msg))
		assert.Equal(t, tt.want, got, tt.msg)
		assert.NotContains(t, got, "sk-secret")
		// Only the checks cobra makes after PersistentPreRunE are reclassified
		// once ox has its context; a command's own error never is.
		postPreRun := tt.want == "required flag missing" || tt.want == "conflicting flags"
		assert.Equal(t, postPreRun, cobraFlagValidation(errors.New(tt.msg)), tt.msg)
	}
}

// TestConversationErrorKind_EveryCodeFiled covers every code the
// conversation read commands can fail with.
//
// Failure prevented: a conversation failure reaching PostHog as
// error_kind=other with the Go type name as its detail, or a code that exits
// 2 (the invocation was wrong) counted as an ox failure.
func TestConversationErrorKind_EveryCodeFiled(t *testing.T) {
	want := map[string]errkind.Kind{
		read.ErrCodeInvalidID:              errkind.Usage,
		read.ErrCodeInvalidSelector:        errkind.Usage,
		read.ErrCodeShareLinkUnresolvable:  errkind.Usage,
		read.ErrCodeShareLinkNotDiscussion: errkind.Usage,
		read.ErrCodeNotAuthenticated:       errkind.NotLoggedIn,
		read.ErrCodeNoTeamAccess:           errkind.Auth,
		read.ErrCodeAccessUnverified:       errkind.Network,
		read.ErrCodeNoTeamContext:          errkind.NotInitialized,
		read.ErrCodeNotIndexed:             errkind.Other,
		read.ErrCodeNoDistillation:         errkind.Other,
		read.ErrCodeTranscriptNotAvailable: errkind.Other,
		read.ErrCodeTopicNotFound:          errkind.Other,
		read.ErrCodeReadError:              errkind.Other,
	}
	for code, kind := range want {
		err := conversationExitError(&read.Error{Code: code, Message: "conversation cnv_secret not found"})
		assert.Equal(t, kind, errkind.Of(err), code)
		assert.Equal(t, code, errkind.DetailOf(err), "the detail is the code, never the message")
		var exit *commandExitError
		require.True(t, errors.As(err, &exit), "main still honors the exit code for %s", code)
		assert.Equal(t, exit.ExitCode == 2, kind == errkind.Usage, "%s: exit 2 if and only if usage", code)
	}

	// Flag failures the command layer catches itself go out as usage too.
	err := conversationUsageExit(io.Discard, "json", conversationUsageErrorCode, "--since: bad value")
	assert.Equal(t, errkind.Usage, errkind.Of(err))
	assert.Equal(t, conversationUsageErrorCode, errkind.DetailOf(err))
}
