// session.go handles Gemini CLI session reading, parsing, discovery, and types.
//
// Format (verified against Gemini CLI 0.36.0: its own bundled
// docs/cli/session-management.md, and 68 real session files on the capture
// machine — see testdata/PROVENANCE.md):
//
// Gemini records every session automatically to
// ~/.gemini/tmp/<project_hash>/chats/session-<YYYY-MM-DD>T<HH-MM>-<id8>.json,
// where <id8> is the first 8 characters of the session UUID. The file is a
// single JSON *object*, rewritten in full on each turn:
//
//	{
//	  "sessionId": "...", "projectHash": "...", "kind": "main",
//	  "startTime": "...", "lastUpdated": "...",
//	  "messages": [
//	    {"id","timestamp","type":"user","content":[{"text":"..."}]},
//	    {"id","timestamp","type":"gemini","content":"...",
//	     "model":"...","thoughts":[...],"tokens":{...},
//	     "toolCalls":[{"id","name","args",
//	                   "result":[{"functionResponse":{"response":{"output":"..."}}}],
//	                   "status","timestamp",...}]}
//	  ]
//	}
//
// Two things about that shape are easy to get wrong, and both were:
//   - "content" is polymorphic. User messages carry an array of {text} parts;
//     model messages carry a bare string.
//   - Tool calls hang off the model message as "toolCalls", not as inline
//     content parts. Call and result share the tool call's "id", which is what
//     lets the ledger pair them.
//
// Because the whole file is rewritten each turn, the incremental offset is an
// entry count, not a byte position.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

func handleRead(p adapterprotocol.ReadParams) (*adapterprotocol.ReadResult, error) {
	data, err := os.ReadFile(p.SessionFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}

	entries, meta, err := parseGeminiSession(data)
	if err != nil {
		return nil, err
	}

	return &adapterprotocol.ReadResult{Entries: entries, Metadata: meta}, nil
}

func handleReadMetadata(p adapterprotocol.ReadParams) (*adapterprotocol.ReadMetadataResult, error) {
	data, err := os.ReadFile(p.SessionFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}

	session, err := decodeGeminiSession(data)
	if err != nil {
		return nil, err
	}

	meta := sessionMetadata(session)
	return &adapterprotocol.ReadMetadataResult{
		AgentVersion: meta.AgentVersion,
		Model:        meta.Model,
	}, nil
}

func findGeminiSession(repoRoot, agentID, since, agentSessionID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}

	tmpDir := filepath.Join(home, ".gemini", "tmp")

	if agentSessionID != "" {
		if err := adapterruntime.ValidateSessionID(agentSessionID); err != nil {
			return "", err
		}
		if path, err := findGeminiBySessionID(tmpDir, agentSessionID); err == nil {
			return path, nil
		}
		// fall through to timestamp-based scanning
	}

	sinceTime := time.Time{}
	if since != "" {
		if t, err := time.Parse(time.RFC3339, since); err == nil {
			sinceTime = t
		}
	}

	candidates, err := listGeminiSessionFiles(tmpDir)
	if err != nil {
		return "", err
	}

	filtered := candidates[:0]
	for _, c := range candidates {
		if !sinceTime.IsZero() && !c.modTime.After(sinceTime) {
			continue
		}
		filtered = append(filtered, c)
	}
	candidates = filtered

	if len(candidates) == 0 {
		return "", fmt.Errorf("no gemini sessions found")
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].modTime.After(candidates[j].modTime)
	})

	if agentID != "" {
		for _, c := range candidates {
			if data, err := os.ReadFile(c.path); err == nil {
				if strings.Contains(string(data), agentID) {
					return c.path, nil
				}
			}
		}
	}

	return candidates[0].path, nil
}

type geminiSessionFile struct {
	path    string
	modTime time.Time
}

// listGeminiSessionFiles walks ~/.gemini/tmp/*/chats for session files.
func listGeminiSessionFiles(tmpDir string) ([]geminiSessionFile, error) {
	projectDirs, err := os.ReadDir(tmpDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read gemini tmp dir: %w", err)
	}

	var out []geminiSessionFile
	for _, projEntry := range projectDirs {
		if !projEntry.IsDir() {
			continue
		}
		chatsDir := filepath.Join(tmpDir, projEntry.Name(), "chats")
		files, err := os.ReadDir(chatsDir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasPrefix(f.Name(), "session-") || !strings.HasSuffix(f.Name(), ".json") {
				continue
			}
			info, err := f.Info()
			if err != nil {
				continue
			}
			out = append(out, geminiSessionFile{
				path:    filepath.Join(chatsDir, f.Name()),
				modTime: info.ModTime(),
			})
		}
	}
	return out, nil
}

