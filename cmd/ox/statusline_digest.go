package main

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/sageox/agentx"

	"github.com/sageox/ox/internal/flowlane"
	"github.com/sageox/ox/internal/oxactivity"
	"github.com/sageox/ox/internal/session"
)

// Status line digest: ox's status line as a turn-end notice, for hosts that
// cannot draw Claude Code's status bar.
//
//   - Codex has no status line a command can draw: the CLI's tui.status_line
//     takes built-in item ids only, and the ChatGPT desktop app has no status
//     bar.
//   - Conductor runs Claude Code through its SDK and draws its own chat, so
//     Claude Code's statusLine command never runs there.
//
// The one hook field these hosts show the coworker is systemMessage. So the
// line is shown as a notice at the end of a turn, and only when it differs
// from the last one shown: a turn that pulled no new team context prints
// nothing.
//
// Flow lanes (flow_lanes.go) are one still row per ox call that moved team
// data. These hosts have no surface a plugin can animate, so the lanes arrive
// settled; `ox statusline --watch` draws the same lanes live in a terminal
// pane.
//
//   - Codex shows each lane under the call that made it: PostToolUse may
//     carry a systemMessage, and that is where the lane rides. Its notice has
//     no status line.
//   - Claude Code inside Conductor shows the status line with the turn's
//     lanes beneath it, at the end of the turn.

const codexEventStop = "Stop"

const (
	// digestMaxLanes caps the lanes in one notice; the rest are counted.
	digestMaxLanes = 5
	// digestLaneColumns keeps a lane on one line in a chat transcript.
	digestLaneColumns = 100
	// digestFirstNoticeWindow bounds what a session's first notice may show
	// when it is not recording: the not-recording trace is kept per AI
	// coworker for a week, and calls from an earlier sitting are not this
	// turn's news.
	digestFirstNoticeWindow = 10 * time.Minute
)

// detectOrchestrator names the app running the AI coworker ("conductor", …),
// or "" when there is none. A variable so tests can stand in for Conductor.
var detectOrchestrator = agentx.OrchestratorType

// stopDigestEnabled reports whether this Stop shows the digest. Codex always
// does. Claude Code does only inside Conductor: in a terminal its status bar
// already shows the line, and the desktop app has the session report band.
func stopDigestEnabled(ctx *HookContext) bool {
	if ctx == nil {
		return false
	}
	switch ctx.AgentType {
	case "codex":
		return true
	case "claude-code":
		return detectOrchestrator() == string(agentx.AgentTypeConductor)
	default:
		return false
	}
}

// statuslineDigestLine is ox's status line for this session as plain text.
// ok is false when there is no line to show at all: the status line is off
// or the session is not primed. counted is false while nothing has been
// counted yet; that line ("● ox") is worth showing only above a flow lane.
func statuslineDigestLine(ctx *HookContext) (text string, counted, ok bool) {
	if ctx == nil || ctx.Input == nil || ctx.Marker == nil {
		return "", false, false
	}
	line, show := buildOxStatusline(statuslineInput{SessionID: ctx.Input.SessionID}, ctx.ProjectRoot)
	if !show {
		return "", false, false
	}
	// Plain text: this is hook protocol output, not a terminal.
	text = oxactivity.Render(line.counts, false)
	if line.recording {
		text += statuslineRecMark
	}
	return text, !line.counts.IsZero(), true
}

// digestLanes renders the flow lanes not yet shown, as plain still rows.
// shown is how many the last notice had reached; firstNotice is whether this
// session has shown none yet.
func digestLanes(flows []finishedFlow, shown int, firstNotice bool, now time.Time) []string {
	if shown > len(flows) {
		shown = 0 // a new trace (a new recording): start over
	}
	fresh := flows[shown:]
	if firstNotice {
		for len(fresh) > 0 && !fresh[0].ended.IsZero() && now.Sub(fresh[0].ended) > digestFirstNoticeWindow {
			fresh = fresh[1:]
		}
	}
	extra := 0
	if len(fresh) > digestMaxLanes {
		extra = len(fresh) - digestMaxLanes
		fresh = fresh[extra:]
	}
	rows := make([]string, 0, len(fresh)+1)
	if extra > 0 {
		rows = append(rows, fmt.Sprintf("+%d earlier %s", extra, pluralNoun("ox call", extra)))
	}
	for _, f := range fresh {
		rows = append(rows, flowlane.Render(f.lane, flowlane.AnimDuration, flowlane.Options{Columns: digestLaneColumns, Still: true}))
	}
	return rows
}

