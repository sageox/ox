package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/ephemeral"
	"github.com/sageox/ox/internal/telemetry"
	"github.com/sageox/ox/internal/updatenotice"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const postHogCommandEvent = "ox command run"

// envStartedByOx is set by main in every ox process, so any ox it starts
// (a hook running prime or a local query, sync starting the daemon) knows it
// is automation rather than a command a person or an AI coworker chose to
// run.
const envStartedByOx = "OX_STARTED_BY_OX"

// startedByOx records whether this process inherited envStartedByOx.
var startedByOx bool

// capturePostHogCommand sends the invocation that just finished to PostHog,
// then shows the one-time telemetry notice on stderr. main calls it after the
// command returns, on success and failure alike, because the exit code and
// duration are known only then.
//
// Nothing here may change what the command did. A panic would replace its
// exit code with Go's 2 and end the process before main flushes the
// command's piped output, so it is recovered and dropped.
func capturePostHogCommand(exitCode int, stderr io.Writer) {
	defer func() {
		if r := recover(); r != nil {
			slog.Debug("usage telemetry failed", "panic", r)
		}
	}()
	// telemetry.Enabled, not the client built at startup: `ox config set
	// telemetry off` must not report itself.
	if startedByOx || cliCtx == nil || cliCtx.Cmd == nil || cliCtx.LongRunning || !telemetry.Enabled() {
		return
	}
	path := cliCtx.CommandPath(cliCtx.Cmd)
	if skipPostHogCommand(path) {
		return
	}
	if telemetry.PostHogConfigured() {
		telemetry.CapturePostHog(postHogCommandEvent, postHogCommandProps(cliCtx, path, exitCode))
	}
	showTelemetryNoticeOnce(stderr)
}

// skipPostHogCommand reports commands that run automatically: coding-agent
// hooks on every tool call, prompt, and turn; the git credential helper on
// every authenticated fetch and push of a SageOx-managed repository; git hooks
// on every commit and push. Sent per run they would outnumber every command
// people choose to run. Session start and end still count, once each per
// session.
func skipPostHogCommand(path string) bool {
	switch path {
	case "agent hook SessionStart", "agent hook SessionEnd":
		return false
	case "git-credential-helper", "hooks commit-msg", "hooks post-commit", "hooks post-rewrite", "hooks pre-push":
		return true
	}
	return path == "agent hook" || strings.HasPrefix(path, "agent hook ")
}

// postHogCommandProps describes an invocation without anything the user
// typed: no argument or flag values, paths, or error text. Inside a repository
// set up for SageOx it adds the repository's and team's SageOx IDs, which let
// usage be counted per team; SageOx can map them to the team, so these events
// are not anonymous at the team level.
func postHogCommandProps(c *cli.Context, path string, exitCode int) map[string]any {
	actor, agentType := oxActorDetector{}.DetectActor()
	if agentType == "ci" { // friction files CI under the agent actor
		actor, agentType = "ci", ""
	}
	initialized := c.ProjectRoot != "" && config.IsInitialized(c.ProjectRoot)
	props := map[string]any{
		"command":          path,
		"flags":            setFlagNames(c.Cmd),
		"success":          exitCode == 0,
		"exit_code":        exitCode,
		"duration_ms":      time.Since(c.CommandStartTime).Milliseconds(),
		"actor":            string(actor),
		"agent_type":       agentType,
		"repo_initialized": initialized,
		"ephemeral":        ephemeral.Reason(),
	}
	if initialized {
		if project, err := config.LoadProjectConfig(c.ProjectRoot); err == nil && project != nil {
			if project.RepoID != "" {
				props["repo_id"] = project.RepoID
			}
			if project.TeamID != "" {
				props["team_id"] = project.TeamID
			}
		}
	}
	if kind := postHogErrorKind(c.Err, exitCode); kind != "" {
		props["error_kind"] = kind
	}
	return props
}

// setFlagNames lists the flags given on the command line, by name.
func setFlagNames(cmd *cobra.Command) []string {
	names := []string{}
	cmd.Flags().Visit(func(f *pflag.Flag) { names = append(names, f.Name) })
	return names
}

// postHogErrorKind classifies a failure without its message, which can carry
// paths and other user input.
func postHogErrorKind(err error, exitCode int) string {
	var netErr net.Error
	switch {
	case exitCode == 0:
		return ""
	case exitCode == 130, errors.Is(err, context.Canceled):
		return "interrupted"
	case errors.Is(err, api.ErrUnauthorized), errors.Is(err, auth.ErrEnvTokenMalformed):
		return "auth"
	case errors.Is(err, api.ErrVersionUnsupported):
		return "version_unsupported"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.As(err, &netErr):
		return "network"
	case exitCode == 2:
		return "usage"
	default:
		return "other"
	}
}

// telemetryNotice is shown once per install, the first time a person runs ox
// at a terminal.
const telemetryNotice = "ox sends usage data (which commands run and whether they worked) to improve it.\n" +
	"Details: ox config get telemetry · Turn off: ox config set telemetry off"

// showTelemetryNoticeOnce prints telemetryNotice unless it was shown before or
// no person is reading. An AI coworker's transcript and --json output get no
// prose, the same audience rule as update notices. The shown flag is saved
// before printing, so a config that can't be saved means no notice rather
// than one on every run.
func showTelemetryNoticeOnce(w io.Writer) {
	if updatenotice.Suppressed() {
		return
	}
	userCfg, err := config.LoadUserConfig()
	if err != nil || userCfg.HasSeenTelemetryNotice() {
		return
	}
	userCfg.SetTelemetryNoticeShown(true)
	if config.SaveUserConfig(userCfg) != nil {
		return
	}
	fmt.Fprintln(w, cli.StyleDim.Render(telemetryNotice))
}
