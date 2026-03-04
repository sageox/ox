package gitserver

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/manifest"
)

// TwoPhaseCloneResult holds the result of a two-phase team context clone.
type TwoPhaseCloneResult struct {
	ManifestConfig *manifest.ManifestConfig
	SparsePaths    []string
}

// TwoPhaseClone performs a two-phase partial clone for team context repos.
//
// Phase 1: Clone with --filter=blob:none --depth=1 --sparse --no-checkout,
// materialize only .sageox/ to read the manifest.
//
// Phase 2: Read manifest, compute sparse set, apply sparse checkout to
// materialize only allowed paths. Unshallow for pull --rebase compatibility.
//
// This function is used by both the daemon (normal path) and the CLI
// (doctor fallback when daemon unavailable).
func TwoPhaseClone(ctx context.Context, cloneURL, repoPath string) (*TwoPhaseCloneResult, error) {
	// phase 1: minimal clone — trees only, no blobs
	cloneCmd := exec.CommandContext(ctx, "git", "clone",
		"--filter=blob:none",
		"--depth=1",
		"--sparse",
		"--no-checkout",
		"--single-branch",
		"--branch", "main",
		"--quiet",
		cloneURL, repoPath,
	)
	if output, err := cloneCmd.CombinedOutput(); err != nil {
		sanitized := gitutil.SanitizeOutput(string(output))
		if sanitized != "" {
			return nil, fmt.Errorf("phase 1 clone failed: %s", sanitized)
		}
		return nil, fmt.Errorf("phase 1 clone failed: %w", err)
	}

	// materialize only .sageox/ to read the manifest.
	// use --no-cone mode to support both file and directory patterns in Phase 2.
	if _, err := gitutil.RunGit(ctx, repoPath, "sparse-checkout", "set", "--no-cone", ".sageox/"); err != nil {
		return nil, fmt.Errorf("phase 1 sparse-checkout set .sageox: %w", err)
	}
	if _, err := gitutil.RunGit(ctx, repoPath, "checkout", "HEAD"); err != nil {
		return nil, fmt.Errorf("phase 1 checkout HEAD: %w", err)
	}

	// phase 2: read manifest and materialize declared paths
	manifestPath := filepath.Join(repoPath, ".sageox", "sync.manifest")
	cfg := manifest.ParseFile(manifestPath)

	sparsePaths := manifest.ComputeSparseSet(cfg)
	if len(sparsePaths) == 0 {
		sparsePaths = []string{".sageox/"}
	}

	// ensure .sageox/ is always in the sparse set
	hasSageox := false
	for _, p := range sparsePaths {
		if p == ".sageox/" || p == ".sageox" {
			hasSageox = true
			break
		}
	}
	if !hasSageox {
		sparsePaths = append([]string{".sageox/"}, sparsePaths...)
	}

	args := append([]string{"sparse-checkout", "set", "--no-cone"}, sparsePaths...)
	if _, err := gitutil.RunGit(ctx, repoPath, args...); err != nil {
		slog.Warn("phase 2 sparse-checkout set failed, continuing with .sageox only",
			"path", repoPath, "error", err)
	}

	// checkout HEAD to materialize the newly declared paths
	if _, err := gitutil.RunGit(ctx, repoPath, "checkout", "HEAD"); err != nil {
		return nil, fmt.Errorf("phase 2 checkout HEAD: %w", err)
	}

	// unshallow so subsequent fetch/pull --rebase work correctly.
	// --depth=1 creates a shallow clone; fetch --unshallow converts to full depth
	// but with --filter=blob:none active, only fetches commit/tree objects.
	if _, err := gitutil.RunGit(ctx, repoPath, "fetch", "--unshallow", "--quiet"); err != nil {
		// non-fatal: pull --rebase may still work if remote only has 1 commit
		slog.Debug("unshallow fetch failed (may be single-commit repo)", "path", repoPath, "error", err)
	}

	// strip lfs config that git-lfs may have injected during clone
	gitutil.StripLFSConfig(repoPath)

	// ensure .sageox/.gitignore excludes daemon-written files (cache/, checkout.json, etc.)
	// so they don't appear as untracked and block blue-green GC reclone
	if err := EnsureCheckoutGitignoreCtx(ctx, repoPath); err != nil {
		slog.Warn("failed to ensure checkout .gitignore", "path", repoPath, "error", err)
	}

	return &TwoPhaseCloneResult{
		ManifestConfig: cfg,
		SparsePaths:    sparsePaths,
	}, nil
}

// ValidateTeamContextClone checks that a freshly cloned team context has
// expected content. All checks are warning-only — a missing file does not
// fail the clone.
func ValidateTeamContextClone(repoPath string, cfg *manifest.ManifestConfig) {
	coreFiles := []string{"SOUL.md", "TEAM.md", "MEMORY.md"}
	found := false
	for _, f := range coreFiles {
		if _, err := os.Stat(filepath.Join(repoPath, f)); err == nil {
			found = true
			break
		}
	}
	if !found {
		slog.Warn("team context missing all core files (SOUL.md, TEAM.md, MEMORY.md)",
			"path", repoPath)
	}

	if _, err := os.Stat(filepath.Join(repoPath, "memory")); os.IsNotExist(err) {
		slog.Debug("team context has no memory/ directory", "path", repoPath)
	}

	if cfg != nil {
		for _, denied := range cfg.Denies {
			deniedPath := filepath.Join(repoPath, strings.TrimSuffix(denied, "/"))
			if _, err := os.Stat(deniedPath); err == nil {
				slog.Warn("denied path exists after clone", "path", repoPath, "denied", denied)
			}
		}
	}
}
