package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/ephemeral"
	"github.com/sageox/ox/internal/errkind"
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
// typed: no argument or flag values, paths, or values from an error message.
// Inside a repository set up for SageOx it adds the repository's and team's
// SageOx IDs, which let usage be counted per team; SageOx can map them to the
// team, so these events are not anonymous at the team level. What a command
// recorded about how it ended (cli.Context.SetOutcome) is added last.
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
		if detail := postHogErrorDetail(c.Err); detail != "" {
			props["error_detail"] = detail
		}
	}
	for k, v := range c.Outcome() {
		props[k] = v
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
// paths and other user input. An error ox created carries its kind
// (errkind.Errorf); the rest are classified from the standard library's
// errors and the exit code.
func postHogErrorKind(err error, exitCode int) string {
	var netErr net.Error
	var opErr *net.OpError
	switch {
	case exitCode == 0:
		return ""
	case exitCode == 130, errors.Is(err, context.Canceled):
		return string(errkind.Interrupted)
	case errkind.Of(err) != "":
		return string(errkind.Of(err))
	// The daemon's socket is the only unix socket ox dials, so this is the
	// daemon down or not answering. Checked before net.Error, which an
	// OpError also satisfies.
	case errors.As(err, &opErr) && opErr.Net == "unix":
		return string(errkind.Daemon)
	case errors.Is(err, context.DeadlineExceeded):
		return string(errkind.Timeout)
	case errors.As(err, &netErr):
		return string(errkind.Network)
	case exitCode == 2:
		return string(errkind.Usage)
	default:
		return string(errkind.Other)
	}
}

// postHogMaxDetail bounds error_detail, so a long detail cannot push the
// event past the 4 KiB limit at which CapturePostHog drops it.
const postHogMaxDetail = 200

// postHogErrorDetail names which failure it was, with no value from its
// message: the detail an error ox created carries (errkind), else the
// operating system's description of an OS error in the chain, else the Go
// type at the bottom of the chain. A plain message has no detail.
func postHogErrorDetail(err error) string {
	detail := errkind.DetailOf(err)
	var errno syscall.Errno
	switch {
	case detail != "" || err == nil:
	case errors.As(err, &errno):
		detail = "syscall.Errno: " + errno.Error()
	default:
		root := err
		for next := errors.Unwrap(root); next != nil; next = errors.Unwrap(root) {
			root = next
		}
		// The errors and fmt packages' types hold only a message, or a join.
		if t := reflect.TypeOf(root).String(); !strings.HasPrefix(t, "*errors.") && !strings.HasPrefix(t, "*fmt.") {
			detail = t
		}
	}
	if len(detail) > postHogMaxDetail {
		detail = strings.ToValidUTF8(detail[:postHogMaxDetail], "")
	}
	return detail
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
