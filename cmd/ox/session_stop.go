package main

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/identity"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/version"
)

// session_stop.go contains session processing logic used by session commands.
// The actual stop command is under ox agent <id> session stop.

// processResult contains outcomes from session processing
type processResult struct {
	RawPath         string
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

	// read session metadata (agent version, model) — error is non-fatal:
	// metadata may not be available for all adapter types or session formats
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
	projectEndpointForSlug := endpoint.GetForProject(projectRoot)
	username := identity.AttributionUsername(projectEndpointForSlug, config.GetDisplayName())

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
	redactor, parseErrs := session.NewRedactorWithCustomRules(projectRoot)
	if len(parseErrs) > 0 {
		for _, pe := range parseErrs {
			slog.Warn("redaction rule parse error", "file", pe.Path, "line", pe.Line, "error", pe.Message)
		}
	}

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

	// ADR-020: apply pause/resume segment mask BEFORE secret redaction.
	// The mask excludes entries whose 0-indexed position falls in any
	// [pause_seq, resume_seq) range from the lifecycle timeline.
	// Order matters only for performance (don't redact entries being dropped);
	// correctness is identical either way.
	if len(state.Lifecycle) > 0 {
		originalCount := len(entries)
		entries = session.ApplySegmentMask(entries, state.Lifecycle)
		result.EntryCount = len(entries)

		// validator: fail-closed if the mask result doesn't match what the
		// lifecycle says should have been excluded. Aborts upload before
		// any paused-range entries can leak.
		if msg := validateMaskInvariant(originalCount, len(entries), state.Lifecycle); msg != "" {
			return nil, fmt.Errorf("upload aborted: %s", msg)
		}
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

	// use AgentType from recording state if set, fall back to AdapterName
	agentTypeForMeta := state.AgentType
	if agentTypeForMeta == "" {
		agentTypeForMeta = state.AdapterName
	}

	// fall back to recording state model for generic adapters
	if result.Model == "" && state.Model != "" {
		result.Model = state.Model
	}

	// write header with metadata; SessionID keeps the raw header a crash-safe
	// carrier of the start-minted ID on this reconstruct path too (daemon
	// orphan-finalize recovers it from here when meta.json was never written)
	meta := &session.StoreMeta{
		Version:                "1.0",
		SessionID:              state.SessionID,
		ContinuedFromSessionID: state.ContinuedFromSessionID,
		CreatedAt:              state.StartedAt,
		AgentID:                state.AgentID,
		AgentType:              agentTypeForMeta,
		AgentVersion:           result.AgentVersion,
		Model:                  result.Model,
		Username:               identity.AttributionDisplayName(projectEndpoint, config.GetDisplayName()),
		RepoID:                 repoID,
		OxVersion:              version.Version,
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

	return result, nil
}
