// session.go handles Gemini CLI session reading, parsing, discovery, and types.
//
// Gemini CLI stores sessions as JSON (not JSONL) in ~/.gemini/sessions/<id>.json.
// The entire file is a single JSON array that gets rewritten each turn. Each
// element has "role" ("user" or "model") and "parts" (array of {text} or
// {functionCall}/{functionResponse}). Because the file is rewritten, the offset
// model uses entry count instead of byte offset.
//
// Format reference: https://github.com/google-gemini/gemini-cli
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

	var session geminiSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("failed to parse session: %w", err)
	}

	return &adapterprotocol.ReadMetadataResult{
		AgentVersion: session.Metadata.AgentVersion,
		Model:        session.Metadata.Model,
	}, nil
}

func findGeminiSession(repoRoot, agentID, since, agentSessionID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}

	tmpDir := filepath.Join(home, ".gemini", "tmp")

	// direct lookup: Gemini files are session-{id}.json under project chats dirs
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

	var candidates []struct {
		path    string
		modTime time.Time
	}

	projectDirs, err := os.ReadDir(tmpDir)
	if err != nil {
		return "", fmt.Errorf("failed to read gemini tmp dir: %w", err)
	}

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
			if !sinceTime.IsZero() && !info.ModTime().After(sinceTime) {
				continue
			}
			candidates = append(candidates, struct {
				path    string
				modTime time.Time
			}{path: filepath.Join(chatsDir, f.Name()), modTime: info.ModTime()})
		}
	}

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

// findGeminiBySessionID searches for a session file named session-{id}.json
// across all project chat directories.
func findGeminiBySessionID(tmpDir, sessionID string) (string, error) {
	targetName := "session-" + sessionID + ".json"

	projectDirs, err := os.ReadDir(tmpDir)
	if err != nil {
		return "", err
	}

	for _, projEntry := range projectDirs {
		if !projEntry.IsDir() {
			continue
		}
		candidate := filepath.Join(tmpDir, projEntry.Name(), "chats", targetName)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("gemini session %s not found", sessionID)
}

func parseGeminiSession(data []byte) ([]adapterprotocol.RawEntry, *adapterprotocol.SessionMetadata, error) {
	var session geminiSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, nil, fmt.Errorf("failed to parse gemini session: %w", err)
	}

	meta := &adapterprotocol.SessionMetadata{
		Model:        session.Metadata.Model,
		AgentVersion: session.Metadata.AgentVersion,
	}

	var entries []adapterprotocol.RawEntry
	for _, msg := range session.Messages {
		parsed := parseGeminiMessage(&msg)
		entries = append(entries, parsed...)
	}

	return entries, meta, nil
}

func parseGeminiMessage(msg *geminiMessage) []adapterprotocol.RawEntry {
	var entries []adapterprotocol.RawEntry

	// gemini has no per-entry timestamps
	var zero time.Time

	// map gemini roles to entry constructors
	textEntry := adapterruntime.UserEntry
	switch msg.Role {
	case "model", "assistant":
		textEntry = adapterruntime.AssistantEntry
	case "system":
		textEntry = adapterruntime.SystemEntry
	}

	for _, part := range msg.Parts {
		if part.Text != "" {
			entries = append(entries, textEntry(zero, part.Text))
		}
		if part.FunctionCall != nil {
			inputJSON, _ := json.Marshal(part.FunctionCall.Args)
			entries = append(entries, adapterruntime.ToolUseEntry(zero, part.FunctionCall.Name, string(inputJSON)))
		}
		if part.FunctionResponse != nil {
			result := adapterruntime.ToolResultEntry(zero, "", false)
			result.ToolName = part.FunctionResponse.Name
			if part.FunctionResponse.Response != nil {
				if errMsg, ok := part.FunctionResponse.Response["error"]; ok {
					result.ToolOutput = fmt.Sprintf("%v", errMsg)
					result.IsError = true
				} else {
					outputJSON, _ := json.Marshal(part.FunctionResponse.Response)
					result.ToolOutput = string(outputJSON)
				}
			}
			entries = append(entries, result)
		}
	}

	return entries
}

// --- types ---

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
