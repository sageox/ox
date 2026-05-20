package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/gitserver"
	internalkb "github.com/sageox/ox/internal/kb"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/paths"
	"github.com/spf13/cobra"
)

// kb_hydrate.go — `ox kb hydrate <slug|kb_id> [path]`. On-demand hydration of
// LFS pointer files in a knowledge bubble's local checkout.
//
// Knowledge bubbles are cloned with the LFS smudge filter stripped (see
// gitserver.TwoPhaseClone), so pointer files survive on disk as plain text.
// This command is the user-facing escape hatch to materialize real content
// when needed (editing, exporting, building).
//
// Design constraints (see .claude/rules/lfs-no-git-lfs-binary.md):
//
//   - never shell out to `git-lfs`
//   - never write `.gitattributes` with filter=lfs
//   - daemon stays pointer-preserving; only this CLI command hydrates
//
// Hydration is one-way: pointer -> content. Re-pointer-ification is out of
// scope for V1.

var kbHydrateCmd = &cobra.Command{
	Use:   "hydrate <slug|id> [path]",
	Short: "Materialize LFS pointer files in a knowledge bubble",
	Long: `Materialize LFS pointer files in a knowledge bubble's local checkout.

Knowledge bubbles are cloned with the LFS smudge filter disabled, so pointer
files survive on disk as plain text. This command downloads the real content
via the LFS Batch API and replaces the pointer files in place.

Without a path, hydrates every pointer file in the bubble. With a path
(relative to the bubble root), hydrates just that file. Already-hydrated
files are skipped silently — running twice is safe and cheap.

Hydration is one-way: pointer to content. ox does not currently support
re-pointer-ifying a hydrated file.

Examples:
  ox kb hydrate platform                       # hydrate all pointers
  ox kb hydrate platform docs/big-asset.png    # hydrate one file
  ox kb hydrate kb_01HXYZ... --json            # JSON output`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runKBHydrate,
}

func init() {
	kbCmd.AddCommand(kbHydrateCmd)
	kbHydrateCmd.Flags().Bool("json", false, "Emit machine-readable JSON")
}

// kbHydrateOutput is the JSON envelope. Hydrated lists relative paths
// (forward-slash, repo-root-relative). Failed lists per-file failures
// the user should know about. Pinned via snapshot test in
// kb_hydrate_test.go so accidental shape changes break a test.
type kbHydrateOutput struct {
	Slug     string             `json:"slug"`
	KBID     string             `json:"kb_id"`
	Hydrated []string           `json:"hydrated"`
	Failed   []kbHydrateFailure `json:"failed,omitempty"`
}

type kbHydrateFailure struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// kbHydrateDeps is the test seam. Production wiring goes through the live
// kb API client and lfs.NewClient for the bubble's git remote URL; tests
// inject fakes so they never need network access.
type kbHydrateDeps struct {
	resolver      kbResolver
	pathFor       func(kbID string) string
	endpointForKB func(projectRoot string) string
	loadCreds     func(endpointURL string) (*gitserver.GitCredentials, error)
	bubbleRemote  func(bubbleDir string) (string, error)
	newLFSClient  func(repoURL, username, token string) *lfs.Client
	hydrateBubble func(ctx context.Context, dir, relPath string, c *lfs.Client, filter lfs.HydrationFilter) (*lfs.HydrateResult, error)
	resolveBubble func(ctx context.Context, query string, deps kbHydrateDeps) (resolvedKB, string, error)
}

func newDefaultKBHydrateDeps(projectRoot string) kbHydrateDeps {
	client := api.NewKBClientForProject(projectRoot)
	if token, err := auth.GetTokenForEndpoint(client.Endpoint()); err == nil && token != nil && token.AccessToken != "" {
		client = client.WithAuthToken(token.AccessToken)
	}
	return kbHydrateDeps{
		resolver:      client,
		pathFor:       paths.KBDir,
		endpointForKB: endpoint.GetForProject,
		loadCreds:     gitserver.LoadCredentialsForEndpoint,
		bubbleRemote:  gitserver.GetBareRemoteURL,
		newLFSClient:  lfs.NewClient,
		hydrateBubble: lfs.HydrateBubble,
		resolveBubble: defaultResolveBubble,
	}
}

