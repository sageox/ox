package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/doctor"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	sessionhtml "github.com/sageox/ox/internal/session/html"
	"github.com/sageox/ox/internal/version"
)

// writeRawHeader writes the metadata header line to raw.jsonl so that
// incremental hooks can append entries after it. Called at session start.
func writeRawHeader(projectRoot string, state *session.RecordingState) error {
	if state.SessionPath == "" {
		return fmt.Errorf("session path is empty")
	}

	rawPath := filepath.Join(state.SessionPath, "raw.jsonl")

	projectEndpoint := endpoint.GetForProject(projectRoot)
	repoID := getRepoIDOrDefault(projectRoot)

	agentTypeForMeta := state.AgentType
	if agentTypeForMeta == "" {
		agentTypeForMeta = state.AdapterName
	}

	meta := &session.StoreMeta{
		Version:   "1.0",
		CreatedAt: state.StartedAt,
		AgentID:   state.AgentID,
		AgentType: agentTypeForMeta,
		Model:     state.Model,
		Username:  getDisplayName(projectEndpoint),
		RepoID:    repoID,
		OxVersion: version.Version,
	}

	// enrich with adapter metadata if available
	if state.SessionFile != "" {
		adapter, err := adapters.GetAdapter(state.AdapterName)
		if err == nil {
			if sessionMeta, _ := adapter.ReadMetadata(state.SessionFile); sessionMeta != nil {
				meta.AgentVersion = sessionMeta.AgentVersion
				if sessionMeta.Model != "" {
					meta.Model = sessionMeta.Model
				}
			}
		}
	}

	header := map[string]any{
		"type":     "header",
		"metadata": meta,
	}

	data, err := json.Marshal(header)
	if err != nil {
		return fmt.Errorf("marshal header: %w", err)
	}
	data = append(data, '\n')

	if err := os.MkdirAll(filepath.Dir(rawPath), 0755); err != nil {
		return fmt.Errorf("create raw.jsonl dir: %w", err)
	}

	if err := os.WriteFile(rawPath, data, 0644); err != nil {
		return fmt.Errorf("write raw.jsonl header: %w", err)
	}

	return nil
}

