// session.go — session reading, parsing, discovery, and types for codex adapter.
//
// Codex CLI stores sessions as JSONL in ~/.codex/sessions/<session-id>.jsonl.
// Each line is a JSON object with "type" field: "user", "assistant",
// "function_call", "function_call_output". Tool entries have "name",
// "arguments" (JSON string), and "call_id" for correlation. Real sessions
// routinely fire several function_calls before any of their
// function_call_outputs return (parallel/back-to-back tool use), so pairs
// are rarely adjacent in the stream. mergeToolEntries() pairs them by
// call_id regardless of distance.
//
// Format reference: https://github.com/openai/codex
package main

import (
	"bufio"
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

// --- types ---

type codexEntry struct {
	Timestamp string        `json:"timestamp"`
	Type      string        `json:"type"`
	Payload   *codexPayload `json:"payload"`
}

type codexPayload struct {
	ID         string              `json:"id"`
	CWD        string              `json:"cwd"`
	CLIVersion string              `json:"cli_version"`
	Model      string              `json:"model"`
	ItemType   string              `json:"type"`
	Role       string              `json:"role"`
	Content    []codexContentBlock `json:"content"`
	Name       string              `json:"name"`
	Arguments  string              `json:"arguments"`
	CallID     string              `json:"call_id"`
	Output     string              `json:"output"`
	// event_msg fields
	Message string `json:"message,omitempty"`
}

type codexContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type candidate struct {
	path    string
	modTime time.Time
}

// --- session reading ---

func handleRead(p adapterprotocol.ReadParams) (*adapterprotocol.ReadResult, error) {
	entries, err := readCodexFile(p.SessionFile)
	if err != nil {
		return nil, err
	}
	merged := mergeToolEntries(entries, nil)

	// extract metadata
	meta := extractCodexMetadata(p.SessionFile)

	return &adapterprotocol.ReadResult{Entries: merged, Metadata: meta}, nil
}

func handleReadMetadata(p adapterprotocol.ReadParams) (*adapterprotocol.ReadMetadataResult, error) {
	meta := extractCodexMetadata(p.SessionFile)
	if meta == nil {
		return &adapterprotocol.ReadMetadataResult{}, nil
	}
	return &adapterprotocol.ReadMetadataResult{
		AgentVersion: meta.AgentVersion,
		Model:        meta.Model,
	}, nil
}

func readCodexFile(path string) ([]adapterprotocol.RawEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()

	var entries []adapterprotocol.RawEntry
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		parsed, err := parseCodexLine(line)
		if err != nil {
			continue
		}
		entries = append(entries, parsed...)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading session file: %w", err)
	}

	return entries, nil
}

// readCodexFromOffset resumes a Codex transcript at a byte offset using the
// shared JSONL tail reader (pkg/adapterruntime.TailJSONL). The hand-rolled
// version this replaced advanced the offset to the file's current size on
// every call, which acknowledges bytes that were never parsed: Codex writes
// its transcript incrementally, so the final line read mid-write is
// frequently partial, and advancing past it silently drops the rest of that
// turn once Codex finishes writing it. TailJSONL stops at the last complete
// newline instead.
func readCodexFromOffset(path string, offset int64) ([]adapterprotocol.RawEntry, int64, error) {
	return adapterruntime.TailJSONL(path, offset, parseCodexLine)
}

func parseCodexLine(line []byte) ([]adapterprotocol.RawEntry, error) {
	var raw codexEntry
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, err
	}

	switch raw.Type {
	case "response_item":
		return parseResponseItem(raw.Payload, raw.Timestamp)
	case "event_msg":
		return parseEventMsg(raw.Payload, raw.Timestamp)
	}

	return nil, nil
}

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func parseResponseItem(p *codexPayload, ts string) ([]adapterprotocol.RawEntry, error) {
	if p == nil {
		return nil, nil
	}

	switch p.ItemType {
	case "message":
		return parseCodexMessage(p, ts)
	case "function_call":
		if p.Name == "" {
			return nil, nil
		}
		return []adapterprotocol.RawEntry{
			adapterruntime.ToolUseWithID(parseTS(ts), p.Name, p.Arguments, p.CallID),
		}, nil
	case "function_call_output":
		isErr := isCodexToolError(p.Output)
		return []adapterprotocol.RawEntry{
			adapterruntime.ToolResultWithID(parseTS(ts), p.Output, isErr, p.CallID),
		}, nil
	}

	return nil, nil
}

func parseEventMsg(p *codexPayload, _ string) ([]adapterprotocol.RawEntry, error) {
	if p == nil {
		return nil, nil
	}

	// user_message events are skipped — response_item/user already captures the
	// same text with richer context (system instructions, content blocks).
	// event_msg types we could extract in the future: task_started (turn
	// boundaries), token_count (usage telemetry).

	return nil, nil
}

