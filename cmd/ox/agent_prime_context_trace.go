package main

import (
	"log/slog"

	"github.com/sageox/ox/internal/prime"
	"github.com/sageox/ox/internal/session/contexttrace"
)

// emitProvidedContextTrace writes "provided" events to context-trace.jsonl
// for all context loaded during prime. Best-effort: failures are logged
// but never block prime output.
func emitProvidedContextTrace(sessionDir string, teamCtx *prime.TeamContextInfo, teamInstructions *prime.TeamInstructions) {
	if sessionDir == "" {
		return
	}

	w := contexttrace.NewWriter(sessionDir)
	ts := contexttrace.Now()

	// team instructions (AGENTS.md, CLAUDE.md)
	if teamInstructions != nil {
		ev := contexttrace.Event{
			Type:         contexttrace.EventProvided,
			Timestamp:    ts,
			Source:       contexttrace.SourceTeamContext,
			Team:         teamInstructions.TeamName,
			Docs:         teamInstructions.Files,
			InlineTokens: teamInstructions.Tokens,
		}
		if err := w.Append(ev); err != nil {
			slog.Debug("context-trace: team instructions", "error", err)
		}
	}

	if teamCtx == nil {
		return
	}

	// team memory (MEMORY.md inlined)
	if teamCtx.MemoryContent != "" {
		ev := contexttrace.Event{
			Type:      contexttrace.EventProvided,
			Timestamp: ts,
			Source:    contexttrace.SourceTeamMemory,
			Team:      teamCtx.TeamName,
			Doc:       "MEMORY.md",
		}
		if err := w.Append(ev); err != nil {
			slog.Debug("context-trace: memory", "error", err)
		}
	}

	// SOUL.md (reference, not inlined)
	if teamCtx.SoulHint != "" {
		ev := contexttrace.Event{
			Type:         contexttrace.EventProvided,
			Timestamp:    ts,
			Source:       contexttrace.SourceTeamMemory,
			Team:         teamCtx.TeamName,
			Doc:          "SOUL.md",
			ReadOnDemand: true,
		}
		if err := w.Append(ev); err != nil {
			slog.Debug("context-trace: soul hint", "error", err)
		}
	}

	// TEAM.md (reference, not inlined)
	if teamCtx.TeamHint != "" {
		ev := contexttrace.Event{
			Type:         contexttrace.EventProvided,
			Timestamp:    ts,
			Source:       contexttrace.SourceTeamMemory,
			Team:         teamCtx.TeamName,
			Doc:          "TEAM.md",
			ReadOnDemand: true,
		}
		if err := w.Append(ev); err != nil {
			slog.Debug("context-trace: team hint", "error", err)
		}
	}

	// agent context (distilled-discussions.md)
	if teamCtx.HasAgentContext {
		ev := contexttrace.Event{
			Type:         contexttrace.EventProvided,
			Timestamp:    ts,
			Source:       contexttrace.SourceTeamContext,
			Team:         teamCtx.TeamName,
			Doc:          "distilled-discussions.md",
			ReadOnDemand: true,
		}
		if err := w.Append(ev); err != nil {
			slog.Debug("context-trace: agent context", "error", err)
		}
	}

	// team docs catalog (progressive disclosure)
	for _, doc := range teamCtx.TeamDocs {
		ev := contexttrace.Event{
			Type:         contexttrace.EventProvided,
			Timestamp:    ts,
			Source:       contexttrace.SourceTeamDocs,
			Team:         teamCtx.TeamName,
			Doc:          doc.Name,
			ReadOnDemand: true,
		}
		if err := w.Append(ev); err != nil {
			slog.Debug("context-trace: team doc", "error", err, "doc", doc.Name)
		}
	}
}