// defaultResolveBubble maps a slug-or-id query to a (kb_id, slug) pair using
// the same resolution priority as `ox kb show`. Slug is preserved (when known)
// for the JSON output; if the user passed a kb_id directly, slug is empty.
func defaultResolveBubble(ctx context.Context, query string, deps kbHydrateDeps) (resolvedKB, string, error) {
	if hasKBIDPrefix(query) {
		// kb_id direct path. We don't even need the API for this — the user
		// will see slug="" in the JSON, which is correct (we don't know it
		// without an API call, and that would defeat the offline path).
		return resolvedKB{KBID: query}, "", nil
	}
	if deps.resolver == nil {
		return resolvedKB{}, "", errKBNotFound
	}
	bubbles, err := deps.resolver.ListBubbles(ctx)
	if err != nil {
		if errors.Is(err, api.ErrKBAPIUnavailable) {
			return resolvedKB{}, "", errKBNotFound
		}
		return resolvedKB{}, "", err
	}
	if kb := pickBubbleBySlug(bubbles, query); kb != nil {
		return resolvedKB{KBID: kb.KBID}, kb.Slug, nil
	}
	return resolvedKB{}, "", errKBNotFound
}

func hasKBIDPrefix(s string) bool {
	const prefix = "kb_"
	return len(s) > len(prefix) && s[:len(prefix)] == prefix
}

func runKBHydrate(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")

	projectRoot, _ := findProjectRoot()
	deps := newDefaultKBHydrateDeps(projectRoot)

	relPath := ""
	if len(args) == 2 {
		relPath = args[1]
	}
	// Strip the optional human-display `#` prefix from the slug-or-id arg
	// before handing it to the resolver; slugs are looked up bare.
	query := internalkb.NormalizeSlugArg(args[0])
	return runKBHydrateWithDeps(cmd, query, relPath, jsonMode, projectRoot, deps)
}

