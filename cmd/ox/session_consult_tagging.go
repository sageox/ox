package main

import (
	"log/slog"
	"path/filepath"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session/consultscan"
)

// tagSessionConsults runs the deterministic knowledge-flow tagger over a just-
// finalized session's LOCAL raw.jsonl and appends turn-anchored `consulted`
// events (reads of SageOx files, ox retrieval commands) and provable `used`
// events (a surfaced ref re-read, re-fetched by id, or cited in assistant
// output — surfaced refs come from the trace already on disk) to its
// context-trace (epic ox-bcgb, tier 1). It is strictly best-effort: any
// failure — a bad config, a panic in the scanner, a write error — is logged
// and swallowed so it can never affect the recording. Runs against the local
// recording cache (real content), never the ledger LFS pointer, per the
// cache-only rule. Idempotent, so a retry never doubles a receipt.
func tagSessionConsults(projectRoot, sessionPath, rawPath string) {
	defer func() {
		if r := recover(); r != nil {
			slog.Debug("consult tagging recovered from panic", "recover", r)
		}
	}()

	// ADR-027 opt-out: off means nothing about this coworker's reads is
	// written, so nothing about them reaches the team.
	if !config.TraceConsultationsEnabled() {
		slog.Debug("consult tagging skipped", "reason", "recap.trace_consultations off")
		return
	}

	n, err := consultscan.TagSessionReads(rawPath, sessionPath, consultRoots(projectRoot))
	if err != nil {
		slog.Debug("consult tagging failed", "error", err)
		return
	}
	if n > 0 {
		slog.Info("tagged SageOx consults", "session", filepath.Base(sessionPath), "events", n)
	}
}

// consultRoots are the SageOx repo roots whose files count as consulted when
// read: this project's ledger clone and its team-context repo. Finalize and
// the live `ox brief` scan share it so both grade the same reads. No roots
// only disables file-read matching; citation and requery evidence needs none,
// so a scan still runs.
func consultRoots(projectRoot string) consultscan.Roots {
	roots := consultscan.Roots{}
	if ctx, err := config.LoadProjectContext(projectRoot); err == nil && ctx != nil {
		roots.Ledger = ctx.DefaultLedgerPath()
	}
	if tc := config.FindRepoTeamContext(projectRoot); tc != nil && tc.Path != "" {
		roots.TeamContext = append(roots.TeamContext, tc.Path)
	}
	return roots
}