func parseCodexMessage(p *codexPayload, ts string) ([]adapterprotocol.RawEntry, error) {
	t := parseTS(ts)
	switch p.Role {
	case "user":
		text, isSystem := classifyCodexUserContent(p.Content)
		if text == "" {
			return nil, nil
		}
		if isSystem {
			return []adapterprotocol.RawEntry{adapterruntime.SystemEntry(t, text)}, nil
		}
		return []adapterprotocol.RawEntry{adapterruntime.UserEntry(t, text)}, nil

	case "assistant":
		var parts []string
		for _, block := range p.Content {
			if block.Type == "output_text" && block.Text != "" {
				parts = append(parts, block.Text)
			}
		}
		if len(parts) == 0 {
			return nil, nil
		}
		return []adapterprotocol.RawEntry{adapterruntime.AssistantEntry(t, strings.Join(parts, "\n"))}, nil
	}

	return nil, nil
}

func classifyCodexUserContent(blocks []codexContentBlock) (string, bool) {
	var parts []string
	for _, block := range blocks {
		if block.Type == "input_text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	text := strings.Join(parts, "\n")
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "# AGENTS.md instructions") ||
		strings.HasPrefix(trimmed, "<permissions instructions>") ||
		strings.HasPrefix(trimmed, "<environment_context>") {
		return text, true
	}
	return text, false
}

// isCodexToolError reports whether a function_call_output represents a
// failed command. Real exec_command/write_stdin output embeds "Process
// exited with code N" as one line within a multi-line block ("Command:
// ...\nChunk ID: ...\nWall time: ...\nProcess exited with code N\n..."), not
// as a prefix of the whole string. A strict HasPrefix check against the
// entire output therefore never matched a real transcript and silently
// reported every failed command as successful — a real is_error entry
// (case fx_fail1-shaped) surfaced with IsError false.
func isCodexToolError(output string) bool {
	if output == "" {
		return false
	}
	for _, line := range strings.Split(output, "\n") {
		if code, ok := strings.CutPrefix(line, "Process exited with code "); ok {
			return code != "0"
		}
	}
	return false
}

// mergeToolEntries pairs function_call / function_call_output entries by
// call_id regardless of how far apart they land in the parsed stream. Codex
// routinely fires several tool calls before any of their results return, so
// requiring strict adjacency (the previous implementation) missed most pairs
// in real sessions — a call kept tool_name/tool_input with no output, and
// its result surfaced later as an orphan entry with output but no name.
//
// pending carries call entries that have not yet seen a matching result, so
// a call read in one incremental window and its result read in a later
// window (see pendingCallStore in serve.go, which reuses this function for
// the daemon's live tail-watch path) still merge instead of the result
// surfacing as a nameless orphan. Pass nil for one-shot reads (handleRead,
// ImportSession) where the whole file is already in entries and there is no
// later window to carry state into.
func mergeToolEntries(entries []adapterprotocol.RawEntry, pending map[string]adapterprotocol.RawEntry) []adapterprotocol.RawEntry {
	if len(entries) == 0 {
		return entries
	}

	// Index every call and result present in this batch by call_id so pairs
	// merge regardless of distance. Entries without a call_id are excluded
	// from the index — merging on an empty key would pair unrelated entries
	// instead of leaving them alone.
	callIdx := make(map[string]int, len(entries))
	resultIdx := make(map[string]int, len(entries))
	for i, e := range entries {
		if e.Role != adapterprotocol.RoleTool || e.CallID == "" {
			continue
		}
		if e.ToolName != "" {
			if _, exists := callIdx[e.CallID]; !exists {
				callIdx[e.CallID] = i
			}
		} else if _, exists := resultIdx[e.CallID]; !exists {
			resultIdx[e.CallID] = i
		}
	}

	result := make([]adapterprotocol.RawEntry, 0, len(entries))
	for _, e := range entries {
		if e.Role != adapterprotocol.RoleTool || e.CallID == "" {
			result = append(result, e)
			continue
		}

		if e.ToolName != "" {
			// tool call: absorb its result if one landed in this same batch.
			if ri, ok := resultIdx[e.CallID]; ok {
				r := entries[ri]
				e.ToolOutput = r.ToolOutput
				e.IsError = r.IsError
				if pending != nil {
					delete(pending, e.CallID)
				}
			} else if pending != nil {
				// no result yet in this batch — remember the call so a
				// later batch can label its result when it arrives.
				pending[e.CallID] = e
			}
			result = append(result, e)
			continue
		}

		// tool result: ToolName == "" here (function_call_output never sets it).
		if _, ok := callIdx[e.CallID]; ok {
			// its call is in this same batch and already carries the
			// output from the block above — drop the now-redundant
			// standalone result.
			continue
		}
		if pending != nil {
			if call, ok := pending[e.CallID]; ok {
				// call arrived in an earlier batch — label the result from
				// it instead of letting it surface nameless.
				e.ToolName = call.ToolName
				e.ToolInput = call.ToolInput
				delete(pending, e.CallID)
			}
		}
		result = append(result, e)
	}
	return result
}

