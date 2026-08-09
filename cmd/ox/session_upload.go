// session_upload.go handles uploading session artifacts to the ledger.
//
// AUTH MODEL: Upload uses Git PAT only (no OAuth required).
//   - LFS upload: PAT via HTTP Basic auth (getLFSClient)
//   - git push: PAT embedded in remote URL (RefreshRemoteCredentials)
//   - checkUploadAccess: OAuth-based, fail-open, kept for viewer detection only
//
// See docs/specs/session-auth-model.md for the full auth model.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/kb"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/ledger/automerge"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/perf"
)

// gcSwapWaitBound is how long the CLI will wait for a daemon-side blue-green
// GC rename-swap to clear before proceeding anyway. The swap itself
// (internal/daemon/sync_gc.go) is a couple of os.Rename calls plus removing
// the old clone — near-instant even for a large ledger — so this bounds a
// genuine wait to something that shouldn't be user-visible, without risking
// an indefinite hang if the marker is ever left behind by a daemon crash
// mid-swap (acquireGCLock's own 5-minute stale-lock reclaim is the backstop
// for that; this constant is unrelated and much shorter on purpose).
const (
	gcSwapWaitBound    = 10 * time.Second
	gcSwapPollInterval = 100 * time.Millisecond
)

// waitForGCSwap gives a daemon-side blue-green GC rename-swap a bounded
// chance to finish before the CLI writes directly to ledgerPath.
//
// ledgerMu (internal/daemon/sync_gc.go) serializes the daemon's own pull
// cycle against its GC swap, but it's an in-process sync.Mutex — it cannot
// serialize against the CLI, which runs as a separate OS process
// (.claude/rules/daemon-git.md's daemon-reads/CLI-writes split). Without
// this check, a commitAndPushLedger racing the exact rename-swap window can
// have its writes silently land in what becomes the old clone, which the
// daemon then deletes — a lost commit with no error surfaced anywhere.
//
// ".gc-swap-lock" is written by the daemon immediately before the rename
// and removed immediately after the old clone is cleaned up — a much
// narrower, cross-process-safe signal than the coarse ".gc-lock" that spans
// the daemon's entire (potentially multi-minute) reclone, so this wait
// should resolve in well under a second in the common case. Best-effort:
// if the marker is still present after gcSwapWaitBound, log and proceed
// anyway rather than risk an indefinite hang — this narrows the race window
// close to zero without giving the CLI a new way to hang forever.
func waitForGCSwap(ledgerPath string) {
	waitForGCSwapWithBound(ledgerPath, gcSwapWaitBound, gcSwapPollInterval)
}

// waitForGCSwapWithBound is waitForGCSwap with an injectable bound/poll
// interval so tests can exercise the full wait/timeout behavior without
// blocking for the production 10s bound.
func waitForGCSwapWithBound(ledgerPath string, bound, pollInterval time.Duration) {
	lockPath := ledgerPath + ".gc-swap-lock"
	if _, err := os.Stat(lockPath); err != nil {
		return // common case: no swap in flight
	}

	deadline := time.Now().Add(bound)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(lockPath); err != nil {
			return
		}
		time.Sleep(pollInterval)
	}
	slog.Warn("proceeding with ledger write despite an in-progress GC swap marker",
		"ledger", ledgerPath, "waited", bound)
}

// checkUploadAccess checks if the user has write access to upload sessions.
// Returns api.ErrReadOnly if the user is a viewer on a public repo.
// Returns nil if the user has write access or if access cannot be determined (fail-open).
func checkUploadAccess(projectRoot string) error {
	repoID := config.GetRepoID(projectRoot)
	if repoID == "" {
		return nil
	}

	ep := endpoint.GetForProject(projectRoot)
	token, err := auth.GetTokenForEndpoint(ep)
	if err != nil || token == nil || token.AccessToken == "" {
		return nil // fail-open: can't determine access without auth
	}

	client := api.NewRepoClientWithEndpoint(ep).WithAuthToken(token.AccessToken)

	// try the detailed repo endpoint first
	detail, err := client.GetRepoDetail(repoID)
	if err == nil && detail != nil {
		if detail.IsReadOnly() {
			return api.ErrReadOnly
		}
		return nil
	}

	// fall back to ledger status if GetRepoDetail returned 404 (nil, nil) or errored
	if detail == nil && err == nil {
		status, statusErr := client.GetLedgerStatus(repoID)
		if statusErr == nil && status != nil && status.IsReadOnly() {
			return api.ErrReadOnly
		}
	}

	return nil // fail-open on any error
}

