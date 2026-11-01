package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/charmbracelet/colorprofile"
	"github.com/sageox/agentx"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/pet"
	"github.com/sageox/ox/internal/theme"
)

// login_arrival.go — the parts of `ox login`'s two attention moments that need
// ox's state: the device-code response, the stored login, the terminal. The
// text itself is internal/cli/login_arrival.go.

// Oxy's celebration beside the success block: one still from the confetti
// loop, arms up, small enough that login stays a step and not a destination.
const (
	loginArtAnimation = "confetti"
	loginArtPosition  = 0.35
	loginArtRows      = 6
)

// writeDeviceInstructions prints where to go and the code to match there.
func writeDeviceInstructions(out io.Writer, deviceCode *auth.DeviceCodeResponse) {
	fmt.Fprintln(out)
	if deviceCode.VerificationURIComplete != "" {
		fmt.Fprint(out, cli.DeviceCodeLines(deviceCode.VerificationURIComplete, deviceCode.UserCode, true))
	} else {
		fmt.Fprint(out, cli.DeviceCodeLines(deviceCode.VerificationURI, deviceCode.UserCode, false))
	}
	fmt.Fprintln(out)
}

// loginFailure is the error for a device flow that ended without a login. A
// denied or expired request gets one line saying what to do about it, because
// the next step is always the same and a bare "authorization was denied" leaves
// the person to work that out.
//
// ctx is the poll's own window, which lasts as long as the code does. When it
// runs out it can win the race against the server's "expired" answer; to the
// person that is the same event. A deadline error while ctx is still open is
// something else (an HTTP timeout on a flaky network), so it is not called one.
func loginFailure(ctx context.Context, err error) error {
	windowClosed := errors.Is(ctx.Err(), context.DeadlineExceeded)
	switch {
	case errors.Is(err, auth.ErrDeviceAuthorizationDenied):
		return fmt.Errorf("authentication failed: %w\n  Nothing was shared. Run `ox login` to try again", err)
	case errors.Is(err, auth.ErrDeviceCodeExpired), windowClosed && errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("authentication failed: %w\n  The code expired before it was approved. Run `ox login` for a new one", auth.ErrDeviceCodeExpired)
	}
	return fmt.Errorf("authentication failed: %w", err)
}

// isReturningLogin reports whether this sign-in is by someone ox already held a
// login for on this endpoint, expired or not. "Welcome back" is only true then.
//
// Call it with the record read BEFORE the device flow: the flow overwrites the
// record it would be compared with. A different account on the same endpoint
// is a new person here, not a return.
func isReturningLogin(prior *auth.StoredToken, now auth.UserInfo) bool {
	if prior == nil {
		return false
	}
	switch {
	case prior.UserInfo.UserID != "" && now.UserID != "":
		return prior.UserInfo.UserID == now.UserID
	case prior.UserInfo.Email != "" && now.Email != "":
		return strings.EqualFold(prior.UserInfo.Email, now.Email)
	}
	return false
}

// loginArtEnv is everything that decides whether Oxy may appear.
type loginArtEnv struct {
	StdoutTerminal bool
	Interactive    bool // cli.IsInteractive: a TTY on stdin, and no --no-input, --no-interactive or CI
	Quiet          bool
	JSON           bool
	AgentContext   bool
	Profile        colorprofile.Profile
}

// allowed is true only for a person at a color terminal. A pipe, CI, an AI
// coworker, --quiet, --json and NO_COLOR all get the text alone: calm, precise
// output, in the same bytes every run.
func (e loginArtEnv) allowed() bool {
	return e.StdoutTerminal && e.Interactive && !e.Quiet && !e.JSON && !e.AgentContext &&
		e.Profile >= colorprofile.ANSI256
}

func currentLoginArtEnv(out io.Writer) loginArtEnv {
	env := loginArtEnv{
		StdoutTerminal: isTerminalWriter(out),
		Interactive:    cli.IsInteractive(),
		AgentContext:   agentx.IsAgentContext(),
		Profile:        theme.Profile,
	}
	if cfg != nil {
		env.Quiet, env.JSON = cfg.Quiet, cfg.JSON
	}
	return env
}

// loginArt is Oxy's still for the success block, one string per terminal row,
// or nil when it may not or cannot be drawn. Art is never worth failing a login
// over.
func loginArt(out io.Writer) []string {
	if !currentLoginArtEnv(out).allowed() {
		return nil
	}
	art, err := pet.TerminalFrame(loginArtAnimation, loginArtPosition, loginArtRows)
	if err != nil {
		slog.Debug("login: Oxy art unavailable", "error", err)
		return nil
	}
	return art.Lines
}
