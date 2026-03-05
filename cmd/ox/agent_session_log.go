package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/session"
)

// sessionLogOutput is the JSON output format for session log.
type sessionLogOutput struct {
	Success bool `json:"success"`
	Seq     int  `json:"seq"`
}

// sessionLogErrorOutput is the JSON error output for session log.
type sessionLogErrorOutput struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
}

// validLogRoles are the allowed values for --role.
var validLogRoles = map[string]bool{
	"user":      true,
	"assistant": true,
	"system":    true,
	"tool":      true,
}

// sessionLogEntry is the JSONL structure written to the session file.
type sessionLogEntry struct {
	Type      string `json:"type"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp"`
	Seq       int    `json:"seq"`
	ToolName  string `json:"tool_name,omitempty"`
	ToolInput string `json:"tool_input,omitempty"`
}

// runAgentSessionLog appends a single conversation entry to the active session file.
//
// Usage:
//
//	ox agent <id> session log --role user --content "Fix the login bug"
//	ox agent <id> session log --role tool --tool-name bash --tool-input "go test ./..." --content "PASS"
//	echo "Multi-line content" | ox agent <id> session log --role assistant --stdin
func runAgentSessionLog(inst *agentinstance.Instance, args []string) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	// parse flags
	role, content, useStdin, toolName, toolInput, parseErr := parseSessionLogArgs(args)
	if parseErr != nil {
		return parseErr
	}

	// validate role
	if role == "" {
		return fmt.Errorf("--role is required\nValid roles: user, assistant, system, tool")
	}
	if !validLogRoles[role] {
		return fmt.Errorf("invalid role %q\nValid roles: user, assistant, system, tool", role)
	}

	// validate content sources: exactly one of --content or --stdin
	if content != "" && useStdin {
		return fmt.Errorf("cannot use both --content and --stdin")
	}
	if content == "" && !useStdin {
		return fmt.Errorf("one of --content or --stdin is required")
	}

	// validate tool-specific flags
	if role != "tool" && (toolName != "" || toolInput != "") {
		return fmt.Errorf("--tool-name and --tool-input are only valid with --role tool")
	}

	// read content from stdin if requested
	if useStdin {
		content, err = readStdinContent()
		if err != nil {
			return fmt.Errorf("failed to read stdin: %w", err)
		}
		if content == "" {
			return fmt.Errorf("stdin was empty; provide content via pipe or redirect")
		}
	}

	// load recording state
	state, err := session.LoadRecordingState(projectRoot)
	if err != nil {
		return fmt.Errorf("failed to load recording state: %w", err)
	}
	if state == nil {
		return fmt.Errorf("no active session\nRun 'ox agent %s session start' first", inst.AgentID)
	}

	// determine the target file path
	targetFile := sessionLogTargetFile(state)

	// append entry with file locking
	seq, err := appendSessionLogEntry(targetFile, role, content, toolName, toolInput)
	if err != nil {
		return fmt.Errorf("failed to append log entry: %w", err)
	}

	// update entry count in recording state (best-effort)
	if updateErr := session.UpdateRecordingState(projectRoot, func(s *session.RecordingState) {
		s.EntryCount++
		// if SessionFile was empty (generic adapter), persist the file path
		if s.SessionFile == "" {
			s.SessionFile = targetFile
		}
	}); updateErr != nil {
		fmt.Fprintf(os.Stderr, "warning: could not update entry count: %v\n", updateErr)
	}

	// output
	if cfg.Text || cfg.Review {
		fmt.Printf("Logged entry #%d (%s, %d chars)\n", seq, role, len(content))
		if cfg.Review {
			fmt.Println()
			fmt.Println("--- Machine Output ---")
		} else {
			return nil
		}
	}

	output := sessionLogOutput{
		Success: true,
		Seq:     seq,
	}
	jsonOut, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("format log JSON: %w", err)
	}
	fmt.Println(string(jsonOut))
	return nil
}

// sessionLogTargetFile determines which file to append log entries to.
// Uses SessionFile from recording state if set, otherwise creates input.jsonl
// in the session directory (for generic adapters).
func sessionLogTargetFile(state *session.RecordingState) string {
	if state.SessionFile != "" {
		return state.SessionFile
	}
	return filepath.Join(state.SessionPath, "input.jsonl")
}

// appendSessionLogEntry appends a single JSONL entry to the session file.
// Uses file locking to handle concurrent writes safely.
// Returns the seq number of the appended entry.
func appendSessionLogEntry(filePath, role, content, toolName, toolInput string) (int, error) {
	lockPath := filePath + ".lock"
	fl := flock.New(lockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	locked, err := fl.TryLockContext(ctx, 50*time.Millisecond)
	if err != nil {
		return 0, fmt.Errorf("acquire file lock: %w", err)
	}
	if !locked {
		return 0, fmt.Errorf("could not acquire file lock within timeout")
	}
	defer fl.Unlock()

	// read last seq from existing file
	lastSeq := lastSeqFromFile(filePath)
	nextSeq := lastSeq + 1

	// open file for append
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return 0, fmt.Errorf("open session file: %w", err)
	}
	defer f.Close()

	entry := sessionLogEntry{
		Type:      role,
		Content:   content,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Seq:       nextSeq,
		ToolName:  toolName,
		ToolInput: toolInput,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return 0, fmt.Errorf("marshal entry: %w", err)
	}

	// write entry followed by newline
	if _, err := f.Write(append(data, '\n')); err != nil {
		return 0, fmt.Errorf("write entry: %w", err)
	}

	if err := f.Sync(); err != nil {
		return 0, fmt.Errorf("sync session file: %w", err)
	}

	return nextSeq, nil
}

// lastSeqFromFile reads the last line of the file and extracts the seq number.
// Returns -1 if the file is empty or doesn't exist (so next seq starts at 0).
func lastSeqFromFile(filePath string) int {
	f, err := os.Open(filePath)
	if err != nil {
		return -1
	}
	defer f.Close()

	var lastLine string
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024) // 10MB max line
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) != "" {
			lastLine = line
		}
	}

	if lastLine == "" {
		return -1
	}

	var entry struct {
		Seq int `json:"seq"`
	}
	if err := json.Unmarshal([]byte(lastLine), &entry); err != nil {
		return -1
	}
	return entry.Seq
}

// parseSessionLogArgs extracts flags from the raw args slice.
// Returns role, content, useStdin, toolName, toolInput, error.
func parseSessionLogArgs(args []string) (string, string, bool, string, string, error) {
	var role, content, toolName, toolInput string
	var useStdin bool

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--role" && i+1 < len(args):
			i++
			role = args[i]
		case strings.HasPrefix(arg, "--role="):
			role = strings.TrimPrefix(arg, "--role=")

		case arg == "--content" && i+1 < len(args):
			i++
			content = args[i]
		case strings.HasPrefix(arg, "--content="):
			content = strings.TrimPrefix(arg, "--content=")

		case arg == "--stdin":
			useStdin = true

		case arg == "--tool-name" && i+1 < len(args):
			i++
			toolName = args[i]
		case strings.HasPrefix(arg, "--tool-name="):
			toolName = strings.TrimPrefix(arg, "--tool-name=")

		case arg == "--tool-input" && i+1 < len(args):
			i++
			toolInput = args[i]
		case strings.HasPrefix(arg, "--tool-input="):
			toolInput = strings.TrimPrefix(arg, "--tool-input=")

		default:
			// ignore unknown flags gracefully
		}
	}

	return role, content, useStdin, toolName, toolInput, nil
}

// readStdinContent reads all content from stdin.
func readStdinContent() (string, error) {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
