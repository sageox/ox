package main

import (
	"fmt"
	"path/filepath"

	"github.com/sageox/ox/internal/lfs"
)

// openSessionContent is the canonical entrypoint for reading hydrated session
// content out of the ledger. It returns a path that holds REAL bytes (not an
// LFS pointer stub), hydrating on demand to the ledger cache when neither
// the cache nor the in-place file has real content.
//
// # CACHE-ONLY DESIGN — load-bearing invariant
//
// The git-tracked file at <ledger>/sessions/<name>/<filename> MUST stay as
// an LFS pointer for any session synced from the ledger. Hydrated content
// lives at <ledger>/.sageox/cache/sessions/<name>/<filename>, which is
// gitignored. Any reader that ignores this contract and writes hydrated
// bytes to the in-place path produces two failure modes:
//
//  1. commitAndPushLedger globs *.jsonl/*.html/*.md inside the session dir
//     and stages whatever is there. A hydrated in-place raw.jsonl gets
//     committed as a regular git blob, replacing the LFS pointer reference
//     and breaking LFS linkage. The ledger then rejects future pushes for
//     any session whose meta.json references the now-orphaned OID.
//
//  2. The daemon's session-finalize anti-entropy skips sessions whose
//     content IS still a pointer (internal/daemon/agentwork/session_finalize.go:306).
//     When in-place is full content, the skip doesn't apply and the daemon
//     can re-finalize already-finalized sessions, race with concurrent CLI
//     work, and clobber good summaries with failure-marker stubs.
//
// Both failures were observed in the 2026-04-25 Phase 2 batch:
// 31 of 71 freshly-regen'd summaries got clobbered, and 2 sessions had
// raw.jsonl committed as full git blobs. See bd ox-4ncz post-mortem.
//
// All readers (regenerate, view, lint, token-optimize, distill, etc.)
// MUST go through this function — they should never call
// session.ReadSessionFromPath against an in-place ledger path directly,
// and they should never write to the in-place path either.
//
// Returns:
//   - resolved path (cache OR in-place real content; never a pointer file)
//   - error if hydration is required but fails
func openSessionContent(projectRoot, ledgerPath, sessionName, filename string) (string, error) {
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	cacheDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", sessionName)

	if p := lfs.ResolveContentPath(sessionDir, cacheDir, filename); p != "" {
		return p, nil
	}

	// Need to hydrate. Cache-only — never write to sessionDir.
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	if err := hydrateFromLedger(projectRoot, sessionsDir, sessionName, true /*quiet*/); err != nil {
		return "", fmt.Errorf("hydrate %s for %s: %w", filename, sessionName, err)
	}

	if p := lfs.ResolveContentPath(sessionDir, cacheDir, filename); p != "" {
		return p, nil
	}
	return "", fmt.Errorf("hydration completed but %s for %s still not resolvable (manifest may be missing the file)",
		filename, sessionName)
}
