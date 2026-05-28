package main

// doctor_ephemeral.go surfaces the ephemeral-mode predicate so `ox doctor`
// makes it obvious whether the daemon-based code path is active or
// whether the CLI is in HTTP-only ephemeral mode. Mirrors the style of
// other one-liner doctor checks; no fix action — the mode is governed by
// env vars, venue markers, CI signals, and user config (see ADR-018).

import (
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/ephemeral"
)

// checkEphemeralMode reports whether ephemeral mode is active and which
// source triggered it. When inactive, it surfaces the daemon socket path
// so users can grep logs / connect manually if needed.
func checkEphemeralMode() checkResult {
	if ephemeral.IsEphemeral() {
		reason := ephemeral.Reason()
		return checkResult{
			name:    "ephemeral mode",
			passed:  true,
			message: "ACTIVE",
			detail:  "source=" + reason + " (daemon disabled, HTTP-only reads)",
		}
	}
	return checkResult{
		name:    "ephemeral mode",
		passed:  true,
		message: "INACTIVE",
		detail:  "daemon_socket=" + daemon.SocketPath(),
	}
}