// digestShowsStatusline reports whether this host's notice leads with ox's
// status line. Codex's does not: there the notice is the flow lanes alone,
// each shown under the call that made it (emitCodexFlowNotice).
func digestShowsStatusline(ctx *HookContext) bool {
	return ctx.AgentType != "codex"
}

// pendingDigest is what this session has not yet been shown: the status
// line when the host shows it and it changed, and the flow lanes of ox
// calls made since the last notice. It records them as shown; "" means
// there is no news.
func pendingDigest(ctx *HookContext) string {
	if ctx == nil || ctx.Input == nil || ctx.Marker == nil {
		return ""
	}
	line, counted, ok := statuslineDigestLine(ctx)
	if !ok {
		return ""
	}
	withLine := digestShowsStatusline(ctx)
	var flows []finishedFlow
	trace := ""
	if flowLanesEnabled() {
		trace = flowTracePath(ctx.ProjectRoot, ctx.Marker.AgentID)
		flows = finishedFlowsAt(trace, ctx.Marker.AgentID)
	}
	showsLine := withLine && counted
	if !showsLine && len(flows) == 0 {
		return "" // nothing to show: no state write, no notice
	}
	key := oxactivity.StateKey(ctx.Marker.AgentID, ctx.Input.SessionID)
	recordingID := ""
	if rec, err := session.LoadRecordingStateForAgent(ctx.ProjectRoot, ctx.Marker.AgentID); err == nil && rec != nil {
		recordingID = rec.SessionID
	}
	var message string
	err := oxactivity.UpdateState(ctx.ProjectRoot, key, func(s *oxactivity.State) bool {
		changed := s.BindRecording(recordingID)
		if trace != "" && s.ShownFlowsTrace != trace {
			// A recording started or ended: these are another trace's lanes,
			// and none of them has been shown.
			s.ShownFlows, s.ShownFlowsTrace = 0, trace
			changed = true
		}
		firstNotice := s.ShownLine == "" && s.ShownFlows == 0
		lanes := digestLanes(flows, s.ShownFlows, firstNotice, time.Now())
		if s.ShownFlows != len(flows) {
			s.ShownFlows = len(flows)
			changed = true
		}
		lineChanged := withLine && counted && s.ShownLine != line
		if !lineChanged && len(lanes) == 0 {
			return changed
		}
		rows := lanes
		if withLine {
			if counted {
				s.ShownLine = line
			}
			rows = append([]string{line}, lanes...)
		}
		message = strings.Join(rows, "\n")
		return true
	})
	if err != nil {
		// Unrecorded, it would repeat on every turn; stay silent instead.
		slog.Debug("statusline: record shown digest failed", "agent_id", ctx.Marker.AgentID, "err", err)
		return ""
	}
	return message
}

// emitStopDigest writes the digest as the Stop hook's output when there is
// news since it was last shown. It writes one JSON document or nothing.
// Codex and Claude Code read the same {"systemMessage": …} shape. In Codex
// the lanes are normally shown call by call, so this is silent unless one was
// missed (a call whose PostToolUse printed nothing).
func emitStopDigest(w io.Writer, ctx *HookContext) {
	if message := pendingDigest(ctx); message != "" {
		writeCodexHookDoc(w, codexEventStop, codexHookOutput{SystemMessage: message})
	}
}

// codexFlowNotice is the flow lane of the ox call PostToolUse just recorded,
// for Codex to show under that call. It is "" for any other tool call,
// decided from the call alone before any file is read.
func codexFlowNotice(ctx *HookContext) string {
	if ctx == nil || ctx.Input == nil {
		return ""
	}
	act, ok := oxactivity.Classify(ctx.Input.ToolName, ctx.Input.ToolInput)
	if !ok {
		return ""
	}
	if _, moves := oxactivity.FlowOf(act.Command); !moves {
		return ""
	}
	return pendingDigest(ctx)
}
