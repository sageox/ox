package daemon

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/manifest"
)

// applySparseFromManifest computes the sparse-checkout set from cfg and
// applies it to repoPath via `git sparse-checkout set --no-cone`. Empty
// computed sets are a no-op so callers can pass any manifest unconditionally.
//
// Used by both team-context sync (sync_team.go) and kb sync
// (sync_bubbles.go::reconcileBubble) so the same sparse rules are
// re-applied on every pull — not just at clone — and never drift when the
// server-side manifest changes after the initial clone.
//
// --no-cone mode lets manifests use both file and directory patterns; cone
// mode would be directory-only and reject the AGENTS.md / CLAUDE.md
// fallback entries.
//
// Failure is non-fatal: the function logs a warn and returns the error so
// callers can propagate if they need to, but the typical pattern is to
// ignore the return value because losing sparse on one pass is recoverable
// on the next.
func applySparseFromManifest(ctx context.Context, repoPath string, cfg *manifest.ManifestConfig, logger *slog.Logger) error {
	if cfg == nil {
		return nil
	}
	paths := manifest.ComputeSparseSet(cfg)
	if len(paths) == 0 {
		if logger != nil {
			logger.Debug("sparse-checkout: no paths computed, skipping", "path", repoPath)
		}
		return nil
	}
	args := append([]string{"sparse-checkout", "set", "--no-cone"}, paths...)
	if _, err := gitutil.RunGit(ctx, repoPath, args...); err != nil {
		if logger != nil {
			logger.Warn("sparse-checkout set failed", "path", repoPath, "error", err)
		}
		return fmt.Errorf("sparse-checkout set: %w", err)
	}
	if logger != nil {
		logger.Debug("sparse-checkout applied", "path", repoPath, "paths", paths)
	}
	return nil
}
