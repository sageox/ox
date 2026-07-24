package main

import (
	"log/slog"
	"path/filepath"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session/consultscan"
)

// tagSessionConsults runs the deterministic knowledge-flow tagger over a just-
// finalized session's LOCAL raw.jsonl and appends turn-anchored `consulted`
// events to its context-trace (epic ox-bcgb, tier 1). It is strictly
// best-effort: any failure — a bad config, a panic in the scanner, a write
// error — is logged and swallowed so it can never affect the recording. Runs
// against the local recording cache (real content), never the ledger LFS
// pointer, per the cache-only rule.
func tagSessionConsults(projectRoot, sessionPath, rawPath string) {
	defer func() {
		if r := recover(); r != nil {
			slog.Debug("consult tagging recovered from panic", "recover", r)
		}
	}()

	roots := consultscan.Roots{}
	if ctx, err := config.LoadProjectContext(projectRoot); err == nil && ctx != nil {
		roots.Ledger = ctx.DefaultLedgerPath()
	}
	if tc := config.FindRepoTeamContext(projectRoot); tc != nil && tc.Path != "" {
		roots.TeamContext = append(roots.TeamContext, tc.Path)
	}
	if roots.Ledger == "" && len(roots.TeamContext) == 0 {
		return // nothing to match reads against
	}

	n, err := consultscan.TagSessionReads(rawPath, sessionPath, roots)
	if err != nil {
		slog.Debug("consult tagging failed", "error", err)
		return
	}
	if n > 0 {
		slog.Info("tagged SageOx consults", "session", filepath.Base(sessionPath), "turns", n)
	}
}
