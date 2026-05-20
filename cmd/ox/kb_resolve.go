package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/kb"
)

// kb_resolve.go centralizes the "user-supplied --kb argument → kb_id" mapping
// used by every `ox kb <verb>` command. It mirrors the resolution rules
// implemented in kb_path.go (kb_* prefix → direct, otherwise → kb-API lookup
// with legacy fallback) so the surface area users see is uniform regardless
// of which subcommand they invoked.
//
// Why a shared helper rather than per-command inlining: when kb_config landed
// it accepted only kb_*-prefixed inputs, while kb_path accepted bare slugs
// and `#`-prefixed slugs. The divergence was a DX bug — users who learned
// `ox kb path #marketing` reasonably tried `ox kb config get … --kb=#marketing`
// and hit a hand-written rejection. Putting one resolver behind both commands
// is the only way to keep them in sync.

// errKBAPIUnreachable is the user-facing sentinel for "we tried to resolve a
// slug, but the kb API is unreachable (flag off, offline, 5xx, etc.)". Callers
// surface it with a help string that points at the kb_id escape hatch.
var errKBAPIUnreachable = errors.New("kb api unreachable")

// resolveKBInput maps a raw --kb argument (kb_id, bare slug, or "#slug") to a
// kb_id. The lookup matches kb_path semantics:
//
//   - kb_* prefix → returned as-is, no API round trip.
//   - otherwise → NormalizeSlugArg + kb-API ListBubbles, with legacy
//     team-context fallback when the API is unavailable.
//
// Returns errKBNotFound if no resolver/legacy entry matches, or
// errKBAPIUnreachable if every available path requires the kb API and the
// API itself failed in a way the legacy fallback couldn't paper over.
func resolveKBInput(ctx context.Context, raw string, deps kbPathDeps) (string, error) {
	normalized := strings.TrimSpace(kb.NormalizeSlugArg(raw))
	if normalized == "" {
		return "", fmt.Errorf("kb argument is required")
	}

	// kb_id direct: no API needed, no slug lookup, no legacy fallback. This
	// keeps `--kb=kb_xxx` fully offline, matching kb_path.go.
	if strings.HasPrefix(normalized, "kb_") {
		return normalized, nil
	}

	resolved, err := resolveKB(ctx, normalized, deps)
	if err == nil {
		return resolved.KBID, nil
	}
	if errors.Is(err, errKBNotFound) {
		return "", errKBNotFound
	}
	// resolveKB only surfaces non-not-found errors when the kb API failed
	// with something other than ErrKBAPIUnavailable AND the legacy fallback
	// didn't satisfy the lookup. From a user's perspective that's "API
	// unreachable for this slug" — give them the kb_id escape hatch.
	return "", fmt.Errorf("%w: %w", errKBAPIUnreachable, err)
}

// resolveKBInputForCmd is the convenience wrapper most callers want: derive
// the project root, build the default deps, apply a sensible timeout, and
// return the resolved kb_id. The deps are returned too so callers in test
// can override them via the lower-level resolveKBInput entry point if needed.
func resolveKBInputForCmd(parentCtx context.Context, raw string) (string, error) {
	projectRoot, _ := findProjectRoot()
	deps := newDefaultKBPathDeps(projectRoot)
	ctx, cancel := context.WithTimeout(parentCtx, 10*time.Second)
	defer cancel()
	return resolveKBInput(ctx, raw, deps)
}

// formatKBInputError converts a resolveKBInput error into the canonical
// stderr line(s) the user should see, returning the cli.ErrSilent sentinel
// so cobra doesn't print its own "Error: …" wrapper on top. `raw` is the
// original user-supplied argument so the message preserves their input form
// (e.g. they typed `marketing` → message says `#marketing`).
func formatKBInputError(raw string, err error) (string, error) {
	display := cli.FormatKBSlug(strings.TrimSpace(kb.NormalizeSlugArg(raw)))
	switch {
	case errors.Is(err, errKBNotFound):
		return fmt.Sprintf("kb not found: %s\n", display), cli.ErrSilent
	case errors.Is(err, errKBAPIUnreachable):
		// Help users out of the wedge: kb_id always works offline, so tell
		// them that. The "wait for next daemon sync" line nods to the fact
		// that the daemon eventually refreshes the local kb registry.
		return fmt.Sprintf(
			"cannot resolve slug %s — kb-API unavailable. Use --kb=kb_<id> directly, or wait for next daemon sync.\n",
			display,
		), cli.ErrSilent
	default:
		return fmt.Sprintf("kb config: %v\n", err), cli.ErrSilent
	}
}

// Compile-time sanity: api.ErrKBAPIUnavailable wrapping is handled by
// resolveKB; we only need this constant referenced somewhere in the package
// to make the linter happy if a future refactor stops using it. Removing
// this no-op reference is safe.
var _ = api.ErrKBAPIUnavailable