// findGeminiBySessionID locates the file recording a given session UUID.
//
// Gemini does not name session files after the full id: the real name is
// session-<YYYY-MM-DD>T<HH-MM>-<first 8 chars of the id>.json. Constructing
// session-<full id>.json — which this adapter used to do — can never match a
// file gemini wrote. The authoritative check is the "sessionId" field inside
// the file, so the filename is only used to avoid reading every candidate.
func findGeminiBySessionID(tmpDir, sessionID string) (string, error) {
	candidates, err := listGeminiSessionFiles(tmpDir)
	if err != nil {
		return "", err
	}

	// cheapest first: the id8 suffix gemini actually writes
	var suffix string
	if len(sessionID) >= 8 {
		suffix = "-" + sessionID[:8] + ".json"
	}

	var deferred []geminiSessionFile
	for _, c := range candidates {
		name := filepath.Base(c.path)
		if name == "session-"+sessionID+".json" {
			return c.path, nil
		}
		if suffix != "" && strings.HasSuffix(name, suffix) {
			if sessionIDOf(c.path) == sessionID {
				return c.path, nil
			}
			continue
		}
		deferred = append(deferred, c)
	}

	// fall back to reading the id out of each remaining file
	for _, c := range deferred {
		if sessionIDOf(c.path) == sessionID {
			return c.path, nil
		}
	}

	return "", fmt.Errorf("gemini session %s not found", sessionID)
}

// sessionIDOf reads just the sessionId field, returning "" on any problem.
func sessionIDOf(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var probe struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return ""
	}
	return probe.SessionID
}

// decodeGeminiSession unmarshals the session envelope and rejects anything that
// is not the object shape gemini writes.
func decodeGeminiSession(data []byte) (*geminiSession, error) {
	var session geminiSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("failed to parse gemini session: %w", err)
	}
	return &session, nil
}

// parseGeminiSession converts a gemini session file into ledger entries.
//
// It deliberately fails loudly when a file holds messages but yields no
// entries. That combination means the parser and the on-disk format have
// diverged, and the previous version of this adapter returned exactly that —
// zero entries, no error — against every real gemini transcript, which is
// invisible to any caller that only checks err.
func parseGeminiSession(data []byte) ([]adapterprotocol.RawEntry, *adapterprotocol.SessionMetadata, error) {
	session, err := decodeGeminiSession(data)
	if err != nil {
		return nil, nil, err
	}

	var entries []adapterprotocol.RawEntry
	for i := range session.Messages {
		entries = append(entries, parseGeminiMessage(&session.Messages[i])...)
	}

	if len(entries) == 0 && len(session.Messages) > 0 {
		return nil, nil, fmt.Errorf(
			"gemini session has %d messages but produced no entries — the parser does not match the file format (message types: %s)",
			len(session.Messages), strings.Join(messageTypes(session), ", "))
	}

	return entries, sessionMetadata(session), nil
}

