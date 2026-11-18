package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/agentx"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/flowlane"
	"github.com/sageox/ox/internal/oxactivity"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/contexttrace"
)

// Flow lanes outside the Claude Code plugin: what each ox call moved between
// SageOx and the AI coworker's session, drawn by internal/flowlane under
// each ox call in Codex and in Conductor's turn-end notice
// (statusline_digest.go), and live in `ox statusline --watch`
// (statusline_watch.go).
//
// The facts come from two places, both local:
//
//   - the usage trace: recordOxToolCall writes one `invoked` event per ox
//     call from the PostToolUse hook, with its label, size, item count and
//     duration;
//   - the running marker below: a harness that fires a pre-tool hook (Codex)
//     notes the start of an ox call there, so the watch pane can show it
//     while it runs and the finished event can say how long it took. A
//     harness with no pre-tool hook (Claude Code) simply has no running
//     state and no duration; its plugin draws the lanes itself.

// flowLanesEnabled reads the coworker's settings: the lanes follow the
// status line (off turns both off) and have their own switch.
func flowLanesEnabled() bool {
	cfg, err := config.LoadUserConfig()
	if err != nil || cfg == nil {
		return true // the default; a broken config file must not hide the lanes
	}
	return cfg.IsStatuslineEnabled() && cfg.IsFlowLanesEnabled()
}

// --- the running marker ---

// flowRunningMaxAge is how long a start with no finish is believed. A tool
// call whose PostToolUse never came (the harness was killed, the call was
// denied) must not leave a lane running forever.
const flowRunningMaxAge = flowlane.MarchMax

// flowRunning is one ox call that has started and not yet finished.
type flowRunning struct {
	// Command is the classified command words only, never arguments.
	Command   string `json:"command"`
	Label     string `json:"label"`
	StartedAt int64  `json:"started_at"` // unix milliseconds
}

type flowMarker struct {
	Running []flowRunning `json:"running,omitempty"`
}

// flowMarkerPath is <projectRoot>/.sageox/cache/flow/<agentID>.json, or ""
// for an agent ID that is not a well-formed one (it names a file).
func flowMarkerPath(projectRoot, agentID string) string {
	if projectRoot == "" || !agentinstance.IsValidAgentID(agentID) {
		return ""
	}
	return filepath.Join(projectRoot, ".sageox", "cache", "flow", agentID+".json")
}

func readFlowMarker(path string) flowMarker {
	var m flowMarker
	data, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return flowMarker{} // a corrupt marker is an empty one
	}
	return m
}

// updateFlowMarker applies fn to the marker under its lock, dropping starts
// older than flowRunningMaxAge first. An empty marker removes the file.
func updateFlowMarker(path string, now time.Time, fn func(*flowMarker)) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return fileutil.WithFileLock(context.Background(), path, func() error {
		m := readFlowMarker(path)
		m.Running = liveFlowRunning(m.Running, now)
		fn(&m)
		if len(m.Running) == 0 {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			return nil
		}
		return fileutil.AtomicWriteJSON(path, m, 0o600)
	})
}

func liveFlowRunning(in []flowRunning, now time.Time) []flowRunning {
	out := in[:0:0]
	for _, r := range in {
		if age := now.Sub(time.UnixMilli(r.StartedAt)); age >= 0 && age < flowRunningMaxAge {
			out = append(out, r)
		}
	}
	return out
}

// recordFlowStart notes that an ox call that moves team data is starting. It
// is the whole of the pre-tool hook: a tool call that is not such an ox call
// returns after classification, before any file is read. Best-effort: nothing
// here can fail or slow the tool call beyond one small file write.
func recordFlowStart(projectRoot string, input *agentx.HookInput, now time.Time) {
	if input == nil || projectRoot == "" || input.SessionID == "" {
		return
	}
	act, ok := oxactivity.Classify(input.ToolName, input.ToolInput)
	if !ok {
		return
	}
	if _, moves := oxactivity.FlowOf(act.Command); !moves {
		return
	}
	marker, err := ReadSessionMarker(input.SessionID)
	if err != nil || marker == nil {
		return // not primed: no AI coworker to file it under
	}
	path := flowMarkerPath(projectRoot, marker.AgentID)
	if path == "" || !flowLanesEnabled() {
		return
	}
	err = updateFlowMarker(path, now, func(m *flowMarker) {
		m.Running = append(m.Running, flowRunning{Command: act.Command, Label: act.Label, StartedAt: now.UnixMilli()})
	})
	if err != nil {
		slog.Debug("flow: record start failed", "agent_id", marker.AgentID, "command", act.Command, "err", err)
	}
}