// uploadSessionLFS uploads session content files to LFS blob storage
// and returns the file->FileRef manifest for inclusion in meta.json.
//
// No OAuth needed — LFS upload uses the Git PAT (HTTP Basic auth).
// Access control is enforced at push time by the PAT, not by a pre-check.
func uploadSessionLFS(projectRoot, sessionPath string) (map[string]lfs.FileRef, error) {
	client, err := getLFSClient(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("create LFS client: %w", err)
	}

	return lfs.UploadSessionFiles(client, sessionPath, slog.Default())
}

// getLFSClient creates an LFS client using project credentials.
// Derives the LFS batch URL from the ledger's local git remote, avoiding any
// dependency on the OAuth API token. Only the Git PAT is needed for LFS auth.
func getLFSClient(projectRoot string) (*lfs.Client, error) {
	ep := endpoint.GetForProject(projectRoot)

	// load git credentials (PAT) for LFS HTTP Basic auth
	creds, err := gitserver.LoadCredentialsForEndpoint(ep)
	if err != nil {
		return nil, fmt.Errorf("load credentials: %w", err)
	}
	if creds == nil {
		return nil, fmt.Errorf("no git credentials found (run 'ox login' first)")
	}
	if creds.Token == "" {
		return nil, fmt.Errorf("git credentials have empty token")
	}

	// derive LFS repo URL from the ledger's local git remote (no API call needed)
	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		return nil, fmt.Errorf("resolve ledger: %w", err)
	}

	repoURL, err := gitserver.GetBareRemoteURL(ledgerPath)
	if err != nil {
		return nil, fmt.Errorf("get ledger remote URL: %w", err)
	}
	if repoURL == "" {
		return nil, fmt.Errorf("ledger has no remote URL configured")
	}

	return lfs.NewClient(repoURL, creds.Username, creds.Token), nil
}

// ensureSessionsGitignore delegates to lfs.EnsureSessionsGitignore.
func ensureSessionsGitignore(sessionsDir string) error {
	return lfs.EnsureSessionsGitignore(sessionsDir)
}

// commitAndPushLedger commits meta.json and .gitignore, then pushes to remote.
// Uses pull --rebase with retry to handle concurrent pushes from other team members.
// NEVER uses --force push. Conflicts are resolved via pull --rebase.
//
// # File staging strategy (manifest-driven, with glob fallback)
//
// The set of session-artifact files to stage is derived from meta.Files
// (the canonical manifest in meta.json). Each entry — LFS pointer file or
// git-stored artifact like summary.json — is staged uniformly. This makes
// meta.Files the single source of truth: adding a new session artifact is
// one entry in the manifest, no code drift across upload, commit, redact,
// and doctor paths.
//
// When meta.json is unreadable or its Files map is empty (very legacy
// sessions, or a torn write), we fall back to the historical glob
// (*.jsonl/*.html/*.md). The fallback is the original behavior; it
// preserves backwards compatibility with sessions that predate the
// Storage-tagged manifest. summary.json is also opportunistically staged
// in the fallback path so the redact flow always carries forward.
//
// Uses exec.Command("git") rather than go-git for the same reasons as the daemon
// (see daemon/sync.go doPull): rebase support, process isolation, and lock safety.
// This is a low-volume path (once per session stop), so subprocess overhead is negligible.
func commitAndPushLedger(ledgerPath, sessionName string) error {
	waitForGCSwap(ledgerPath)

	// ensure .gitignore is in place before any commit to prevent cache file leakage
	gitserver.EnsureGitignoreBeforeCommit(ledgerPath)

	// stage meta.json and .gitignore
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	sessionDir := filepath.Join(sessionsDir, sessionName)

	metaPath := filepath.Join(sessionDir, "meta.json")
	gitignorePath := filepath.Join(sessionsDir, ".gitignore")

	filesToAdd := append([]string{metaPath, gitignorePath}, sessionArtifactsToStage(sessionDir)...)

	// --sparse: ledger repos use sparse-checkout (cone mode); this flag
	// prevents git from blocking adds if sparse rules change or edge cases arise
	addArgs := append([]string{"-C", ledgerPath, "add", "--sparse"}, filesToAdd...)
	addCmd := exec.Command("git", addArgs...)
	if output, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add failed: %s: %w", string(output), err)
	}

	// commit
	commitMsg := fmt.Sprintf("session: %s", sessionName)
	commitCmd := exec.Command("git", "-C", ledgerPath, "commit", "--no-verify", "-m", commitMsg)
	if output, err := commitCmd.CombinedOutput(); err != nil {
		// check if nothing to commit
		if strings.Contains(string(output), "nothing to commit") {
			return nil
		}
		return fmt.Errorf("%s: %w", wrapCommitError(string(output), err), err)
	}

	// push with pull --rebase retry (up to 3 attempts)
	return pushLedger(context.Background(), ledgerPath)
}

