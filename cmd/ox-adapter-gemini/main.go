// ox-adapter-gemini is the external adapter binary for Gemini CLI sessions.
//
// Gemini writes monolithic JSON session files (not JSONL). The entire file
// is rewritten on each turn. This adapter re-reads the JSON and uses entry
// count as the offset (not byte position).
package main

import (
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
	adapterName    = "gemini"
	adapterDisplay = "Gemini CLI"
	adapterVersion = "0.1.0"
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
		HookEnvValues: []string{"gemini"},
		ServeMode:     true,
	}, nil
}

func handleDetect() (*adapterprotocol.DetectResponse, error) {
	if os.Getenv("AGENT_ENV") == "gemini" {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "AGENT_ENV=gemini"}, nil
	}
	if os.Getenv("GEMINI_API_KEY") != "" || os.Getenv("GOOGLE_API_KEY") != "" {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "Gemini API key found"}, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return &adapterprotocol.DetectResponse{Detected: false, Reason: "cannot determine home directory"}, nil
	}

	tmpDir := filepath.Join(home, ".gemini", "tmp")
	if _, err := os.Stat(tmpDir); os.IsNotExist(err) {
		return &adapterprotocol.DetectResponse{Detected: false, Reason: "~/.gemini/tmp not found"}, nil
	}

	entries, err := os.ReadDir(tmpDir)
	if err != nil || len(entries) == 0 {
		return &adapterprotocol.DetectResponse{Detected: false, Reason: "~/.gemini/tmp is empty"}, nil
	}

	return &adapterprotocol.DetectResponse{Detected: true, Reason: "found ~/.gemini/tmp with session data"}, nil
}

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

func handleDiagnose(p adapterprotocol.DiagnoseParams) (*adapterprotocol.DiagnoseResult, error) {
	var issues []adapterprotocol.DiagnoseIssue

	home, _ := os.UserHomeDir()
	geminiDir := filepath.Join(home, ".gemini")
	if _, err := os.Stat(geminiDir); os.IsNotExist(err) {
		issues = append(issues, adapterprotocol.DiagnoseIssue{
			Slug:     "gemini:not-installed",
			Severity: "warning",
			Title:    "Gemini CLI not detected",
			Detail:   "~/.gemini directory not found.",
		})
	}

	return &adapterprotocol.DiagnoseResult{OK: len(issues) == 0, Issues: issues}, nil
}

type geminiSessionState struct {
	sessionFile string
	entryCount  int64
}

func handleServe(srv *adapterruntime.Server) {
	store := adapterruntime.NewSessionStore[*geminiSessionState]()

	srv.OnFindSession(func(ctx context.Context, p adapterprotocol.FindSessionParams) (*adapterprotocol.FindSessionResult, error) {
		sessionFile, err := findGeminiSession(p.RepoRoot, p.AgentID, p.Since, p.AgentSessionID)
		if err != nil {
			return nil, err
		}

		// determine initial entry count
		var offset int64
		if data, err := os.ReadFile(sessionFile); err == nil {
			if entries, _, err := parseGeminiSession(data); err == nil {
				offset = int64(len(entries))
			}
		}

		store.Set(p.AgentID, &geminiSessionState{sessionFile: sessionFile, entryCount: offset})

		return &adapterprotocol.FindSessionResult{SessionFile: sessionFile, Offset: offset}, nil
	})

	srv.OnReadFromOffset(func(ctx context.Context, p adapterprotocol.ReadFromOffsetParams) (*adapterprotocol.ReadFromOffsetResult, error) {
		state, ok := store.Get(p.AgentID)
		if !ok {
			return nil, adapterruntime.ErrSessionNotFound
		}

		data, err := os.ReadFile(state.sessionFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read session file: %w", err)
		}

		allEntries, _, err := parseGeminiSession(data)
		if err != nil {
			return nil, err
		}

		total := int64(len(allEntries))
		if p.Offset >= total {
			return &adapterprotocol.ReadFromOffsetResult{Entries: nil, NewOffset: total}, nil
		}

		newEntries := allEntries[p.Offset:]
		state.entryCount = total

		return &adapterprotocol.ReadFromOffsetResult{Entries: newEntries, NewOffset: total}, nil
	})

	srv.OnEndSession(func(ctx context.Context, p adapterprotocol.EndSessionParams) error {
		store.Delete(p.AgentID)
		return nil
	})

	srv.Serve()
}

func findGeminiSession(repoRoot, agentID, since, agentSessionID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}

	tmpDir := filepath.Join(home, ".gemini", "tmp")

	// direct lookup: Gemini files are session-{id}.json under project chats dirs
	if agentSessionID != "" {
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

	role := msg.Role
	if role == "model" {
		role = "assistant"
	}

	for _, part := range msg.Parts {
		if part.Text != "" {
			entries = append(entries, adapterprotocol.RawEntry{
				Role:    role,
				Content: part.Text,
			})
		}
		if part.FunctionCall != nil {
			inputJSON, _ := json.Marshal(part.FunctionCall.Args)
			entries = append(entries, adapterprotocol.RawEntry{
				Role:      "tool",
				ToolName:  part.FunctionCall.Name,
				ToolInput: string(inputJSON),
			})
		}
		if part.FunctionResponse != nil {
			entry := adapterprotocol.RawEntry{
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