// takeFlowStart removes the oldest running start of command and reports how
// long ago it began. ok is false when there was none: the harness fires no
// pre-tool hook, or the start aged out.
func takeFlowStart(projectRoot, agentID, command string, now time.Time) (elapsed time.Duration, ok bool) {
	path := flowMarkerPath(projectRoot, agentID)
	if path == "" {
		return 0, false
	}
	if _, err := os.Stat(path); err != nil {
		return 0, false // the common case (no pre-tool hook): no lock, no write
	}
	err := updateFlowMarker(path, now, func(m *flowMarker) {
		for i, r := range m.Running {
			if r.Command == command {
				elapsed, ok = now.Sub(time.UnixMilli(r.StartedAt)), true
				m.Running = append(m.Running[:i:i], m.Running[i+1:]...)
				return
			}
		}
	})
	if err != nil {
		slog.Debug("flow: take start failed", "agent_id", agentID, "command", command, "err", err)
		return 0, false
	}
	return max(elapsed, time.Millisecond), ok
}

// runningFlow is one ox call still in progress as a lane, with how long ago
// it started: what its marching dots are drawn from.
type runningFlow struct {
	lane    flowlane.Lane
	elapsed time.Duration
}

// runningFlowLanes are the ox calls of agentID still in progress, oldest first.
func runningFlowLanes(projectRoot, agentID string, now time.Time) []runningFlow {
	path := flowMarkerPath(projectRoot, agentID)
	if path == "" {
		return nil
	}
	var lanes []runningFlow
	for _, r := range liveFlowRunning(readFlowMarker(path).Running, now) {
		flow, ok := oxactivity.FlowOf(r.Command)
		if !ok {
			continue
		}
		lanes = append(lanes, runningFlow{
			lane:    flowlane.Lane{Direction: flow.Direction, Label: r.Label, Step: flow.Step, Running: true},
			elapsed: now.Sub(time.UnixMilli(r.StartedAt)),
		})
	}
	return lanes
}

// --- what a finished call moved ---

// flowItemNoun is what a command's results are called, singular; "" for a
// command whose result is not a list of items.
func flowItemNoun(command string) string {
	switch {
	case command == "query", command == "mcp Search", command == "conversation search", command == "conversation list":
		return "discussion"
	case strings.HasPrefix(command, "session "):
		return "session"
	case strings.HasPrefix(command, "code "):
		return "match"
	case command == "murmur list", command == "mcp ListMurmurs":
		return "murmur"
	}
	return ""
}

func pluralNoun(noun string, n int) string {
	if n == 1 {
		return noun
	}
	for _, suffix := range []string{"ch", "sh", "s", "x"} {
		if strings.HasSuffix(noun, suffix) {
			return noun + "es"
		}
	}
	return noun + "s"
}

// maxFlowItemsParseBytes bounds how much output is parsed for an item count.
const maxFlowItemsParseBytes = 256 << 10

// flowItems counts the items a call's JSON output returned, with their noun:
// a top-level array, or the `results` / `sessions` / `murmurs` list, or
// `team_context.results`, or a stated `total`. It reports 0 when the output
// is not JSON, states no count, or the command has no noun. Counts only: the
// items themselves are never read.
func flowItems(command, out string) (int, string) {
	noun := flowItemNoun(command)
	if noun == "" || len(out) > maxFlowItemsParseBytes {
		return 0, ""
	}
	trimmed := mcpResultText(strings.TrimSpace(out))
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return 0, ""
	}
	n := -1
	if trimmed[0] == '[' {
		var list []json.RawMessage
		if json.Unmarshal([]byte(trimmed), &list) == nil {
			n = len(list)
		}
	} else {
		var obj map[string]json.RawMessage
		if json.Unmarshal([]byte(trimmed), &obj) != nil {
			return 0, ""
		}
		listLen := func(raw json.RawMessage) int {
			var list []json.RawMessage
			if len(raw) == 0 || json.Unmarshal(raw, &list) != nil || list == nil {
				return -1
			}
			return len(list)
		}
		for _, key := range []string{"results", "sessions", "murmurs"} {
			if n = listLen(obj[key]); n >= 0 {
				break
			}
		}
		if n < 0 {
			var tc map[string]json.RawMessage
			if json.Unmarshal(obj["team_context"], &tc) == nil {
				n = listLen(tc["results"])
			}
		}
		if n < 0 {
			var total float64
			if json.Unmarshal(obj["total"], &total) == nil && total >= 0 && len(obj["total"]) > 0 {
				n = int(total)
			}
		}
	}
	if n <= 0 {
		return 0, ""
	}
	return n, pluralNoun(noun, n)
}