// --- session discovery ---

func findCodexSession(repoRoot, agentID, since, agentSessionID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}

	sessionsDir := filepath.Join(home, ".codex", "sessions")

	// direct lookup: scan recent date dirs for a file whose session_meta.id matches
	if agentSessionID != "" {
		if err := adapterruntime.ValidateSessionID(agentSessionID); err != nil {
			return "", err
		}
		if path, err := findCodexBySessionID(sessionsDir, agentSessionID); err == nil {
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

	candidates := collectCodexCandidates(sessionsDir, sinceTime)
	if len(candidates) == 0 {
		return "", fmt.Errorf("no codex sessions found")
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].modTime.After(candidates[j].modTime)
	})

	// filter by cwd (repoRoot)
	cwdMatches := filterCodexByCWD(candidates, repoRoot)
	if len(cwdMatches) == 0 {
		return "", fmt.Errorf("no codex sessions found for %s", repoRoot)
	}

	if agentID != "" {
		for _, c := range cwdMatches {
			if codexSessionContainsAgentID(c.path, agentID) {
				return c.path, nil
			}
		}
	}

	return cwdMatches[0].path, nil
}

// findCodexBySessionID scans recent date directories for a JSONL file whose
// session_meta entry has a matching id field.
func findCodexBySessionID(sessionsDir, sessionID string) (string, error) {
	now := time.Now()
	for day := 0; day < searchDays; day++ {
		t := now.AddDate(0, 0, -day)
		dateDir := filepath.Join(sessionsDir, t.Format("2006"), t.Format("01"), t.Format("02"))
		entries, err := os.ReadDir(dateDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
				continue
			}
			path := filepath.Join(dateDir, entry.Name())
			if codexFileHasSessionID(path, sessionID) {
				return path, nil
			}
		}
	}
	return "", fmt.Errorf("codex session %s not found", sessionID)
}

// codexFileHasSessionID checks if the first session_meta entry in a file
// has the given session ID.
func codexFileHasSessionID(path, sessionID string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		var entry codexEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry.Type == "session_meta" && entry.Payload != nil {
			return entry.Payload.ID == sessionID
		}
		// session_meta is always the first entry; stop after first line
		break
	}
	return false
}

func collectCodexCandidates(sessionsDir string, since time.Time) []candidate {
	var candidates []candidate
	now := time.Now()

	for day := 0; day < searchDays; day++ {
		t := now.AddDate(0, 0, -day)
		dateDir := filepath.Join(sessionsDir, t.Format("2006"), t.Format("01"), t.Format("02"))
		entries, err := os.ReadDir(dateDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			if !since.IsZero() && !info.ModTime().After(since) {
				continue
			}
			candidates = append(candidates, candidate{
				path:    filepath.Join(dateDir, entry.Name()),
				modTime: info.ModTime(),
			})
		}
	}

	return candidates
}

func filterCodexByCWD(candidates []candidate, cwd string) []candidate {
	var matches []candidate
	for _, c := range candidates {
		if codexSessionCWD(c.path) == cwd {
			matches = append(matches, c)
		}
	}
	return matches
}

func codexSessionCWD(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		var entry codexEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry.Type == "session_meta" && entry.Payload != nil {
			return entry.Payload.CWD
		}
		break
	}
	return ""
}

func codexSessionContainsAgentID(path, agentID string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	for scanner.Scan() {
		if strings.Contains(scanner.Text(), agentID) {
			return true
		}
	}
	return false
}

func extractCodexMetadata(path string) *adapterprotocol.SessionMetadata {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	meta := &adapterprotocol.SessionMetadata{}
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	for scanner.Scan() {
		var entry codexEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry.Payload == nil {
			continue
		}
		switch entry.Type {
		case "session_meta":
			if meta.AgentVersion == "" {
				meta.AgentVersion = entry.Payload.CLIVersion
			}
		case "turn_context":
			if meta.Model == "" {
				meta.Model = entry.Payload.Model
			}
		}
		if meta.AgentVersion != "" && meta.Model != "" {
			break
		}
	}

	if meta.AgentVersion == "" && meta.Model == "" {
		return nil
	}
	return meta
}
