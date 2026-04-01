package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// GeminiAdapter reads Gemini CLI session files stored as monolithic JSON
// in ~/.gemini/tmp/<project_hash>/chats/session-*.json.
//
// Unlike JSONL-based adapters (Claude Code, Codex), Gemini rewrites the
// entire session file on each turn. This adapter re-reads the JSON on
// each change and emits only new entries (delta-based).
type GeminiAdapter struct{}

func init() {
	Register(&GeminiAdapter{})
}

func (a *GeminiAdapter) Name() string { return "gemini" }

func (a *GeminiAdapter) geminiDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".gemini")
}

// Detect checks if Gemini CLI session files are present.
func (a *GeminiAdapter) Detect() bool {
	if os.Getenv("AGENT_ENV") == "gemini" {
		return true
	}
	if os.Getenv("GEMINI_API_KEY") != "" || os.Getenv("GOOGLE_API_KEY") != "" {
		return true
	}

	geminiDir := a.geminiDir()
	if geminiDir == "" {
		return false
	}

	tmpDir := filepath.Join(geminiDir, "tmp")
	if _, err := os.Stat(tmpDir); os.IsNotExist(err) {
		return false
	}

	entries, err := os.ReadDir(tmpDir)
	return err == nil && len(entries) > 0
}

// FindSessionFile locates the most recent Gemini session file.
// Gemini stores sessions in ~/.gemini/tmp/<project_hash>/chats/session-*.json.
// The project hash algorithm is not publicly documented, so we scan all
// subdirectories and return the most recently modified session file.
func (a *GeminiAdapter) FindSessionFile(agentID string, since time.Time) (string, error) {
	geminiDir := a.geminiDir()
	if geminiDir == "" {
		return "", ErrSessionNotFound
	}
	tmpDir := filepath.Join(geminiDir, "tmp")

	var candidates []sessionCandidate

	// scan all project directories under tmp/
	projectDirs, err := os.ReadDir(tmpDir)
	if err != nil {
		return "", fmt.Errorf("%w: failed to read gemini tmp dir", ErrSessionNotFound)
	}

	for _, projEntry := range projectDirs {
		if !projEntry.IsDir() {
			continue
		}
		chatsDir := filepath.Join(tmpDir, projEntry.Name(), "chats")
		if _, err := os.Stat(chatsDir); os.IsNotExist(err) {
			continue
		}

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
			if info.ModTime().After(since) {
				candidates = append(candidates, sessionCandidate{
					path:    filepath.Join(chatsDir, f.Name()),
					modTime: info.ModTime(),
				})
			}
		}
	}

	if len(candidates) == 0 {
		return "", fmt.Errorf("%w: no gemini sessions modified after %v", ErrSessionNotFound, since)
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].modTime.After(candidates[j].modTime)
	})

	// if agentID provided, search content
	if agentID != "" {
		for _, c := range candidates {
			data, err := os.ReadFile(c.path)
			if err != nil {
				continue
			}
			if strings.Contains(string(data), agentID) {
				return c.path, nil
			}
		}
	}

	return candidates[0].path, nil
}

// ReadMetadata extracts session metadata from a Gemini session file.
func (a *GeminiAdapter) ReadMetadata(sessionPath string) (*SessionMetadata, error) {
	data, err := os.ReadFile(sessionPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}

	var session geminiSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("failed to parse session file: %w", err)
	}

	meta := &SessionMetadata{}
	if session.Metadata.Model != "" {
		meta.Model = session.Metadata.Model
	}
	if session.Metadata.AgentVersion != "" {
		meta.AgentVersion = session.Metadata.AgentVersion
	}

	if meta.AgentVersion == "" && meta.Model == "" {
		return nil, nil
	}
	return meta, nil
}

// Read parses all entries from a Gemini session file.
func (a *GeminiAdapter) Read(sessionPath string) ([]RawEntry, error) {
	data, err := os.ReadFile(sessionPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open session file: %w", err)
	}

	return a.parseSession(data)
}

// ReadFromOffset reads new entries since the given offset.
// For Gemini, offset represents entry count (not byte position) since
// the entire JSON file is rewritten on each turn.
func (a *GeminiAdapter) ReadFromOffset(path string, offset int64) ([]RawEntry, int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, offset, fmt.Errorf("failed to read session file: %w", err)
	}

	allEntries, err := a.parseSession(data)
	if err != nil {
		return nil, offset, err
	}

	total := int64(len(allEntries))
	if offset >= total {
		return nil, total, nil
	}

	return allEntries[offset:], total, nil
}