// mcpResultText unwraps an MCP tool reply to the JSON it carries: the text of
// a content array ([{"type":"text","text":"<json>"}]) or of a
// {"content":[…]} result, or a {"structuredContent":{…}} object. Without
// it the content blocks themselves would be counted as the results. Anything
// else is returned as it came.
func mcpResultText(trimmed string) string {
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return trimmed
	}
	firstJSON := func(blocks []any) (string, bool) {
		texts := contentTexts(blocks)
		if len(texts) == 0 || len(texts) != len(blocks) {
			return "", false // not a content array: a plain list of results
		}
		for _, s := range texts {
			if s = strings.TrimSpace(s); s != "" && (s[0] == '{' || s[0] == '[') {
				return s, true
			}
		}
		return "", true // content blocks holding prose only: nothing to count
	}
	if trimmed[0] == '[' {
		var blocks []any
		if json.Unmarshal([]byte(trimmed), &blocks) != nil {
			return trimmed
		}
		if text, ok := firstJSON(blocks); ok {
			return text
		}
		return trimmed
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(trimmed), &obj) != nil {
		return trimmed
	}
	if sc := strings.TrimSpace(string(obj["structuredContent"])); strings.HasPrefix(sc, "{") {
		return sc
	}
	var blocks []any
	if len(obj["content"]) > 0 && json.Unmarshal(obj["content"], &blocks) == nil {
		if text, ok := firstJSON(blocks); ok {
			return text
		}
	}
	return trimmed
}

// addFlowFacts fills the flow lane's fields on an invoked event: how long
// the call ran, what it counted, and what an outbound MCP call sent.
func addFlowFacts(ev *contexttrace.Event, flow oxactivity.Flow, input *agentx.HookInput, out string, elapsed time.Duration, timed bool) {
	if timed {
		ev.DurationMS = elapsed.Milliseconds()
	}
	if ev.Failed {
		return
	}
	switch flow.Direction {
	case oxactivity.DirectionIn:
		ev.Items, ev.ItemsNoun = flowItems(ev.Command, out)
	case oxactivity.DirectionOut:
		// An MCP tool's arguments are what it sent. A CLI call's payload is
		// its command line or a file, which ox does not size here.
		if strings.HasPrefix(ev.Command, "mcp ") {
			ev.SentBytes = len(input.ToolInput)
		}
	}
}

// --- lanes from the trace ---

// finishedFlow is one finished ox call as a lane, with when it ended.
type finishedFlow struct {
	lane  flowlane.Lane
	ended time.Time
}

// flowLaneOf turns an invoked trace event into a lane; false for an event
// that is not an ox call, or one that moved no team data.
func flowLaneOf(ev contexttrace.Event) (finishedFlow, bool) {
	if ev.Type != contexttrace.EventInvoked {
		return finishedFlow{}, false
	}
	flow, ok := oxactivity.FlowOf(ev.Command)
	if !ok {
		return finishedFlow{}, false
	}
	lane := flowlane.Lane{
		Direction: flow.Direction,
		Label:     ev.Label,
		Step:      flow.Step,
		Items:     ev.Items,
		ItemsNoun: ev.ItemsNoun,
		Duration:  time.Duration(ev.DurationMS) * time.Millisecond,
		Failed:    ev.Failed,
	}
	if lane.Label == "" {
		lane.Label = "ran ox " + ev.Command
	}
	switch {
	case flow.Direction == oxactivity.DirectionIn:
		lane.Tokens = ev.InlineTokens
	case ev.SentBytes > 0:
		lane.Tokens = (ev.SentBytes + 3) / 4 // tokens.EstimateTokens, from a size
	default:
		lane.Unsized = !ev.Failed // sent, but ox could not see how much
	}
	ended, _ := time.Parse(time.RFC3339, ev.Timestamp)
	return finishedFlow{lane: lane, ended: ended}, true
}

// flowTracePath is where agentID's ox calls are traced: the live recording's
// context-trace.jsonl, else its not-recording cache file (see
// usageTraceWriter, the writer of both).
func flowTracePath(projectRoot, agentID string) string {
	if projectRoot == "" || agentID == "" {
		return ""
	}
	if state, err := session.LoadRecordingStateForAgent(projectRoot, agentID); err == nil && state != nil && state.SessionPath != "" {
		return filepath.Join(state.SessionPath, contexttrace.FileName)
	}
	if !agentinstance.IsValidAgentID(agentID) {
		return ""
	}
	return filepath.Join(agentTraceCacheDir(projectRoot), agentID+".jsonl")
}

// finishedFlowsAt are the finished flow lanes in the trace at path (see
// flowTracePath), oldest first. A missing or unreadable trace is no lanes: a
// lane is decoration, never an error.
func finishedFlowsAt(path, agentID string) []finishedFlow {
	if path == "" {
		return nil
	}
	events, err := contexttrace.ReadEvents(path)
	if err != nil {
		slog.Debug("flow: read trace", "agent_id", agentID, "err", err)
		return nil
	}
	var flows []finishedFlow
	for _, ev := range events {
		if f, ok := flowLaneOf(ev); ok {
			flows = append(flows, f)
		}
	}
	return flows
}
