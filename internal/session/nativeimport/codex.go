package nativeimport

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// CodexHome is $CODEX_HOME, or ~/.codex.
func CodexHome() (string, error) {
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		return filepath.Abs(dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex"), nil
}

// DiscoverCodex lists rollout files under sessions/ and archived_sessions/.
// There is no calendar window: a resumed session keeps the directory of the
// day it started. Symlinks and anything that is not a regular file are skipped.
func DiscoverCodex(home string) ([]string, error) {
	var paths []string
	for _, dir := range []string{"sessions", "archived_sessions"} {
		err := filepath.WalkDir(filepath.Join(home, dir), func(p string, d os.DirEntry, err error) error {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if d.Type().IsRegular() && strings.HasPrefix(d.Name(), "rollout-") && strings.HasSuffix(d.Name(), ".jsonl") {
				paths = append(paths, p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(paths)
	return paths, nil
}

var rolloutID = regexp.MustCompile(`-([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.jsonl$`)

type codexRecord struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

type codexPayload struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	CWD       string          `json:"cwd"`
	Source    json.RawMessage `json:"source"`
	Role      string          `json:"role"`
	CallID    string          `json:"call_id"`
	Arguments string          `json:"arguments"`
	Input     string          `json:"input"`
	Content   json.RawMessage `json:"content"`
	Output    json.RawMessage `json:"output"`
	Item      *codexItem      `json:"item"`
	Git       *struct {
		Branch string `json:"branch"`
	} `json:"git"`
}

type codexItem struct {
	Type    string   `json:"type"`
	Command []string `json:"command"`
}

// InspectCodex reads one Codex rollout structurally. The first record must be
// the session_meta header, and its id must equal the UUID in the filename.
func InspectCodex(path string) (Session, error) {
	match := rolloutID.FindStringSubmatch(filepath.Base(path))
	if match == nil {
		return Session{}, fmt.Errorf("rollout name has no session ID")
	}
	s := Session{Agent: AgentCodex, NativeID: match[1], Path: path}
	cwds := cwdSet{}
	primeCalls := map[string]bool{}
	userMessages, eventMessages := 0, 0
	assistantMessages, eventReplies := 0, 0
	info, err := readRecords(path, func(lineNo int, line []byte) error {
		var rec codexRecord
		if json.Unmarshal(line, &rec) != nil {
			return fmt.Errorf("invalid JSON at line %d", lineNo)
		}
		var p codexPayload
		_ = json.Unmarshal(rec.Payload, &p)
		if lineNo == 1 {
			if rec.Type != "session_meta" {
				return fmt.Errorf("rollout lacks a session_meta header")
			}
			if p.ID != s.NativeID {
				return fmt.Errorf("session_meta id does not match the rollout name")
			}
			internal, err := codexInternalSource(p.Source)
			if err != nil {
				return err
			}
			s.Internal = internal
			if p.Git != nil {
				s.Branch = p.Git.Branch
			}
		}
		s.observe(parseTimestamp(rec.Timestamp))
		switch rec.Type {
		case "session_meta", "turn_context":
			cwds.add(p.CWD)
		case "event_msg":
			switch p.Type {
			case "task_started":
				s.InFlight = true
			case "task_complete", "task_completed", "turn_aborted":
				s.InFlight = false
			case "user_message":
				eventMessages++
			case "agent_message":
				eventReplies++
			case "item_completed":
				if p.Item != nil && p.Item.Type == "CommandExecution" && strings.Contains(strings.Join(p.Item.Command, " "), "ox agent prime") {
					var payload any
					if json.Unmarshal(rec.Payload, &payload) == nil {
						s.Markers = append(s.Markers, markersIn(payload)...)
					}
				}
			}
		case "response_item":
			s.inspectCodexItem(rec, p, primeCalls, &userMessages, &assistantMessages)
		}
		return nil
	})
	if err != nil {
		return Session{}, err
	}
	s.Size, s.ModTime, s.CWDs = info.Size(), info.ModTime(), cwds.sorted()
	// Newer rollouts record conversation as response items; older ones only as
	// events. Count one representation, never both.
	s.Prompts, s.Replies = userMessages, assistantMessages
	if s.Prompts == 0 && s.Replies == 0 {
		s.Prompts, s.Replies = eventMessages, eventReplies
	}
	if s.StartedAt.IsZero() {
		return Session{}, fmt.Errorf("rollout has no timestamps")
	}
	return s, nil
}

func (s *Session) inspectCodexItem(rec codexRecord, p codexPayload, primeCalls map[string]bool, users, assistants *int) {
	switch p.Type {
	case "message":
		switch p.Role {
		case "user":
			if isCodexUserPrompt(p.Content) {
				*users++
			}
			s.markersFromPrime(rec.Payload)
		case "assistant":
			*assistants++
		case "developer", "system":
			s.markersFromPrime(rec.Payload)
		}
	case "function_call", "custom_tool_call":
		if strings.Contains(p.Arguments+p.Input, "ox agent prime") {
			primeCalls[p.CallID] = true
		}
	case "function_call_output", "custom_tool_call_output":
		if primeCalls[p.CallID] {
			var payload any
			if json.Unmarshal(rec.Payload, &payload) == nil {
				s.Markers = append(s.Markers, markersIn(payload)...)
			}
		}
	}
}

// markersFromPrime keeps a marker from an injected message only when the
// message carries ox's whole prime output, not a quote of one tag.
func (s *Session) markersFromPrime(raw json.RawMessage) {
	if !strings.Contains(string(raw), "<ox-prime>") {
		return
	}
	var payload any
	if json.Unmarshal(raw, &payload) == nil {
		s.Markers = append(s.Markers, markersIn(payload)...)
	}
}

// isCodexUserPrompt skips the user-role messages Codex injects for its own
// context (environment, AGENTS.md instructions), which start with a tag.
func isCodexUserPrompt(content json.RawMessage) bool {
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &parts) != nil {
		return false
	}
	for _, part := range parts {
		text := strings.TrimSpace(part.Text)
		if text != "" && !strings.HasPrefix(text, "<") && !strings.HasPrefix(text, "# AGENTS.md") {
			return true
		}
	}
	return false
}

// codexInternalSource classifies session_meta.source. Codex's SessionSource is
// a tagged enum; an unknown variant cannot establish that the thread is user
// history, so it is refused rather than guessed.
func codexInternalSource(raw json.RawMessage) (bool, error) {
	if len(raw) == 0 {
		return false, nil // rollouts before the field existed are user sessions
	}
	var name string
	if json.Unmarshal(raw, &name) == nil {
		switch name {
		case "cli", "vscode", "exec", "mcp", "app", "":
			return false, nil
		}
		return false, fmt.Errorf("unrecognized Codex session source %q", name)
	}
	var tagged map[string]json.RawMessage
	if json.Unmarshal(raw, &tagged) == nil && len(tagged) == 1 {
		for tag := range tagged {
			switch tag {
			case "custom":
				return false, nil
			case "subagent", "internal":
				return true, nil
			}
		}
	}
	return false, fmt.Errorf("unrecognized Codex session source")
}
