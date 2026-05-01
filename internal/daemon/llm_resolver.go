package daemon

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/ledger/automerge"
)

// llmMergeBinary returns the path to the LLM binary the daemon should
// use for tier-3 merges, or "" when no LLM tier should run server-side.
//
// Priority:
//  1. OX_DAEMON_LLM_MERGE_BIN — daemon-only setting so an operator can
//     enable LLM merges in the daemon process without the user's
//     CLI environment having to be configured.
//  2. OX_LLM_MERGE_BIN — shared with the CLI's session-upload
//     escalation. On a single-developer machine this is usually the
//     same binary.
//
// Empty when neither is set; the daemon then falls back to the
// surface-and-wait behavior and the CLI's
// session_upload.go::ledgerLLMResolveHook handles escalation if the
// user is the one driving ox at the moment.
func llmMergeBinary() string {
	if v := os.Getenv("OX_DAEMON_LLM_MERGE_BIN"); v != "" {
		return v
	}
	return os.Getenv("OX_LLM_MERGE_BIN")
}

// newDaemonLLMResolver builds the function the SyncScheduler injects
// into pullManagedRepo via ManagedRepoPullOpts.LLMResolver.
//
// Same automerge.Resolve semantics as the CLI's
// ledgerLLMResolveHook so behavior is consistent regardless of who
// triggered the resolution. Returns:
//
//   - (true,  nil)   on full resolution including rebase --continue
//   - (false, nil)   on best-effort no-op (ErrNoConflicts, or
//     ErrLLMUnavailable surfaces here when the configured binary
//     stopped working — caller falls through to abort)
//   - (false, err)   for unexpected failures
//
// We pass ledger.DefaultResolveRules-derived prefixes through SafePrefixes
// so the resolver knows which paths are eligible for accept-theirs
// before escalating to LLM. Empty SafeDenyPrefixes is fine — the
// resolver enforces deny via its own option, not via an empty allow.
func newDaemonLLMResolver(binary string, logger *slog.Logger) func(ctx context.Context, repoPath string, paths []string) (bool, error) {
	return func(ctx context.Context, repoPath string, paths []string) (bool, error) {
		r := automerge.New(automerge.Options{
			LLMBinary:    binary,
			SafePrefixes: ledger.AutoResolvePrefixes,
			Logger:       logger,
		})
		resolved, err := r.Resolve(ctx, repoPath)
		switch {
		case err == nil:
			return resolved, nil
		case errors.Is(err, automerge.ErrNoConflicts):
			// the conflict cleared between detection and resolve —
			// treat as success since there's nothing to do.
			return true, nil
		case errors.Is(err, automerge.ErrLLMUnavailable):
			// binary went away (deleted, permission flipped) since
			// startup. Log info, fall through to abort.
			logger.Info("automerge: llm tier unavailable mid-flight",
				"binary", binary, "paths", paths)
			return resolved, nil
		default:
			logger.Warn("automerge: daemon-side resolve failed",
				"paths", paths, "error", err)
			return resolved, err
		}
	}
}