// commitPointerRewriteAndPush commits the post-upload LFS pointer rewrite and
// pushes it. Called immediately after lfs.WritePointerFiles in the session-stop
// pipeline to close the dirty-worktree window that would otherwise race against
// the daemon's sync-timer pull (`git pull --rebase --autostash`).
//
// Stages only the explicit pointer paths returned by WritePointerFiles — does
// not re-glob the manifest — so any concurrent unrelated change to the session
// dir between the initial commit and this one cannot be silently folded into
// the "lfs: pointerize <name>" commit.
//
// Returns nil if pointerPaths is empty (nothing to commit) or if git reports
// "nothing to commit" (idempotent).
func commitPointerRewriteAndPush(ledgerPath, sessionName string, pointerPaths []string) error {
	if len(pointerPaths) == 0 {
		return nil
	}

	waitForGCSwap(ledgerPath)

	// --sparse: ledger repos use sparse-checkout (cone mode)
	addArgs := append([]string{"-C", ledgerPath, "add", "--sparse"}, pointerPaths...)
	addCmd := exec.Command("git", addArgs...)
	if output, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add failed: %s: %w", string(output), err)
	}

	// commit under a distinct subject so it doesn't shadow the canonical
	// "session: <name>" commit in git log
	commitMsg := fmt.Sprintf("lfs: pointerize %s", sessionName)
	commitCmd := exec.Command("git", "-C", ledgerPath, "commit", "--no-verify", "-m", commitMsg)
	if output, err := commitCmd.CombinedOutput(); err != nil {
		if strings.Contains(string(output), "nothing to commit") {
			return nil
		}
		return fmt.Errorf("%s: %w", wrapCommitError(string(output), err), err)
	}

	// push with pull --rebase retry. If the remote has independently modified
	// the same path (the very race this guards against), PushWithRetry will
	// abort the rebase cleanly (internal/gitutil/push.go:191) — no conflict
	// markers can survive into a commit. Caller (slog.Warn) handles the error.
	return pushLedger(context.Background(), ledgerPath)
}

// commitAndPushLedgerWithExtras is a thin compatibility shim around
// commitAndPushLedger, retained for the doctor retry path. Pre-manifest,
// commitAndPushLedger only globbed *.jsonl/*.html/*.md and missed
// summary.json; this helper existed to opt summary.json in. With the
// manifest-driven staging in commitAndPushLedger, summary.json is now
// staged automatically whenever meta.Files records it (which push-summary
// always does). The includeSummary flag is therefore a no-op for new
// sessions; it remains accepted for old call sites and to belt-and-
// suspender any session whose meta.json hasn't been updated yet.
func commitAndPushLedgerWithExtras(ledgerPath, sessionName string, includeSummary bool) error {
	if includeSummary {
		// best-effort: ensure summary.json is registered in the manifest
		// so commitAndPushLedger picks it up even on legacy meta.json.
		sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
		summaryPath := filepath.Join(sessionDir, "summary.json")
		if info, err := os.Stat(summaryPath); err == nil {
			_ = backfillGitArtifactInMeta(sessionDir, "summary.json", info.Size())
		}
	}
	return commitAndPushLedger(ledgerPath, sessionName)
}