// runKBHydrateWithDeps is the dependency-injected core. Returns cli.ErrSilent
// for user-facing failures (the message is already on stderr) and bubbles up
// real errors otherwise.
func runKBHydrateWithDeps(cmd *cobra.Command, query, relPath string, jsonMode bool, projectRoot string, deps kbHydrateDeps) error {
	stdout := cmd.OutOrStdout()
	stderr := cmd.ErrOrStderr()

	ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
	defer cancel()

	kb, slug, err := deps.resolveBubble(ctx, query, deps)
	if err != nil {
		if errors.Is(err, errKBNotFound) {
			display := query
			if !hasKBIDPrefix(query) {
				display = cli.FormatKBSlug(query)
			}
			fmt.Fprintf(stderr, "kb not found: %s\n", display)
			return cli.ErrSilent
		}
		fmt.Fprintf(stderr, "kb hydrate: %v\n", err)
		return cli.ErrSilent
	}

	bubbleDir := deps.pathFor(kb.KBID)
	if bubbleDir == "" {
		fmt.Fprintf(stderr, "kb hydrate: could not resolve canonical path for kb_id=%s\n", kb.KBID)
		return cli.ErrSilent
	}
	if _, err := os.Stat(bubbleDir); err != nil {
		fmt.Fprintf(stderr, "kb hydrate: bubble checkout missing at %s — run `ox sync` first\n", bubbleDir)
		return cli.ErrSilent
	}

	// Resolve auth + LFS endpoint. Same path session_upload uses: PAT from
	// gitserver.LoadCredentialsForEndpoint, repo URL from the bubble's local
	// git remote (so it tolerates server-side URL changes between syncs).
	ep := deps.endpointForKB(projectRoot)
	creds, err := deps.loadCreds(ep)
	if err != nil {
		fmt.Fprintf(stderr, "kb hydrate: load credentials: %v\n", err)
		return cli.ErrSilent
	}
	if creds == nil || creds.Token == "" {
		fmt.Fprintln(stderr, "kb hydrate: no git credentials found — run `ox login` first")
		return cli.ErrSilent
	}
	repoURL, err := deps.bubbleRemote(bubbleDir)
	if err != nil {
		fmt.Fprintf(stderr, "kb hydrate: get bubble remote URL: %v\n", err)
		return cli.ErrSilent
	}
	if repoURL == "" {
		fmt.Fprintf(stderr, "kb hydrate: bubble at %s has no remote — run `ox sync` first\n", bubbleDir)
		return cli.ErrSilent
	}

	client := deps.newLFSClient(repoURL, creds.Username, creds.Token)

	// Per-bubble file lock so a concurrent daemon pull (or another `ox kb
	// hydrate`) doesn't race the rename. Lock file lives in the OS tmpdir
	// keyed by the bubble dir hash (see fileutil.LockPath).
	lockTarget := filepath.Join(bubbleDir, ".sageox", "kb-hydrate.lock-target")
	var hyd *lfs.HydrateResult
	lockErr := fileutil.WithFileLock(ctx, lockTarget, func() error {
		var hErr error
		hyd, hErr = deps.hydrateBubble(ctx, bubbleDir, relPath, client, defaultHydrateFilter)
		return hErr
	})
	if lockErr != nil {
		fmt.Fprintf(stderr, "kb hydrate: %v\n", lockErr)
		return cli.ErrSilent
	}

	// Build output. We emit JSON or human regardless of whether anything
	// failed — the user-visible cue is the failed list (and the exit code).
	failures := make([]kbHydrateFailure, 0, len(hyd.Failed)+len(hyd.Skipped))
	for _, f := range hyd.Failed {
		failures = append(failures, kbHydrateFailure{Path: f.Path, Error: f.Err.Error()})
	}
	for _, f := range hyd.Skipped {
		failures = append(failures, kbHydrateFailure{Path: f.Path, Error: f.Err.Error()})
	}

	out := kbHydrateOutput{
		Slug:     slug,
		KBID:     kb.KBID,
		Hydrated: hyd.Hydrated,
		Failed:   failures,
	}
	if out.Hydrated == nil {
		out.Hydrated = []string{}
	}

	if jsonMode {
		if err := writeKBHydrateJSON(stdout, out); err != nil {
			return err
		}
	} else {
		writeKBHydrateHuman(stdout, stderr, query, slug, out)
	}

	slog.Info("kb_lfs_hydrate", "kb_id", kb.KBID, "hydrated", len(out.Hydrated), "failed", len(out.Failed), "scanned", hyd.Scanned)

	// Exit code policy:
	//   - any hydrated  -> 0 (partial progress is success)
	//   - all attempted failed -> non-zero so scripts notice
	//   - nothing to do (no pointers) -> 0
	if len(out.Hydrated) == 0 && len(out.Failed) > 0 {
		return cli.ErrSilent
	}
	return nil
}

// defaultHydrateFilter excludes paths that are categorically not user data:
// the .git directory (already excluded by the walker), .sageox/ control
// files, and lock-target sidecars we plant ourselves. Other paths are
// inspected by the LFS pointer detector.
func defaultHydrateFilter(relPath string) bool {
	if relPath == "" {
		return true
	}
	switch {
	case relPath == ".sageox" || hasPathPrefix(relPath, ".sageox/"):
		return false
	case relPath == ".git" || hasPathPrefix(relPath, ".git/"):
		return false
	}
	return true
}

func hasPathPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func writeKBHydrateJSON(w io.Writer, out kbHydrateOutput) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	return nil
}

func writeKBHydrateHuman(stdout, stderr io.Writer, query, slug string, out kbHydrateOutput) {
	// Resolved slug wins over the user's original query for display. kb_id
	// inputs (no resolvable slug) show bare; slug inputs get the human-
	// display `#` prefix.
	display := slug
	if display == "" {
		display = query
	}
	if !hasKBIDPrefix(display) {
		display = cli.FormatKBSlug(display)
	}
	if len(out.Hydrated) == 0 && len(out.Failed) == 0 {
		fmt.Fprintf(stdout, "Nothing to hydrate in %s\n", display)
		return
	}
	fmt.Fprintf(stdout, "Hydrated %d file(s) in %s\n", len(out.Hydrated), display)
	if len(out.Failed) > 0 {
		fmt.Fprintf(stderr, "%d file(s) failed:\n", len(out.Failed))
		for _, f := range out.Failed {
			fmt.Fprintf(stderr, "  %s: %s\n", f.Path, f.Error)
		}
	}
}