// messageTypes reports the distinct "type" values present, so a format-drift
// error names the shape it actually saw instead of just failing.
func messageTypes(s *geminiSession) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range s.Messages {
		t := m.Type
		if t == "" {
			t = "<missing>"
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// sessionMetadata pulls what gemini records about the run. Gemini never writes
// its own version into the session file, so AgentVersion stays empty.
func sessionMetadata(s *geminiSession) *adapterprotocol.SessionMetadata {
	meta := &adapterprotocol.SessionMetadata{}
	for i := len(s.Messages) - 1; i >= 0; i-- {
		if s.Messages[i].Model != "" {
			meta.Model = s.Messages[i].Model
			break
		}
	}
	return meta
}

func parseGeminiMessage(msg *geminiMessage) []adapterprotocol.RawEntry {
	ts := parseGeminiTime(msg.Timestamp)

	var entries []adapterprotocol.RawEntry
	if text := geminiMessageText(msg.Content); text != "" {
		switch msg.Type {
		case "gemini", "model", "assistant":
			entries = append(entries, adapterruntime.AssistantEntry(ts, text))
		case "user":
			entries = append(entries, adapterruntime.UserEntry(ts, text))
		case "info", "error", "system":
			entries = append(entries, adapterruntime.SystemEntry(ts, text))
		default:
			// an unrecognized type is still a real turn; recording it as a
			// system entry keeps it in the ledger instead of dropping it
			entries = append(entries, adapterruntime.SystemEntry(ts, text))
		}
	}

	// Gemini's thoughts[] are model-internal reasoning summaries. They are
	// intentionally not emitted: the ledger records the conversation, and
	// thoughts duplicate the response they preceded.
	for i := range msg.ToolCalls {
		entries = append(entries, parseGeminiToolCall(&msg.ToolCalls[i], ts)...)
	}

	return entries
}

func parseGeminiToolCall(call *geminiToolCall, fallback time.Time) []adapterprotocol.RawEntry {
	ts := fallback
	if t := parseGeminiTime(call.Timestamp); !t.IsZero() {
		ts = t
	}

	inputJSON := ""
	if call.Args != nil {
		if b, err := json.Marshal(call.Args); err == nil {
			inputJSON = string(b)
		}
	}

	entries := []adapterprotocol.RawEntry{
		adapterruntime.ToolUseWithID(ts, call.Name, inputJSON, call.ID),
	}

	// a result entry is only emitted once gemini has recorded one; an
	// in-flight call (status "executing", "awaiting_approval", ...) has none
	output, isError, ok := call.outcome()
	if !ok {
		return entries
	}

	result := adapterruntime.ToolResultWithID(ts, output, isError, call.ID)
	result.ToolName = call.Name
	return append(entries, result)
}

// outcome extracts the tool result text and whether it failed. Reports ok=false
// when gemini has not recorded a response yet.
func (c *geminiToolCall) outcome() (output string, isError bool, ok bool) {
	var resp map[string]any
	for _, r := range c.Result {
		if r.FunctionResponse != nil {
			resp = r.FunctionResponse.Response
			ok = true
			break
		}
	}
	if !ok {
		return "", false, false
	}

	// any status other than "success" is a failure. Gemini's status enum
	// (read out of the 0.36.0 bundle) includes "error" and the British
	// spelling it actually emits; treating unknown values as failures is the
	// safe direction, because a failed tool recorded as a success misleads
	// every later reader.
	if c.Status != "" && c.Status != "success" {
		isError = true
	}

	if v, present := resp["error"]; present && v != nil {
		return stringifyToolValue(v), true, true
	}
	if v, present := resp["output"]; present {
		return stringifyToolValue(v), isError, true
	}
	if len(resp) == 0 {
		return "", isError, true
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return "", isError, true
	}
	return string(b), isError, true
}

// stringifyToolValue keeps plain strings readable and JSON-encodes the rest,
// so tool_output is never a Go %v rendering of a decoded map.
func stringifyToolValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// geminiMessageText flattens gemini's polymorphic "content": a bare string on
// model messages, an array of {text} parts on user messages.
func geminiMessageText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}

	var parts []geminiContentPart
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Text == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(p.Text)
		}
		return b.String()
	}

	return ""
}

func parseGeminiTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}

// --- types ---

type geminiSession struct {
	SessionID   string          `json:"sessionId"`
	ProjectHash string          `json:"projectHash"`
	StartTime   string          `json:"startTime"`
	LastUpdated string          `json:"lastUpdated"`
	Kind        string          `json:"kind"`
	Messages    []geminiMessage `json:"messages"`
}

type geminiMessage struct {
	ID        string           `json:"id"`
	Timestamp string           `json:"timestamp"`
	Type      string           `json:"type"`
	Content   json.RawMessage  `json:"content"`
	Model     string           `json:"model"`
	ToolCalls []geminiToolCall `json:"toolCalls"`
}

type geminiContentPart struct {
	Text string `json:"text"`
}

type geminiToolCall struct {
	ID        string             `json:"id"`
	Name      string             `json:"name"`
	Args      map[string]any     `json:"args"`
	Result    []geminiToolResult `json:"result"`
	Status    string             `json:"status"`
	Timestamp string             `json:"timestamp"`
}

type geminiToolResult struct {
	FunctionResponse *geminiFunctionResponse `json:"functionResponse"`
}

type geminiFunctionResponse struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}