// sessionArtifactsToStage returns the set of session-artifact files (LFS
// pointer files + git-stored artifacts like summary.json) that should be
// staged for commit, derived from meta.Files. Falls back to the historical
// glob (*.jsonl/*.html/*.md) plus opportunistic summary.json when
// meta.json is missing, unreadable, or its Files map is empty — this
// preserves backwards compat with very-legacy sessions and any torn-write
// recovery scenario.
//
// All returned paths are absolute (joined with sessionDir).
func sessionArtifactsToStage(sessionDir string) []string {
	meta, err := lfs.ReadSessionMeta(sessionDir)
	// A draft placeholder contains ONLY meta.json, which every caller stages
	// explicitly. Returning early matters because a draft's Files map is
	// legitimately empty, which would otherwise fall through to the glob
	// fallback below and stage any *.jsonl / *.md in the directory — including
	// a server-authored summary.md, or real raw.jsonl bytes committed as a git
	// blob, which breaks LFS linkage for every future push
	// (.claude/rules/cache-only-design.md).
	if err == nil && meta.IsDraft() {
		return nil
	}
	if err == nil && len(meta.Files) > 0 {
		paths := make([]string, 0, len(meta.Files))
		for name := range meta.Files {
			paths = append(paths, filepath.Join(sessionDir, name))
		}
		return paths
	}

	// fallback: glob the historical artifact extensions and opportunistically
	// pick up summary.json if it exists. Used when meta.json is unreadable
	// or the manifest is empty.
	var paths []string
	for _, pattern := range []string{"*.jsonl", "*.html", "*.md"} {
		matches, _ := filepath.Glob(filepath.Join(sessionDir, pattern))
		paths = append(paths, matches...)
	}
	if summaryPath := filepath.Join(sessionDir, "summary.json"); fileExists(summaryPath) {
		paths = append(paths, summaryPath)
	}
	return paths
}

// backfillGitArtifactInMeta records a git-stored artifact in meta.Files
// for legacy sessions whose manifest predates the Storage tag. Idempotent
// and best-effort; failures are logged at debug only.
//
// Runs the entire read-modify-write under MutateSessionMeta's flock so
// the daemon's concurrent summary write doesn't clobber this artifact
// entry (and vice versa). See ox-e1ot for the failure mode this
// prevents.
func backfillGitArtifactInMeta(sessionDir, filename string, size int64) bool {
	changed := false
	err := lfs.MutateSessionMeta(context.Background(), sessionDir, func(meta *lfs.SessionMeta) (*lfs.SessionMeta, error) {
		if meta == nil {
			return nil, nil // no meta.json yet — skip silently (legacy callers expect best-effort)
		}
		if meta.Files == nil {
			meta.Files = make(map[string]lfs.FileRef)
		}
		if existing, ok := meta.Files[filename]; ok && existing.IsGit() && existing.Size == size {
			return nil, nil // already canonical
		}
		meta.Files[filename] = lfs.NewGitFileRef(size)
		changed = true
		return meta, nil
	})
	if err != nil {
		slog.Debug("backfillGitArtifactInMeta: write meta.json failed", "session", sessionDir, "error", err)
		return false
	}
	return changed
}

// resolveLedgerPath returns the ledger git repo path for the project.
// Uses the existing getLedgerPath() helper, wrapping its result for error handling.
func resolveLedgerPath() (string, error) {
	path := getLedgerPath()
	if path == "" {
		return "", fmt.Errorf("no ledger path found (run 'ox doctor --fix' or wait for daemon to clone)")
	}

	// verify ledger exists on disk
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", fmt.Errorf("ledger not found at %s (run 'ox doctor --fix')", path)
	}

	return path, nil
}

// ledgerAutoResolvePrefixes aliases the canonical list from internal/ledger
// for use by the CLI push path.
var ledgerAutoResolvePrefixes = ledger.AutoResolvePrefixes