// finalizeIncrementalSession completes a session that was incrementally recorded
// by hooks. Does a final drain from the source file, then generates events,
// HTML, and summary artifacts from the already-written raw.jsonl.
func finalizeIncrementalSession(projectRoot string, state *session.RecordingState, rawPath string, adapter adapters.Adapter, result *agentSessionResult) (*agentSessionResult, error) {
	// final drain: read any remaining entries since last hook
	if reader, ok := adapter.(adapters.IncrementalReader); ok && state.SessionFile != "" {
		entries, newOffset, readErr := reader.ReadFromOffset(state.SessionFile, state.SourceOffset)
		if readErr != nil {
			slog.Debug("finalize: incremental read failed", "error", readErr)
		} else if len(entries) > 0 {
			if !state.StartedAt.IsZero() {
				filtered := make([]adapters.RawEntry, 0, len(entries))
				for _, e := range entries {
					if !e.Timestamp.Before(state.StartedAt) {
						filtered = append(filtered, e)
					}
				}
				entries = filtered
			}

			if len(entries) > 0 {
				redactor, _ := session.NewRedactorWithCustomRules(projectRoot)

				drainEntries := make([]session.Entry, 0, len(entries))
				for _, raw := range entries {
					entry := session.Entry{
						Timestamp: raw.Timestamp,
						Content:   raw.Content,
						ToolName:  raw.ToolName,
						ToolInput: raw.ToolInput,
					}
					entry.Type = mapRoleToEntryType(raw.Role)
					drainEntries = append(drainEntries, entry)
				}
				redactor.RedactEntries(drainEntries)

				if appendErr := appendRedactedEntries(rawPath, drainEntries); appendErr != nil {
					slog.Debug("finalize: append entries failed", "error", appendErr)
				} else {
					// only advance offset/count after successful append;
					// leaving them unchanged lets the next drain retry these entries
					_ = session.UpdateRecordingStateForAgent(projectRoot, state.AgentID, func(s *session.RecordingState) {
						s.SourceOffset = newOffset
						s.EntryCount += len(entries)
					})
				}
			}
		}
	}

	// read back the completed raw.jsonl to generate artifacts
	storedSession, err := session.ReadSessionFromPath(rawPath)
	if err != nil {
		return nil, fmt.Errorf("read incremental raw.jsonl: %w", err)
	}

	result.RawPath = rawPath
	result.EntryCount = len(storedSession.Entries)

	if result.EntryCount == 0 {
		return result, nil
	}

	// reconstruct session entries from stored raw for event generation and summary
	sessionEntries := make([]session.Entry, 0, len(storedSession.Entries))
	for _, rawMap := range storedSession.Entries {
		entry := session.Entry{}
		if ts, ok := rawMap["timestamp"]; ok {
			if tsStr, ok := ts.(string); ok {
				if parsed, valid := session.ParseTimestamp(tsStr); valid {
					entry.Timestamp = parsed
				}
			}
		}
		if content, ok := rawMap["content"].(string); ok {
			entry.Content = content
		}
		if entryType, ok := rawMap["type"].(string); ok {
			entry.Type = session.SessionEntryType(entryType)
		}
		if toolName, ok := rawMap["tool_name"].(string); ok {
			entry.ToolName = toolName
		}
		if toolInput, ok := rawMap["tool_input"].(string); ok {
			entry.ToolInput = toolInput
		}
		if toolOutput, ok := rawMap["tool_output"].(string); ok {
			entry.ToolOutput = toolOutput
		}
		sessionEntries = append(sessionEntries, entry)
	}

	// extract metadata from header and adapter
	if storedSession.Meta != nil {
		if storedSession.Meta.AgentVersion != "" {
			result.AgentVersion = storedSession.Meta.AgentVersion
		}
		if storedSession.Meta.Model != "" {
			result.Model = storedSession.Meta.Model
		}
	}
	sessionMeta, _ := adapter.ReadMetadata(state.SessionFile)
	if sessionMeta != nil {
		if sessionMeta.AgentVersion != "" {
			result.AgentVersion = sessionMeta.AgentVersion
		}
		if sessionMeta.Model != "" {
			result.Model = sessionMeta.Model
		}
	}
	if result.Model == "" && state.Model != "" {
		result.Model = state.Model
	}

	sessionName := session.GetSessionName(state.SessionPath)
	result.SessionName = sessionName

	// generate summary
	localSummary := session.LocalSummary(sessionEntries)
	summaryResp := &session.SummarizeResponse{
		Summary: localSummary,
	}
	result.Summary = localSummary

	ledgerPath, ledgerErr := resolveLedgerPath()
	if ledgerErr == nil {
		result.LedgerSessionDir = filepath.Join(ledgerPath, "sessions", sessionName)
	}
	result.SummaryPrompt = session.BuildSummaryPrompt(sessionEntries, result.RawPath, result.LedgerSessionDir)

	sessionCacheDir := filepath.Dir(result.RawPath)
	_ = session.WriteNeedsSummaryMarker(sessionCacheDir, result.RawPath, result.LedgerSessionDir)

	// generate all session artifacts via shared path
	htmlGen, _ := sessionhtml.NewGenerator()
	artifactPaths, artifactErr := session.WriteSessionArtifacts(filepath.Dir(result.RawPath), storedSession, summaryResp, htmlGen)
	if artifactErr != nil {
		_ = doctor.SetNeedsDoctorAgent(projectRoot)
		slog.Debug("artifact generation failed", "error", artifactErr)
	} else {
		result.HTMLPath = artifactPaths.HTML
		result.SummaryMDPath = artifactPaths.SummaryMD
		result.SessionMDPath = artifactPaths.SessionMD
	}

	// check for plan.md saved during session
	planSrcPath := filepath.Join(state.SessionPath, ledgerFilePlan)
	if _, statErr := os.Stat(planSrcPath); statErr == nil {
		cacheDir := filepath.Dir(result.RawPath)
		planDstPath := filepath.Join(cacheDir, ledgerFilePlan)
		if data, readErr := os.ReadFile(planSrcPath); readErr == nil {
			if writeErr := os.WriteFile(planDstPath, data, 0644); writeErr == nil {
				result.PlanPath = planDstPath
			}
		}
	}

	// ledger upload
	publishMode := config.GetSessionPublishing(projectRoot)
	if publishMode == config.SessionPublishingManual {
		slog.Info("session publishing mode is manual, skipping upload", "session", sessionName)
		result.LedgerSessionDir = ""
		result.UploadWarning = "Session saved locally (publishing mode: manual). Use 'ox session upload' to publish."
		return result, nil
	}

	if ledgerErr != nil {
		_ = doctor.SetNeedsDoctorAgent(projectRoot)
		fmt.Fprintf(os.Stderr, "warning: LFS upload skipped (no ledger): %v\n", ledgerErr)
		result.LedgerSessionDir = ""
		result.UploadWarning = "Session saved locally but ledger upload skipped (no ledger). Run 'ox doctor' to retry."
	} else {
		uploadStart := time.Now()
		uploadErr := uploadSessionToLedger(projectRoot, result, state, ledgerPath, sessionName)
		result.UploadMs = time.Since(uploadStart).Milliseconds()
		if uploadErr != nil {
			if errors.Is(uploadErr, api.ErrReadOnly) {
				fmt.Fprintln(os.Stderr, "\nUpload skipped — you have read-only access to this public repo.")
				fmt.Fprintln(os.Stderr, "To upload sessions, request team membership from an admin.")
			} else {
				_ = doctor.SetNeedsDoctorAgent(projectRoot)
				fmt.Fprintf(os.Stderr, "warning: LFS upload failed (session saved locally): %v\n", uploadErr)
				result.UploadWarning = "Session saved locally but ledger upload failed. Run 'ox doctor' to retry."
			}
			result.LedgerSessionDir = ""
		} else {
			if cacheDir := filepath.Dir(result.RawPath); cacheDir != "" && cacheDir != "." {
				if err := os.RemoveAll(cacheDir); err != nil {
					slog.Debug("prune session cache", "dir", cacheDir, "error", err)
				}
			}

			if result.LedgerSessionDir != "" {
				result.RawPath = filepath.Join(result.LedgerSessionDir, ledgerFileRaw)

				rewriteIfExists := func(field *string, name string) {
					if *field == "" {
						return
					}
					p := filepath.Join(result.LedgerSessionDir, name)
					if _, err := os.Stat(p); err == nil {
						*field = p
					} else {
						*field = ""
					}
				}
				rewriteIfExists(&result.HTMLPath, ledgerFileHTML)
				rewriteIfExists(&result.SummaryMDPath, ledgerFileSummaryMD)
				rewriteIfExists(&result.SessionMDPath, ledgerFileSessionMD)
				rewriteIfExists(&result.PlanPath, ledgerFilePlan)

				result.SummaryPrompt = session.BuildSummaryPrompt(sessionEntries, result.RawPath, result.LedgerSessionDir)
			}
		}
	}

	return result, nil
}
