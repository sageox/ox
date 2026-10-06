package main

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/doctor"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/identity"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/session/claudesource"
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

	agentTypeForMeta := adapters.CanonicalAdapterName(state.AgentType)
	if agentTypeForMeta == "" {
		agentTypeForMeta = adapters.CanonicalAdapterName(state.AdapterName)
	}

	meta := &session.StoreMeta{
		Version:                "1.0",
		CreatedAt:              state.StartedAt,
		AgentID:                state.AgentID,
		SessionID:              state.SessionID,
		ContinuedFromSessionID: state.ContinuedFromSessionID,
		AgentType:              agentTypeForMeta,
		Model:                  state.Model,
		Username:               identity.AttributionDisplayName(projectEndpoint, config.GetDisplayName()),
		RepoID:                 repoID,
		OxVersion:              version.Version,
		NativeSessions:         state.NativeSessions,
		TraceCapture:           state.Trace,
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

	if err := os.MkdirAll(filepath.Dir(rawPath), 0755); err != nil {
		return fmt.Errorf("create raw.jsonl dir: %w", err)
	}

	// route the header through the RawWriter chokepoint so every byte in
	// raw.jsonl — including this metadata line — passes the redaction
	// stack. Truncate because the header is always the first line written
	// at session start.
	w, err := session.NewRawWriterTruncate(rawPath, projectRoot)
	if err != nil {
		return fmt.Errorf("open raw.jsonl: %w", err)
	}
	if err := w.WriteRaw(header); err != nil {
		_ = w.Close()
		return fmt.Errorf("write raw.jsonl header: %w", err)
	}
	if err := w.CloseAndSync(); err != nil {
		return fmt.Errorf("sync raw.jsonl header: %w", err)
	}

	return nil
}

