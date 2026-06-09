package gitserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
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

// TestAllowFileTransport is a test-only escape hatch that disables the
// `-c protocol.file.allow=never` hardening in TwoPhaseClone. The Blue-green
// GC tests clone from a local bare repo via file:// to simulate a remote;
// they MUST set this to true for the duration of the test (the production
// default is false). Setting this in non-test code is a security bug.
//
// We use a package var (not a parameter) to keep the production call sites
// in sync.go / sync_gc.go / sync_team.go unchanged — the hardening still
// applies to every production code path because none of them flip the var.
var TestAllowFileTransport bool

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
//
// cloneURL MUST be bare (no embedded oauth2:TOKEN userinfo). Credentials are
// resolved via the ox credential helper — installed as argv flags on the
// phase-1 clone and persisted into .git/config afterward. Passing a
// token-embedded URL would leak the PAT into .git/config (forbidden by
// ox-eeqi) without changing behavior.
//
// TODO(investigate): go-git v6 has native support for this entire sequence:
//   - CloneOptions.Filter, Depth, NoCheckout, SingleBranch for phase 1
//   - CheckoutOptions.SparseCheckoutDirectories for phase 2
//     This could replace 6 sequential exec.Command calls with ~10 lines of typed Go.
//     Blockers to verify before migrating:
//     1. SparseCheckoutDirectories uses --no-cone mode (ox uses file-level patterns)
//     2. go-git network performance vs native git for clone
//     3. credential injection (URL-embedded tokens) works correctly
func TwoPhaseClone(ctx context.Context, cloneURL, repoPath string) (*TwoPhaseCloneResult, error) {
	// phase 1: minimal clone — trees only, no blobs.
	// Set cmd.Dir to the parent of repoPath so git doesn't fail with
	// "Unable to read current working directory" when the daemon's CWD
	// has been deleted (e.g. tmpdir cleanup on macOS).
	parentDir := filepath.Dir(repoPath)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return nil, fmt.Errorf("create parent dir %s: %w", parentDir, err)
	}
	// NewNetworkCmd sets GIT_TERMINAL_PROMPT=0 so a credential gap fails fast
	// with a clear error instead of EOFing on a username prompt in the
	// TTY-less daemon (and doctor fallback). Credentials are supplied via the
	// helper flags in phaseOneCloneArgs.
	cloneCmd := gitutil.NewNetworkCmd(ctx, phaseOneCloneArgs(cloneURL, repoPath)...)
	cloneCmd.Dir = parentDir
	if output, err := cloneCmd.CombinedOutput(); err != nil {
		sanitized := gitutil.SanitizeOutput(string(output))
		if sanitized != "" {
			return nil, fmt.Errorf("phase 1 clone failed: %s", sanitized)
		}
		return nil, fmt.Errorf("phase 1 clone failed: %w", err)
	}

	// Install the ox-managed credential helper into the fresh clone's
	// .git/config so the phase-2 `fetch --unshallow` below and all later
	// fetch/pull operations resolve credentials via the helper. The phase-1
	// clone above carries the helper via argv (the repo didn't exist yet);
	// from here on it lives in .git/config. Host-scoped to the clone host so
	// it never fires for unrelated remotes. Best-effort — file:// test clones
	// have no host and unshallow is non-fatal anyway.
	if host := cloneHost(cloneURL); host != "" {
		if err := InstallCredentialHelper(repoPath, HelperConfig{
			Host:    host,
			Command: DefaultHelperCommand(),
		}); err != nil {
			slog.Warn("two-phase clone: failed to install credential helper",
				"path", repoPath, "host", host, "error", err)
		}
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

	// ensure .sageox/ is always in the sparse set.
	// MUST be appended (not prepended) because ComputeSparseSet returns
	// patterns like ["/*", "!/*/", ...] where !/*/  excludes all root dirs.
	// In --no-cone mode (gitignore semantics), later patterns override earlier
	// ones, so .sageox/ must come AFTER !/*/ to re-include it.
	hasSageox := false
	for _, p := range sparsePaths {
		if p == ".sageox/" || p == ".sageox" {
			hasSageox = true
			break
		}
	}
	if !hasSageox {
		sparsePaths = append(sparsePaths, ".sageox/")
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
	unshallowArgs := append(gitutil.GitHTTPTimeoutFlags(), "fetch", "--unshallow", "--quiet")
	if _, err := gitutil.RunGit(ctx, repoPath, unshallowArgs...); err != nil {
		// non-fatal: pull --rebase may still work if remote only has 1 commit
		slog.Debug("unshallow fetch failed (may be single-commit repo)", "path", repoPath, "error", err)
	}

	// strip lfs config that git-lfs may have injected during clone
	gitutil.StripLFSConfig(repoPath)

	// ensure .sageox/.gitignore excludes daemon-written files (cache/, checkout.json, etc.)
	// so they don't appear as untracked and block blue-green GC reclone
	if err := EnsureCheckoutGitignoreCtx(ctx, repoPath); err != nil {
		return nil, fmt.Errorf("ensure checkout .gitignore: %w", err)
	}

	return &TwoPhaseCloneResult{
		ManifestConfig: cfg,
		SparsePaths:    sparsePaths,
	}, nil
}

// phaseOneCloneArgs builds the argv for the phase-1 partial clone.
//
// Credential-helper flags come first so git resolves the PAT via the
// ox-managed helper instead of prompting (the team-context clone is given a
// bare URL — no embedded userinfo). Then protocol hardening:
// `-c protocol.{file,ext}.allow=never` disables file:// and ext::sh://
// transports for this invocation, in case a malicious submodule or redirect
// tries to coerce git into a CVE-2017-1000117-class fetch. `--` terminates
// option parsing so a hostile URL starting with "-" is treated as a
// positional, not a flag.
//
// TestAllowFileTransport is a test-only override (see its godoc); ext://
// hardening is always applied — no production or test code legitimately
// needs it.
func phaseOneCloneArgs(cloneURL, repoPath string) []string {
	args := CredentialHelperArgs()
	args = append(args, gitutil.GitHTTPTimeoutFlags()...)
	if !TestAllowFileTransport {
		args = append(args, "-c", "protocol.file.allow=never")
	}
	args = append(args,
		"-c", "protocol.ext.allow=never",
		"clone",
		"--filter=blob:none",
		"--depth=1",
		"--sparse",
		"--no-checkout",
		"--single-branch",
		"--branch", "main",
		"--quiet",
		"--",
		cloneURL, repoPath,
	)
	return args
}

// cloneHost extracts the host from a clone URL for credential-helper scoping.
// Returns "" for URLs without an https host (e.g. file:// test clones), in
// which case no helper is installed.
func cloneHost(cloneURL string) string {
	u, err := url.Parse(cloneURL)
	if err != nil || u.Scheme != "https" {
		return ""
	}
	return u.Hostname()
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
