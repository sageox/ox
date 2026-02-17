package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	sessionhtml "github.com/sageox/ox/internal/session/html"
)

// session_stop.go contains session processing logic used by session commands.
// The actual stop command is under ox agent <id> session stop.

// processResult contains outcomes from session processing
type processResult struct {
	RawPath         string
	EventsPath      string
	HTMLPath        string
	EntryCount      int
	SecretsRedacted int
	AgentVersion    string
	Model           string
}

// processSession reads, redacts secrets, and saves the session.
// Both raw and events sessions have secrets scrubbed before storage.
func processSession(projectRoot string, state *session.RecordingState) (*processResult, error) {
	result := &processResult{}

	// resolve project endpoint for auth lookups
	projectEndpoint := endpoint.GetForProject(projectRoot)

	// get adapter
	adapter, err := adapters.GetAdapter(state.AdapterName)
	if err != nil {
		return nil, fmt.Errorf("adapter not found: %w", err)
	}

	// read session metadata (agent version, model)
	sessionMeta, _ := adapter.ReadMetadata(state.SessionFile)
	if sessionMeta != nil {
		result.AgentVersion = sessionMeta.AgentVersion
		result.Model = sessionMeta.Model
	}

	// read entries from session file
	rawEntries, err := adapter.Read(state.SessionFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read session: %w", err)
	}

	if len(rawEntries) == 0 {
		return result, nil // nothing to process
	}
	result.EntryCount = len(rawEntries)

	// get user info for filename
	username := getSessionUsername()

	// get repo ID and create store using helper
	repoID := getRepoIDOrDefault(projectRoot)

	// create store
	contextPath := session.GetContextPath(repoID)
	if contextPath == "" {
		return nil, fmt.Errorf("failed to get context path")
	}

	store, err := session.NewStore(contextPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create store: %w", err)
	}

	// generate filename
	filename := session.GenerateFilename(username, state.AgentID)

	// create redactor for secret scrubbing
	// CRITICAL: Both raw and events sessions must have secrets scrubbed
	// before storage to prevent credential leaks in ledgers
	redactor := session.NewRedactor()

	// convert raw entries to session entries and redact secrets
	entries := make([]session.Entry, 0, len(rawEntries))
	for _, raw := range rawEntries {
		entry := session.Entry{
			Timestamp: raw.Timestamp,
			Content:   raw.Content,
			ToolName:  raw.ToolName,
			ToolInput: raw.ToolInput,
		}

		// map role to entry type
		switch raw.Role {
		case "user":
			entry.Type = session.EntryTypeUser
		case "assistant":
			entry.Type = session.EntryTypeAssistant
		case "system":
			entry.Type = session.EntryTypeSystem
		case "tool":
			entry.Type = session.EntryTypeTool
		default:
			entry.Type = session.EntryTypeSystem
		}

		entries = append(entries, entry)
	}

	// redact secrets from entries (modifies in place)
	result.SecretsRedacted = redactor.RedactEntries(entries)

	// also redact the raw JSON if present
	for i := range rawEntries {
		if len(rawEntries[i].Raw) > 0 {
			var rawData map[string]any
			if json.Unmarshal(rawEntries[i].Raw, &rawData) == nil {
				if redactor.RedactMap(rawData) {
					// re-marshal with redacted content
					if redactedJSON, err := json.Marshal(rawData); err == nil {
						rawEntries[i].Raw = redactedJSON
					}
				}
			}
		}
	}

	// write raw session (with secrets redacted)
	rawWriter, err := store.CreateRaw(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to create raw session: %w", err)
	}

	// write header with metadata
	meta := &session.StoreMeta{
		Version:      "1.0",
		CreatedAt:    state.StartedAt,
		AgentID:      state.AgentID,
		AgentType:    state.AdapterName,
		AgentVersion: result.AgentVersion,
		Model:        result.Model,
		Username:     getDisplayName(projectEndpoint),
		RepoID:       repoID,
	}
	if err := rawWriter.WriteHeader(meta); err != nil {
		rawWriter.Close()
		return nil, fmt.Errorf("failed to write header: %w", err)
	}

	// write entries
	for _, entry := range entries {
		data := map[string]any{
			"type":      string(entry.Type),
			"content":   entry.Content,
			"timestamp": entry.Timestamp,
		}
		if entry.ToolName != "" {
			data["tool_name"] = entry.ToolName
		}
		if entry.ToolInput != "" {
			data["tool_input"] = entry.ToolInput
		}
		if entry.ToolOutput != "" {
			data["tool_output"] = entry.ToolOutput
		}
		if err := rawWriter.WriteRaw(data); err != nil {
			rawWriter.Close()
			return nil, fmt.Errorf("failed to write entry: %w", err)
		}
	}

	if err := rawWriter.Close(); err != nil {
		return nil, fmt.Errorf("failed to close raw session: %w", err)
	}
	result.RawPath = rawWriter.FilePath()

	// generate and write events session
	eventLog := session.NewEventLog(entries, state.AgentID, state.AdapterName)
	eventsWriter, err := store.CreateEvents(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to create events session: %w", err)
	}

	// write events header
	eventsMeta := &session.StoreMeta{
		Version:      "1.0",
		CreatedAt:    state.StartedAt,
		AgentID:      state.AgentID,
		AgentType:    state.AdapterName,
		AgentVersion: result.AgentVersion,
		Model:        result.Model,
		Username:     getDisplayName(projectEndpoint),
		RepoID:       repoID,
	}
	if err := eventsWriter.WriteHeader(eventsMeta); err != nil {
		eventsWriter.Close()
		return nil, fmt.Errorf("failed to write events header: %w", err)
	}

	// write events (events are derived from already-scrubbed entries)
	for _, event := range eventLog.Events {
		data := map[string]any{
			"type":      string(event.Type),
			"summary":   event.Summary,
			"timestamp": event.Timestamp,
		}
		if event.Details != "" {
			data["details"] = event.Details
		}
		if event.ErrorMsg != "" {
			data["error"] = event.ErrorMsg
		}
		if event.RelatedFile != "" {
			data["file"] = event.RelatedFile
		}
		if event.Success != nil {
			data["success"] = *event.Success
		}
		if err := eventsWriter.WriteRaw(data); err != nil {
			eventsWriter.Close()
			return nil, fmt.Errorf("failed to write event: %w", err)
		}
	}

	if err := eventsWriter.Close(); err != nil {
		return nil, fmt.Errorf("failed to close events session: %w", err)
	}
	result.EventsPath = eventsWriter.FilePath()

	// generate HTML viewer
	if result.RawPath != "" {
		htmlGen, err := sessionhtml.NewGenerator()
		if err == nil {
			// read back the raw session
			rawSession, readErr := store.ReadSession(filename)
			if readErr == nil && rawSession != nil {
				htmlPath := strings.TrimSuffix(result.RawPath, ".jsonl") + ".html"
				if genErr := htmlGen.GenerateToFile(rawSession, htmlPath); genErr == nil {
					result.HTMLPath = htmlPath
				}
			}
		}
	}

	return result, nil
}
