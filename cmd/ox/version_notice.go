package main

import (
	"fmt"
	"time"

	"github.com/sageox/ox/internal/updatenotice"
	"github.com/sageox/ox/internal/version"
)

// updateNoticeDue answers "may we bring up the available upgrade right now?"
// and returns the release line to stamp once we have.
//
// Callers stamp with updatenotice.RecordNotified(line, now) only AFTER the
// notice actually reaches someone — a notice we decided not to show must not
// consume the day's budget.
func updateNoticeDue(now time.Time) (line string, due bool) {
	line = updatenotice.Line(version.Version)
	return line, updatenotice.ShouldNotify(readVersionCache(), line, now)
}

// calmUpdateNoticeDue is updateNoticeDue plus the audience check, for the calm
// tier (the GitHub-derived "update available" line in `ox status`).
//
// The calm tier is strictly for a human at a terminal. Inside a coding agent
// both streams land in the transcript, where an upgrade nudge costs context
// tokens on every command; `ox agent prime` carries the same fact as structured
// data instead, which the agent can act on without it being shouted.
func calmUpdateNoticeDue(now time.Time) (line string, due bool) {
	if updatenotice.Suppressed() {
		return "", false
	}
	return updateNoticeDue(now)
}

// formatCalmUpdateNotice renders the calm tier's one and only line. The urgent
// tier's copy belongs to the server (it is the X-SageOx-Deprecated value,
// printed verbatim); this is the client-owned half.
func formatCalmUpdateNotice(latest, current string) string {
	return fmt.Sprintf("→ ox %s available (you're on %s) — run 'ox upgrade'", latest, current)
}
