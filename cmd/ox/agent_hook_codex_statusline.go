package main

import (
	"io"
	"log/slog"

	"github.com/sageox/ox/internal/oxactivity"
)

// Codex status line digest.
//
// Codex has no status line a command can draw: the CLI's tui.status_line takes
// built-in item ids only, and the ChatGPT desktop app has no status bar. The
// one hook field either host shows the coworker is systemMessage. So the line
// Claude Code keeps in its status bar is shown in Codex as a notice at the end
// of a turn, and only when it differs from the last one shown: a turn that
// pulled no new team context prints nothing.

const codexEventStop = "Stop"

// codexStatuslineDigest is ox's status line for this Codex session as plain
// text, or "" when there is nothing to show: the status line is off, the
// session is not primed, or nothing has been counted yet.
func codexStatuslineDigest(ctx *HookContext) string {
	if ctx == nil || ctx.Input == nil || ctx.Marker == nil {
		return ""
	}
	line, show := buildOxStatusline(statuslineInput{SessionID: ctx.Input.SessionID}, ctx.ProjectRoot)
	if !show || line.counts.IsZero() {
		return ""
	}
	// Plain text: this is hook protocol output, not a terminal.
	digest := oxactivity.Render(line.counts, false)
	if line.recording {
		digest += statuslineRecMark
	}
	return digest
}

// emitCodexStopDigest writes the digest as Codex's Stop output when it has
// changed since it was last shown. It writes one JSON document or nothing.
func emitCodexStopDigest(w io.Writer, ctx *HookContext) {
	digest := codexStatuslineDigest(ctx)
	if digest == "" {
		return
	}
	key := oxactivity.StateKey(ctx.Marker.AgentID, ctx.Input.SessionID)
	changed := false
	err := oxactivity.UpdateState(ctx.ProjectRoot, key, func(s *oxactivity.State) bool {
		if s.ShownLine == digest {
			return false
		}
		s.ShownLine = digest
		changed = true
		return true
	})
	if err != nil {
		// Unrecorded, it would repeat on every turn; stay silent instead.
		slog.Debug("statusline: record shown digest failed", "agent_id", ctx.Marker.AgentID, "err", err)
		return
	}
	if changed {
		writeCodexHookDoc(w, codexEventStop, codexHookOutput{SystemMessage: digest})
	}
}
