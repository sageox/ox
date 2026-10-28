package main

import (
	"log/slog"
	"path"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/prime"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/teamdocs"
	"github.com/sageox/ox/internal/tokens"
)

// emitProvidedContextTrace writes "provided" events to context-trace.jsonl
// for all context loaded during prime. Best-effort: failures are logged
// but never block prime output.
//
// Every event carries Seq — the recording's entry count when sessionDir holds
// a live recording, 0 otherwise — and a single-doc event carries Title, the
// doc's basename. Doc refs are paths relative to the team-context root.
func emitProvidedContextTrace(sessionDir string, teamCtx *prime.TeamContextInfo, teamInstructions *prime.TeamInstructions) {
	if sessionDir == "" {
		return
	}

	w := contexttrace.NewWriter(sessionDir)
	ts := contexttrace.Now()
	seq := primeTraceSeq(sessionDir)

	// team instructions (AGENTS.md, CLAUDE.md)
	if teamInstructions != nil {
		ev := contexttrace.Event{
			Type:         contexttrace.EventProvided,
			Timestamp:    ts,
			Source:       contexttrace.SourceTeamContext,
			Team:         teamInstructions.TeamName,
			Docs:         teamInstructions.Files,
			InlineTokens: teamInstructions.Tokens,
			Seq:          seq,
		}
		if len(teamInstructions.Files) == 1 {
			ev.Title = docTitle(teamInstructions.Files[0])
		}
		if err := w.Append(ev); err != nil {
			slog.Debug("context-trace: team instructions", "error", err)
		}
	}

	if teamCtx == nil {
		return
	}

	// team memory (MEMORY.md inlined). InlineTokens uses the same len/4
	// heuristic as everywhere else prime estimates cost (tokens.EstimateTokens,
	// also what TeamInstructions.Tokens and prime's <context-budget> use) —
	// this was the one "provided" event still recording inline_tokens=0
	// across every trace, per the ox-32f6 audit.
	if teamCtx.MemoryContent != "" {
		ev := contexttrace.Event{
			Type:         contexttrace.EventProvided,
			Timestamp:    ts,
			Source:       contexttrace.SourceTeamMemory,
			Team:         teamCtx.TeamName,
			Doc:          "MEMORY.md",
			Title:        docTitle("MEMORY.md"),
			Seq:          seq,
			InlineTokens: tokens.EstimateTokens(teamCtx.MemoryContent),
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
			Title:        docTitle("SOUL.md"),
			Seq:          seq,
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
			Title:        docTitle("TEAM.md"),
			Seq:          seq,
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
			Title:        docTitle("distilled-discussions.md"),
			Seq:          seq,
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
			Doc:          teamDocRef(teamCtx.Path, doc),
			Title:        docTitle(doc.Name),
			ReadOnDemand: true,
			Seq:          seq,
		}
		if err := w.Append(ev); err != nil {
			slog.Debug("context-trace: team doc", "error", err, "doc", doc.Name)
		}
	}
}

// primeTraceSeq is the entry count of the recording held in sessionDir, so
// provided events sit in the same seq frame as every later trace writer. 0
// when sessionDir is not a live recording (the not-recording cache dir).
func primeTraceSeq(sessionDir string) int {
	state, err := session.ReadRecordingStateFile(sessionDir)
	if err != nil || state == nil {
		return 0
	}
	return state.EntryCount
}

// teamDocRef is a team doc's canonical ref: its path relative to the
// team-context root. Falls back to the doc's name when the doc has no path or
// lives outside that root (a doc merged in from another source).
func teamDocRef(teamRoot string, doc teamdocs.TeamDoc) string {
	if teamRoot == "" || doc.Path == "" || !filepath.IsAbs(doc.Path) {
		return doc.Name
	}
	rel, err := filepath.Rel(teamRoot, doc.Path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return doc.Name
	}
	return filepath.ToSlash(rel)
}

// docTitle is the display title for a doc ref: its basename.
func docTitle(ref string) string {
	return path.Base(filepath.ToSlash(ref))
}
