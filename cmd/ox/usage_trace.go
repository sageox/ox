package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/oxactivity"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/session/contextuse"
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
	hits := surfacedToolHits(act.Command, input)
	if singleItemCommands[act.Command] && len(hits) == 1 {
		ev.Ref, ev.Title, ev.URL = hits[0].Ref, hits[0].Title, hits[0].URL
	}
	appendUsageTraceEvent(projectRoot, agentID, ev)
	// An ox CLI command traces its own surfaced items; an MCP tool runs on
	// the server, so its items are traced here from the response.
	if strings.HasPrefix(act.Command, "mcp ") {
		traceProvidedHits(projectRoot, agentID, contexttrace.SourceOnDemand, contexttrace.MechanismRetrieval, hits)
	}
}

// maxSurfacedParseBytes bounds how much of a tool response is parsed for
// surfaced items. A larger response is still counted, just not itemized.
const maxSurfacedParseBytes = 256 << 10

// mcpSurfacingTools are the SageOx MCP tools whose response names specific
// team items, mapped to the ref_type of those items ("" = per item, from its
// doc_type/source_type).
var mcpSurfacingTools = map[string]string{
	"Search":                 "",
	"Conversation":           "discussion",
	"ConversationDigest":     "discussion",
	"ConversationTranscript": "discussion",
	"GetPlan":                "plan",
	"ListMurmurs":            "murmur",
}

// singleItemCommands return exactly one team item, so their invoked event
// can name it.
var singleItemCommands = map[string]bool{
	"conversation show":          true,
	"mcp Conversation":           true,
	"mcp ConversationDigest":     true,
	"mcp ConversationTranscript": true,
	"mcp GetPlan":                true,
}

// surfacedToolHits extracts the team items a successful tool call surfaced:
// from the response of a surfacing MCP tool, or the stdout of a single-item
// ox command. Refs, titles and links only.
func surfacedToolHits(command string, input *agentx.HookInput) []providedHit {
	if input.ToolError != "" || len(input.ToolResponse) > maxSurfacedParseBytes {
		return nil
	}
	if tool, ok := strings.CutPrefix(command, "mcp "); ok {
		refType, surfacing := mcpSurfacingTools[tool]
		if !surfacing {
			return nil
		}
		return surfacedItems(responseDocs(input.ToolResponse), refType)
	}
	if command == "conversation show" {
		return surfacedItems(textDocs([]string{toolOutput(input.ToolResponse)}), "discussion")
	}
	return nil
}

// responseDocs decodes the JSON documents in a tool response: an MCP content
// array ([{"type":"text","text":"<json>"}]), a {"content":[…]} or
// {"structuredContent":{…}} result, a JSON string, or a bare JSON value.
func responseDocs(resp json.RawMessage) []any {
	var v any
	if json.Unmarshal(resp, &v) != nil {
		return nil
	}
	switch t := v.(type) {
	case string:
		return textDocs([]string{t})
	case []any:
		if texts := contentTexts(t); len(texts) > 0 {
			return textDocs(texts)
		}
	case map[string]any:
		if sc, ok := t["structuredContent"].(map[string]any); ok {
			return []any{sc}
		}
		if c, ok := t["content"].([]any); ok {
			return textDocs(contentTexts(c))
		}
	}
	return []any{v}
}

// contentTexts returns the text of each MCP text content block.
func contentTexts(blocks []any) []string {
	var out []string
	for _, b := range blocks {
		if m, ok := b.(map[string]any); ok {
			if s, ok := m["text"].(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// textDocs parses each text that is a JSON object or array; prose is skipped.
func textDocs(texts []string) []any {
	var out []any
	for _, s := range texts {
		s = strings.TrimSpace(s)
		if s == "" || (s[0] != '{' && s[0] != '[') {
			continue
		}
		var v any
		if json.Unmarshal([]byte(s), &v) == nil {
			out = append(out, v)
		}
	}
	return out
}

// surfacedItemShape is what a response object must look like to count as a
// team item: an id under one of IDKeys (first wins), plus a title or link —
// or an id whose type prefix alone says it is a team item. Text fields
// (text, body, summary, content) are never read.
var surfacedItemShape = struct {
	IDKeys, TitleKeys, URLKeys, TypeKeys []string
	// SkipKeys hold objects that describe people or scopes, not items.
	SkipKeys map[string]bool
	// IDPrefixes mark an id as a team item without a title or link.
	IDPrefixes []string
}{
	IDKeys:     []string{"conversation_id", "recording_id", "plan_id", "session_id", "murmur_id", "source_id", "id", "slug"},
	TitleKeys:  []string{"title", "name", "topic"},
	URLKeys:    []string{"url", "share_url", "link"},
	TypeKeys:   []string{"doc_type", "source_type"},
	SkipKeys:   map[string]bool{"team": true, "teams": true, "author": true, "user": true, "owner": true, "repo": true, "created_by": true, "participants": true, "speakers": true, "from": true},
	IDPrefixes: []string{"cnv_", "rec_", "ses_", "pln_"},
}

// maxSurfacedWalkDepth bounds the search for items inside a response.
const maxSurfacedWalkDepth = 5

// surfacedItems walks decoded documents, collecting up to
// maxProvidedHitsPerCall items in document order. An item is not descended
// into, so a search result's nested hits never count as items of their own.
func surfacedItems(docs []any, refType string) []providedHit {
	var hits []providedHit
	var walk func(v any, depth int)
	walk = func(v any, depth int) {
		if len(hits) >= maxProvidedHitsPerCall || depth > maxSurfacedWalkDepth {
			return
		}
		switch t := v.(type) {
		case []any:
			for _, e := range t {
				walk(e, depth+1)
			}
		case map[string]any:
			if h, ok := surfacedItem(t, refType); ok {
				hits = append(hits, h)
				return
			}
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			slices.Sort(keys) // map order is random; document order is not recoverable
			for _, k := range keys {
				if !surfacedItemShape.SkipKeys[k] {
					walk(t[k], depth+1)
				}
			}
		}
	}
	for _, d := range docs {
		walk(d, 0)
	}
	return hits
}

// surfacedItem reads one response object as a team item, if it is one.
func surfacedItem(m map[string]any, refType string) (providedHit, bool) {
	ref := firstString(m, surfacedItemShape.IDKeys)
	if ref == "" {
		return providedHit{}, false
	}
	title := firstString(m, surfacedItemShape.TitleKeys)
	url := firstString(m, surfacedItemShape.URLKeys)
	if title == "" && url == "" && !hasAnyPrefix(ref, surfacedItemShape.IDPrefixes) {
		return providedHit{}, false
	}
	if refType == "" {
		refType = firstString(m, surfacedItemShape.TypeKeys)
		if refType == "" {
			refType = "doc"
		}
	}
	score, _ := m["score"].(float64)
	return providedHit{SurfacedRef: contextuse.SurfacedRef{Ref: ref, RefType: refType, Title: title, URL: url}, Score: score}, true
}

func firstString(m map[string]any, keys []string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
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
