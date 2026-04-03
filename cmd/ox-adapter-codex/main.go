// ox-adapter-codex is the external adapter binary for OpenAI Codex CLI sessions.
//
// Codex stores sessions as JSONL in ~/.codex/sessions/YYYY/MM/DD/.
// Entry types: session_meta, turn_context, response_item, event_msg.
// Only response_item carries conversation data.
package main

import (
	"bufio"
	"context"
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

const (
	adapterName    = "codex"
	adapterDisplay = "OpenAI Codex CLI"
	adapterVersion = "0.1.0"
	searchDays     = 14
)

func main() {
	adapterruntime.Run(adapterruntime.Config{
		Info:         handleInfo,
		Detect:       handleDetect,
		Read:         handleRead,
		ReadMetadata: handleReadMetadata,
		Diagnose:     handleDiagnose,
		Serve:        handleServe,
	})
}

func handleInfo() (*adapterprotocol.InfoResponse, error) {
	return &adapterprotocol.InfoResponse{
		ProtocolVersion: adapterprotocol.ProtocolVersion,
		Name:            adapterName,
		DisplayName:     adapterDisplay,
		Version:         adapterVersion,
		Type:            adapterprotocol.TypeSession,
		Capabilities: []string{
			adapterprotocol.CapSessionReader,
			adapterprotocol.CapIncrementalReader,
			adapterprotocol.CapServeMode,
		},
		HookEnvValues: []string{"codex"},
		ServeMode:     true,
	}, nil
}

func handleDetect() (*adapterprotocol.DetectResponse, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return &adapterprotocol.DetectResponse{Detected: false, Reason: "cannot determine home directory"}, nil
	}

	sessionsDir := filepath.Join(home, ".codex", "sessions")
	if info, err := os.Stat(sessionsDir); err == nil && info.IsDir() {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "found ~/.codex/sessions/"}, nil
	}

	return &adapterprotocol.DetectResponse{Detected: false, Reason: "~/.codex/sessions/ not found"}, nil
}

func handleRead(p adapterprotocol.ReadParams) (*adapterprotocol.ReadResult, error) {
	entries, err := readCodexFile(p.SessionFile)
	if err != nil {
		return nil, err
	}
	merged := mergeToolEntries(entries)

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

func handleDiagnose(p adapterprotocol.DiagnoseParams) (*adapterprotocol.DiagnoseResult, error) {
	var issues []adapterprotocol.DiagnoseIssue

	home, _ := os.UserHomeDir()
	codexDir := filepath.Join(home, ".codex")
	if _, err := os.Stat(codexDir); os.IsNotExist(err) {
		issues = append(issues, adapterprotocol.DiagnoseIssue{
			Slug:     "codex:not-installed",
			Severity: "warning",
			Title:    "Codex CLI not detected",
			Detail:   "~/.codex directory not found.",
		})
	}

	return &adapterprotocol.DiagnoseResult{OK: len(issues) == 0, Issues: issues}, nil
}

type codexSessionState struct {
	sessionFile string
	offset      int64
}

func handleServe(srv *adapterruntime.Server) {
	store := adapterruntime.NewSessionStore[*codexSessionState]()

	srv.OnFindSession(func(ctx context.Context, p adapterprotocol.FindSessionParams) (*adapterprotocol.FindSessionResult, error) {
		sessionFile, err := findCodexSession(p.RepoRoot, p.AgentID, p.Since, p.AgentSessionID)
		if err != nil {
			return nil, err
		}

		var offset int64
		if info, err := os.Stat(sessionFile); err == nil {
			offset = info.Size()
		}

		store.Set(p.AgentID, &codexSessionState{sessionFile: sessionFile, offset: offset})

		return &adapterprotocol.FindSessionResult{SessionFile: sessionFile, Offset: offset}, nil
	})

	srv.OnReadFromOffset(func(ctx context.Context, p adapterprotocol.ReadFromOffsetParams) (*adapterprotocol.ReadFromOffsetResult, error) {
		state, ok := store.Get(p.AgentID)
		if !ok {
			return nil, adapterruntime.ErrSessionNotFound
		}

		entries, newOffset, err := readCodexFromOffset(state.sessionFile, p.Offset)
		if err != nil {
			return nil, err
		}

		merged := mergeToolEntries(entries)
		state.offset = newOffset

		return &adapterprotocol.ReadFromOffsetResult{Entries: merged, NewOffset: newOffset}, nil
	})

	srv.OnEndSession(func(ctx context.Context, p adapterprotocol.EndSessionParams) error {
		store.Delete(p.AgentID)
		return nil
	})

	srv.Serve()
}

// --- session reading ---

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

func readCodexFromOffset(path string, offset int64) ([]adapterprotocol.RawEntry, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()

	if offset > 0 {
		if _, err := f.Seek(offset, 0); err != nil {
			return nil, offset, fmt.Errorf("failed to seek: %w", err)
		}
	}

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

	newOffset := offset
	if info, err := f.Stat(); err == nil {
		newOffset = info.Size()
	}

	return entries, newOffset, nil
}

func parseCodexLine(line []byte) ([]adapterprotocol.RawEntry, error) {
	var raw codexEntry
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, err
	}

	if raw.Type != "response_item" || raw.Payload == nil {
		return nil, nil
	}

	p := raw.Payload
	ts := raw.Timestamp

	switch p.ItemType {
	case "message":
		return parseCodexMessage(p, ts)
	case "function_call":
		if p.Name == "" {
			return nil, nil
		}
		return []adapterprotocol.RawEntry{{
			Timestamp: ts,
			Role:      "tool",
			ToolName:  p.Name,
			ToolInput: p.Arguments,
			CallID:    p.CallID,
		}}, nil
	case "function_call_output":
		if !isCodexToolError(p.Output) {
			return nil, nil
		}
		return []adapterprotocol.RawEntry{{
			Timestamp:  ts,
			Role:       "tool",
			ToolOutput: p.Output,
			IsError:    true,
			CallID:     p.CallID,
		}}, nil
	}

	return nil, nil
}

func parseCodexMessage(p *codexPayload, ts string) ([]adapterprotocol.RawEntry, error) {
	switch p.Role {
	case "user":
		text, isSystem := classifyCodexUserContent(p.Content)
		if text == "" {
			return nil, nil
		}
		role := "user"
		if isSystem {
			role = "system"
		}
		return []adapterprotocol.RawEntry{{Timestamp: ts, Role: role, Content: text}}, nil

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
		return []adapterprotocol.RawEntry{{
			Timestamp: ts,
			Role:      "assistant",
			Content:   strings.Join(parts, "\n"),
		}}, nil
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

func isCodexToolError(output string) bool {
	if output == "" {
		return false
	}
	if strings.HasPrefix(output, "Process exited with code ") {
		return output != "Process exited with code 0"
	}
	return false
}

func mergeToolEntries(entries []adapterprotocol.RawEntry) []adapterprotocol.RawEntry {
	if len(entries) < 2 {
		return entries
	}
	result := make([]adapterprotocol.RawEntry, 0, len(entries))
	i := 0
	for i < len(entries) {
		e := entries[i]
		if e.Role == "tool" && e.ToolName != "" && e.CallID != "" && i+1 < len(entries) {
			next := entries[i+1]
			if next.Role == "tool" && next.ToolOutput != "" && next.CallID == e.CallID {
				e.ToolOutput = next.ToolOutput
				e.IsError = next.IsError
				result = append(result, e)
				i += 2
				continue
			}
		}
		result = append(result, e)
		i++
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

type candidate struct {
	path    string
	modTime time.Time
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
}

type codexContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
