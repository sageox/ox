package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/oxactivity"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/tokens"
)

// Usage trace: every ox call an AI coworker makes is recorded as an `invoked`
// context-trace event, so `ox brief` can report what ox put in front of the
// model in a session. Written from the PostToolUse hook for both harnesses.
// Everything here is best-effort: errors are logged at debug and never reach
// the hook's stdout.

// agentTraceCacheMaxAge bounds how long a not-recording agent's usage trace is
// kept in the project cache.
const agentTraceCacheMaxAge = 7 * 24 * time.Hour

// recordOxToolCall classifies one harness tool call and, when it is ox work,
// (a) counts a read of one item of team context (a plan, a discussion, the
// Team Context) on the status line and (b) appends an `invoked` event to the
// AI coworker's usage trace. Non-ox tools return after classification, paying
// no config or recording-state read.
func recordOxToolCall(projectRoot, agentID string, input *agentx.HookInput) {
	if input == nil || projectRoot == "" || agentID == "" {
		return
	}
	act, ok := oxactivity.Classify(input.ToolName, input.ToolInput)
	if !ok {
		return
	}
	refType := invokedRefType(act)
	recordStatuslineRetrieval(projectRoot, agentID, input.SessionID, act, refType)

	// prime records its own richer event (recordPrimeInvocation) — a second,
	// smaller one from the hook would double-count its context.
	if act.Command == "agent prime" {
		return
	}
	if !config.TraceConsultationsEnabled() {
		return
	}

	out := toolOutput(input.ToolResponse)
	ev := contexttrace.Event{
		Type:         contexttrace.EventInvoked,
		Source:       contexttrace.SourceOxCLI,
		Command:      act.Command,
		Label:        act.Label,
		RefType:      refType,
		OutputBytes:  len(out),
		InlineTokens: tokens.EstimateTokens(out),
		Failed:       input.ToolError != "",
	}
	if act.Kind == oxactivity.KindRetrieval {
		ev.Query = act.Query
	}
	appendUsageTraceEvent(projectRoot, agentID, ev)
}

// invokedRefType names what kind of team knowledge a retrieval pulled, using
// the same ref_type vocabulary as consulted events. Non-retrievals carry none.
func invokedRefType(act oxactivity.Activity) string {
	if act.Kind != oxactivity.KindRetrieval {
		return ""
	}
	cmd := strings.TrimPrefix(act.Command, "mcp ")
	switch {
	case cmd == "query" || cmd == "Search":
		return "query"
	case strings.HasPrefix(cmd, "code "):
		return "code"
	case strings.HasPrefix(cmd, "murmur") || cmd == "ListMurmurs":
		return "murmur"
	case strings.HasPrefix(cmd, "plan ") || cmd == "GetPlan":
		return "plan"
	case strings.HasPrefix(cmd, "session "):
		return "session"
	case strings.HasPrefix(cmd, "conversation") || strings.HasPrefix(cmd, "Conversation"):
		return "discussion"
	case strings.HasPrefix(cmd, "decision "):
		return "decision"
	case cmd == "agent team-ctx":
		return "team-context"
	}
	return ""
}

// toolOutput returns the text a tool call put back into the model's context.
// Claude Code's Bash response is {"stdout":…,"stderr":…} and only stdout is
// what the ox command printed; a JSON string is its decoded text; any other
// shape counts at its raw size. Absent → "" (size unknown, field omitted).
func toolOutput(resp json.RawMessage) string {
	trimmed := bytes.TrimSpace(resp)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return ""
	}
	switch trimmed[0] {
	case '{':
		var bash struct {
			Stdout *string `json:"stdout"`
		}
		if json.Unmarshal(trimmed, &bash) == nil && bash.Stdout != nil {
			return *bash.Stdout
		}
	case '"':
		var s string
		if json.Unmarshal(trimmed, &s) == nil {
			return s
		}
	}
	return string(trimmed)
}

// appendUsageTraceEvent writes one usage event to the AI coworker's trace:
// the live recording's context-trace.jsonl when recording (so it travels with
// the session through upload, secret scan and abort), else a per-agent file in
// the project cache (gitignored, pruned after a week).
func appendUsageTraceEvent(projectRoot, agentID string, ev contexttrace.Event) {
	w, recording := usageTraceWriter(projectRoot, agentID)
	if w == nil {
		return
	}
	if recording != nil && ev.Seq == 0 {
		ev.Seq = recording.EntryCount // the turn this call landed at
	}
	if err := w.Append(ev); err != nil {
		slog.Debug("usage trace: append failed", "agent_id", agentID, "command", ev.Command, "err", err)
	}
}

// usageTraceWriter resolves where an agent's usage events go. It returns the
// recording state when the destination is a live session, nil otherwise.
func usageTraceWriter(projectRoot, agentID string) (*contexttrace.Writer, *session.RecordingState) {
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	if err != nil {
		slog.Debug("usage trace: recording state", "agent_id", agentID, "err", err)
	}
	if err == nil && state != nil && state.SessionPath != "" {
		return contexttrace.NewWriter(state.SessionPath), state
	}

	// not recording: the agent ID names a file, so it must be a real one
	if !agentinstance.IsValidAgentID(agentID) {
		slog.Debug("usage trace: invalid agent id for cache file", "agent_id", agentID)
		return nil, nil
	}
	dir := agentTraceCacheDir(projectRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Debug("usage trace: cache dir", "err", err)
		return nil, nil
	}
	pruneAgentTraceCache(dir, time.Now())
	return contexttrace.NewFileWriter(filepath.Join(dir, agentID+".jsonl")), nil
}

// agentTraceCacheDir is where usage events land when no recording is live —
// the same project-cache dir prime falls back to for its provided events.
func agentTraceCacheDir(projectRoot string) string {
	return filepath.Join(projectRoot, ".sageox", "cache", "context-trace")
}

// pruneAgentTraceCache removes per-agent usage files older than a week. It
// only touches files this code writes (<agentID>.jsonl), never prime's shared
// context-trace.jsonl.
func pruneAgentTraceCache(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".jsonl") || !agentinstance.IsValidAgentID(strings.TrimSuffix(name, ".jsonl")) {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < agentTraceCacheMaxAge {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			slog.Debug("usage trace: prune", "file", name, "err", err)
		}
	}
}