// Watch monitors a Gemini session file for new entries using fsnotify.
// Since Gemini rewrites the entire file on each turn, we re-read the
// full JSON and emit only entries beyond our last-seen count.
// Watches the parent directory to survive atomic file replacements (temp + rename).
func (a *GeminiAdapter) Watch(ctx context.Context, sessionPath string) (<-chan RawEntry, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	// watch parent directory to survive atomic file replacements
	parentDir := filepath.Dir(sessionPath)
	targetName := filepath.Base(sessionPath)
	if err := watcher.Add(parentDir); err != nil {
		watcher.Close()
		return nil, err
	}

	ch := make(chan RawEntry, 100)

	go func() {
		defer close(ch)
		defer watcher.Close()

		// initial read to establish baseline
		var lastCount int64
		if data, err := os.ReadFile(sessionPath); err == nil {
			if entries, err := a.parseSession(data); err == nil {
				lastCount = int64(len(entries))
			}
		}

		debounceTimer := time.NewTimer(0)
		if !debounceTimer.Stop() {
			<-debounceTimer.C
		}
		pendingRead := false

		for {
			select {
			case <-ctx.Done():
				return

			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				// filter to our target file only
				if filepath.Base(event.Name) != targetName {
					continue
				}
				if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) != 0 {
					if !pendingRead {
						debounceTimer.Reset(debounceDelay)
						pendingRead = true
					}
				}

			case <-debounceTimer.C:
				pendingRead = false
				data, err := os.ReadFile(sessionPath)
				if err != nil {
					continue
				}
				entries, err := a.parseSession(data)
				if err != nil {
					continue
				}
				newCount := int64(len(entries))
				if newCount > lastCount {
					for _, e := range entries[lastCount:] {
						select {
						case ch <- e:
						case <-ctx.Done():
							return
						}
					}
					lastCount = newCount
				}

			case _, ok := <-watcher.Errors:
				if !ok {
					return
				}
			}
		}
	}()

	return ch, nil
}

// parseSession converts a Gemini session JSON into RawEntries.
func (a *GeminiAdapter) parseSession(data []byte) ([]RawEntry, error) {
	var session geminiSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("failed to parse gemini session: %w", err)
	}

	var entries []RawEntry
	for _, msg := range session.Messages {
		parsed := a.parseMessage(&msg)
		entries = append(entries, parsed...)
	}
	return entries, nil
}

// parseMessage converts a Gemini message into RawEntries.
// A single message can produce multiple entries (text + function calls).
func (a *GeminiAdapter) parseMessage(msg *geminiMessage) []RawEntry {
	var entries []RawEntry

	role := msg.Role
	switch role {
	case "model":
		role = "assistant"
	case "tool":
		// tool responses from Gemini are function responses
	}

	for _, part := range msg.Parts {
		if part.Text != "" {
			entries = append(entries, RawEntry{
				Role:    role,
				Content: part.Text,
			})
		}
		if part.FunctionCall != nil {
			inputJSON, _ := json.Marshal(part.FunctionCall.Args)
			entries = append(entries, RawEntry{
				Role:      "tool",
				ToolName:  part.FunctionCall.Name,
				ToolInput: string(inputJSON),
			})
		}
		if part.FunctionResponse != nil {
			entry := RawEntry{
				Role:     "tool",
				ToolName: part.FunctionResponse.Name,
			}
			if part.FunctionResponse.Response != nil {
				if errMsg, ok := part.FunctionResponse.Response["error"]; ok {
					entry.ToolOutput = fmt.Sprintf("%v", errMsg)
					entry.IsError = true
				} else {
					outputJSON, _ := json.Marshal(part.FunctionResponse.Response)
					entry.ToolOutput = string(outputJSON)
				}
			}
			entries = append(entries, entry)
		}
	}

	return entries
}

// Gemini session JSON structures.

type geminiSession struct {
	Messages []geminiMessage `json:"messages"`
	Metadata geminiMetadata  `json:"metadata"`
}

type geminiMetadata struct {
	Model        string `json:"model"`
	AgentVersion string `json:"agentVersion"`
}

type geminiMessage struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}

type geminiFunctionCall struct {
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args"`
}

type geminiFunctionResponse struct {
	Name     string                 `json:"name"`
	Response map[string]interface{} `json:"response"`
}