// pushLedger pushes ledger changes to remote with conflict retry.
// Delegates to gitutil.PushWithRetry with ledger-appropriate options:
// LFS reconciliation, rebase on conflict, auto-resolve for data/github/.
//
// Per-phase timing is captured as nested spans so OX_TRACE=1 / -v shows
// which segment of the push dominates — the secret scan, credential
// refresh, the push itself, or LFS reconcile.
func pushLedger(ctx context.Context, ledgerPath string) error {
	ctx, span := perf.Start(ctx, "pre-push total")
	defer span.End()

	// resolve endpoint once, before entering the push loop.
	// only refresh credentials when we have a real project root —
	// GetForProject("") falls back to Default, which would inject
	// production credentials into a local file:// remote URL.
	// findGitRoot() is CWD-dependent — if the caller isn't in a git repo
	// (e.g., doctor retry from a different dir), this silently returns "".
	_, settingsSpan := perf.Start(ctx, "resolve_push_settings")
	var ep string
	if root := findGitRoot(); root != "" {
		ep = endpoint.GetForProject(root)
	} else {
		slog.Warn("pushLedger: no git root found, credential refresh will be skipped")
	}

	// pre-flight: heal ledger merge=union rules in .git/info/attributes so
	// multi-writer merges work even on ledgers initialized with older CLI
	// versions that lacked this. Per-clone, never enters the working tree.
	// Best-effort: failure here is a degraded mode, not a push failure.
	if changed, err := kb.EnsureMergeAttributes(ledgerPath); err != nil {
		slog.Warn("pushLedger: ensure kb merge attributes failed", "error", err)
	} else if changed {
		slog.Info("healed kb merge attributes", "ledger", ledgerPath)
	}
	settingsSpan.End()

	// Pre-push secret gate (ox-1uss). Scans the commit range that we're about
	// to push for known credential patterns; refuses the push if any are found
	// unless the user has set OX_ALLOW_SECRETS=1. Runs AFTER credential
	// refresh / merge-attribute healing so failures from those don't get
	// confused with a secret-gate refusal; runs BEFORE PushWithRetry so we
	// never send bytes containing detected credentials.
	gateCtx, gateSpan := perf.Start(ctx, "secret_gate")
	if err := runPrePushSecretGate(gateCtx, ledgerPath); err != nil {
		perf.RecordError(gateSpan, err)
		gateSpan.End()
		perf.RecordError(span, err)
		return err
	}
	gateSpan.End()

	pushCtx, pushSpan := perf.Start(ctx, "git_push")
	err := gitutil.PushWithRetry(pushCtx, ledgerPath, gitutil.PushOpts{
		AutoResolvePrefixes: ledgerAutoResolvePrefixes,
		PrePush: func(repoPath string) error {
			if ep != "" {
				if err := gitserver.RefreshRemoteCredentials(repoPath, ep); err != nil {
					return fmt.Errorf("credential refresh: %w", err)
				}
			}
			return nil
		},
		ReconcileLFS:          makeLFSReconciler(ep),
		OnUnresolvedConflicts: ledgerLLMResolveHook(),
	})
	if err != nil {
		perf.RecordError(pushSpan, err)
		perf.RecordError(span, err)
	}
	pushSpan.End()
	return err
}

// ledgerLLMResolveHook returns the OnUnresolvedConflicts callback used by
// pushLedger when accept-theirs auto-resolution does not cover every
// conflicted path. Tries the automerge resolver's tiered strategy:
// in-code union, then LLM merge if a model binary is configured.
//
// LLM tier is opt-in via the OX_LLM_MERGE_BIN env var (path to a `claude` /
// `codex` / `gemini` binary). When unset, the hook still runs the in-code
// union tier and reports back; LLM is skipped silently.
func ledgerLLMResolveHook() func(ctx context.Context, repoPath string, paths []string) (bool, error) {
	return func(ctx context.Context, repoPath string, paths []string) (bool, error) {
		llmBin := os.Getenv("OX_LLM_MERGE_BIN")
		r := automerge.New(automerge.Options{
			LLMBinary:    llmBin,
			SafePrefixes: ledgerAutoResolvePrefixes,
			Logger:       slog.Default(),
		})
		resolved, err := r.Resolve(ctx, repoPath)
		switch {
		case err == nil:
			return resolved, nil
		case errors.Is(err, automerge.ErrNoConflicts):
			// nothing to do == nothing to fail. PushWithRetry's caller
			// already saw the conflict; if automerge can't see one anymore
			// it means another tier (or git itself) handled it.
			return true, nil
		case errors.Is(err, automerge.ErrLLMUnavailable):
			// expected when no LLM binary is configured. Lower-tier
			// resolution already ran inside Resolve; we just couldn't
			// escalate further. Surface as info, not warn.
			slog.Info("automerge: llm tier skipped", "reason", "binary not configured", "paths", paths)
			return resolved, nil
		default:
			slog.Warn("automerge: resolve failed", "paths", paths, "error", err)
			return resolved, err
		}
	}
}

// makeLFSReconciler returns a ReconcileLFS callback that strips orphaned LFS
// pointer stubs and squashes unpushed history so the push can succeed.
// Returns nil (no reconciliation) if no endpoint is available.
func makeLFSReconciler(ep string) func(string) (bool, error) {
	if ep == "" {
		return nil
	}
	return func(repoPath string) (bool, error) {
		result, err := lfs.ReconcileUnpushedPointers(
			context.Background(), repoPath, ep, slog.Default())
		if err != nil {
			return false, err
		}
		return result.Replaced > 0, nil
	}
}