// finalizeIncrementalSession completes a session that was incrementally recorded
// by hooks. Does a final drain from the source file, then generates events,
// and summary artifacts from the already-written raw.jsonl.
func finalizeIncrementalSession(projectRoot string, state *session.RecordingState, rawPath string, adapter adapters.Adapter, result *agentSessionResult) (*agentSessionResult, error) {
	// final drain: read any remaining entries since last hook
	if reader, ok := adapter.(adapters.IncrementalReader); ok && state.SessionFile != "" {
		// use StartOffset as minimum read position to skip pre-session content
		readOffset := state.SourceOffset
		if state.StartOffset > 0 && readOffset < state.StartOffset {
			readOffset = state.StartOffset
		}

		// diagnostic: log source file state for debugging truncation issues
		if fi, statErr := os.Stat(state.SessionFile); statErr == nil {
			slog.Info("finalize: final drain", "source", state.SessionFile, "file_size", fi.Size(), "read_offset", readOffset, "start_offset", state.StartOffset)
		}

		var sourceSnapshot os.FileInfo
		// A native transcript or workspace that no longer exists cannot be
		// drained or rechecked, but raw.jsonl was ownership-checked batch by
		// batch as it was written. Finalize that capture rather than fail
		// every stop and recover on a source that will never come back.
		skipDrain := false
		if state.AdapterName == "claude-code" {
			var snapshotErr error
			sourceSnapshot, snapshotErr = claudesource.Snapshot(state.SessionFile)
			if errors.Is(snapshotErr, fs.ErrNotExist) {
				slog.Warn("finalize: native session is gone; finalizing the captured recording", "source", state.SessionFile)
				skipDrain = true
			} else if snapshotErr != nil {
				return nil, fmt.Errorf("stat native session before final drain: %w", snapshotErr)
			}
		}
		var entries []adapters.RawEntry
		var newOffset int64
		if !skipDrain {
			var readErr error
			entries, newOffset, readErr = reader.ReadFromOffset(state.SessionFile, readOffset)
			if readErr != nil {
				return nil, fmt.Errorf("read final session entries: %w", readErr)
			}
		}
		if state.AdapterName == "claude-code" && !skipDrain {
			repoRoot := state.WorkspacePath
			if repoRoot == "" {
				repoRoot = projectRoot
			}
			if err := claudesource.ValidateRecorded(state.SessionFile, repoRoot, state.AgentSessionID, state.StartOffset, sourceSnapshot); errors.Is(err, claudesource.ErrSourceGone) {
				// the transcript itself vanished mid-check: nothing read can be vouched for
				slog.Warn("finalize: native session cannot be rechecked; finalizing the captured recording", "source", state.SessionFile, "error", err)
				entries = nil
			} else if err != nil {
				return nil, fmt.Errorf("claude source changed repository during final drain: %w", err)
			}
		}
		if len(entries) > 0 {
			slog.Info("finalize: drain result", "entries_read", len(entries), "new_offset", newOffset)

			// filter entries by timestamp — strict After() to prevent boundary leaks
			if !state.StartedAt.IsZero() {
				filtered := make([]adapters.RawEntry, 0, len(entries))
				for _, e := range entries {
					if e.Timestamp.After(state.StartedAt) {
						filtered = append(filtered, e)
					}
				}
				entries = filtered
			}

			if len(entries) > 0 {
				// Per ox-h20u: redaction is enforced by the RawWriter
				// chokepoint that appendRedactedEntries delegates to.
				// Callers no longer pre-redact — the writer guarantees
				// every byte passes through cmd-allowlist + builtin +
				// gitleaks layers in order before encoding.
				drainEntries := session.ConvertRawEntries(entries)

				writer, err := session.NewRawWriter(rawPath, projectRoot)
				if err != nil {
					return nil, err
				}
				appendErr := writer.AppendRecordingBatch(filepath.Join(state.SessionPath, ".recording.json"), drainEntries, newOffset)
				closeErr := writer.Close()
				if appendErr != nil {
					return nil, appendErr
				}
				if closeErr != nil {
					return nil, closeErr
				}
				state.SourceOffset = newOffset
				state.EntryCount += len(entries)

			}
		}
	}

	// The header was written at start, before any /clear or resume could add
	// a native session id and before the stop time existed. Append both now
	// as the footer so the ledger copy of raw.jsonl is self-describing on its
	// own (a daemon retry of this upload has no .recording.json to consult).
	// Appended rather than rewritten: a parallel PostToolUse hook may still
	// hold the file open. Best-effort: meta.json gets the same values from
	// state regardless.
	stoppedAt := session.ResolveStoppedAt(state.StoppedAt, rawPath, time.Now())
	if err := session.StampRawCarrier(rawPath, session.CarrierStamp{
		NativeSessions: state.NativeSessions,
		TraceCapture:   state.Trace,
		StoppedAt:      stoppedAt,
	}); err != nil {
		slog.Warn("finalize: could not stamp raw.jsonl carrier", "session", state.SessionPath, "error", err)
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

	// Deterministic knowledge-flow tagging (epic ox-bcgb, tier 1): tag the turns
	// where the agent consulted SageOx knowledge — reads of ledger/team-context
	// files, and `ox` retrieval commands. Strictly best-effort against the LOCAL
	// raw.jsonl (real content, per the cache-only rule); it appends to the
	// session's context-trace and NEVER affects the recording result.
	tagSessionConsults(projectRoot, state.SessionPath, rawPath)

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
		if callID, ok := rawMap["call_id"].(string); ok {
			entry.CallID = callID
		}
		if isError, ok := rawMap["is_error"].(bool); ok {
			entry.IsError = isError
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

	// A held session gets its hold before anything the daemon reacts to, and
	// no summary prompt: push-summary publishes (GH #1093). If the hold cannot
	// be recorded, the stop fails and the recording state is kept.
	sessionCacheDir := filepath.Dir(result.RawPath)
	held := stopHoldsSession(projectRoot, state)
	if held {
		if err := holdStoppedSession(result, sessionCacheDir, "session_stop"); err != nil {
			return nil, err
		}
	} else {
		result.SummaryPrompt = session.BuildSummaryPrompt(sessionEntries, result.RawPath, result.LedgerSessionDir)
		_ = session.WriteNeedsSummaryMarker(sessionCacheDir, result.RawPath, result.LedgerSessionDir)
	}

	// generate all session artifacts via shared path
	artifactPaths, artifactErr := session.WriteSessionArtifacts(filepath.Dir(result.RawPath), storedSession, summaryResp)
	if artifactErr != nil {
		_ = doctor.SetNeedsDoctorAgent(projectRoot)
		slog.Debug("artifact generation failed", "error", artifactErr)
	} else {
		result.SummaryMDPath = artifactPaths.SummaryMD
		result.SessionMDPath = artifactPaths.SessionMD
	}

	// check for plan.md saved during session
	planSrcPath := filepath.Join(state.SessionPath, ledgerFilePlan)
	if _, statErr := os.Stat(planSrcPath); statErr == nil {
		cacheDir := filepath.Dir(result.RawPath)
		planDstPath := filepath.Join(cacheDir, ledgerFilePlan)
		data, readErr := os.ReadFile(planSrcPath)
		if readErr != nil {
			slog.Warn("plan.md read failed", "path", planSrcPath, "error", readErr)
		} else if writeErr := os.WriteFile(planDstPath, data, 0644); writeErr != nil {
			slog.Warn("plan.md copy failed", "dst", planDstPath, "error", writeErr)
		} else {
			result.PlanPath = planDstPath
		}
	}

	// ledger upload
	if held {
		slog.Info("session held on this machine, skipping upload", "session", sessionName)
		result.LedgerSessionDir = ""
		result.UploadWarning = heldSessionWarning(sessionName)
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
			// keep cache alive — raw.jsonl in ledger becomes an LFS stub after push,
			// but push-summary needs to read it. Cache is pruned later by
			// clearNeedsSummaryMarkerForSession after push-summary completes.

			if result.LedgerSessionDir != "" {
				// rewrite secondary artifact paths to ledger (small git-tracked files, not LFS)
				// but keep RawPath and SummaryPrompt pointing to cache so agents can read raw.jsonl
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
				rewriteIfExists(&result.SummaryMDPath, ledgerFileSummaryMD)
				rewriteIfExists(&result.SessionMDPath, ledgerFileSessionMD)
				rewriteIfExists(&result.PlanPath, ledgerFilePlan)
			}
		}
	}

	return result, nil
}
