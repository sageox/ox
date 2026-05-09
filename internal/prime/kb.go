package prime

// kb.go — converts an internal/kb.MergeResult into the []KBInfo envelope
// emitted by `ox agent prime`. Pure function so the conversion + sort + the
// "personal bubble must always appear" guarantee can be unit-tested without
// standing up the merger or its three live sources.

import (
	"log/slog"
	"sort"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/kb"
)

// hint strings for KBInfo.Hint, kept short on purpose — the rich payload
// still flows via the deprecated TeamContext mirror until the per-type split
// lands. These are agent-facing prose; quote command names for scannability.
const (
	hintPersonal = "your personal scratchpad — your own notes/decisions across all repos"
	hintProfile  = "your public profile bubble"
	hintTeam     = "team-wide decisions/conventions; read with 'ox agent team-ctx'"
	hintRepoSyn  = "ledger archive — read on demand"
	hintCustom   = "custom knowledge bubble"
)

// BuildKBInfos converts a merger result into the prime KB envelope.
//
// tokensByType is consulted to populate KBInfo.Tokens — typically sourced
// from the daemon's per-kb-type cumulative counters (or the prime per-source
// budget for the freshly-emitted content). Tokens are split equally across
// bubbles of the same type when there are multiple; this matches the rolled-
// up shape of the heartbeat counter and avoids fabricating per-bubble
// numbers the daemon doesn't actually track today. Pass a nil map when no
// token data is available — Tokens will be left at zero.
//
// Sort order matches `ox kb list`: type-priority, then non-legacy before
// legacy within a type, then slug. Stable so test snapshots don't churn.
func BuildKBInfos(result kb.MergeResult, tokensByType map[string]int64) []KBInfo {
	if len(result.Bubbles) == 0 {
		return nil
	}

	// pre-count bubbles per type so token splits are deterministic. Key
	// is the same normalized slug bubbleToKBInfo emits ("unknown" for
	// empty/Unknown types) so the count matches the bucket that token
	// attribution looks up — otherwise empty-Type and Unknown-Type rows
	// would share a token bucket but not a count bucket and per-bubble
	// attribution would inflate.
	typeCounts := make(map[string]int, len(result.Bubbles))
	for _, b := range result.Bubbles {
		typeCounts[normalizedTypeKey(b.Type)]++
	}

	out := make([]KBInfo, 0, len(result.Bubbles))
	for _, b := range result.Bubbles {
		out = append(out, bubbleToKBInfo(b, typeCounts[normalizedTypeKey(b.Type)], tokensByType))
	}

	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := kbTypePriority(out[i].Type), kbTypePriority(out[j].Type)
		if pi != pj {
			return pi < pj
		}
		// non-legacy before legacy within the same type bucket
		if out[i].Legacy != out[j].Legacy {
			return !out[i].Legacy
		}
		return out[i].Slug < out[j].Slug
	})
	return out
}

// bubbleToKBInfo builds one KBInfo row. Path falls back to LocalPath from
// the merger; populating the canonical paths.KBDir is the daemon's job once
// the bubble is checked out, so we don't fabricate a path here when the
// merger doesn't supply one (kb-API rows that haven't been pulled yet).
func bubbleToKBInfo(b kb.Bubble, sameTypeCount int, tokensByType map[string]int64) KBInfo {
	typeStr := normalizedTypeKey(b.Type)

	info := KBInfo{
		KBID:       b.KBID,
		Type:       typeStr,
		Slug:       b.Slug,
		Name:       b.Name,
		Path:       b.LocalPath,
		ViewerRole: b.ViewerRole,
		Legacy:     b.Legacy,
		Hint:       hintForType(b.Type, b.Legacy),
	}

	// per-bubble tokens: split the per-type rollup evenly so the sum
	// across KB entries matches the deprecated mirror's per-source totals.
	if sameTypeCount > 0 {
		if v, ok := tokensByType[typeStr]; ok && v > 0 {
			info.Tokens = int(v / int64(sameTypeCount))
		}
	}

	return info
}

// normalizedTypeKey collapses empty and KBTypeUnknown to the literal
// "unknown" slug so token bucket and count bucket lookups agree on the
// same key. Used by both BuildKBInfos (for typeCounts) and
// bubbleToKBInfo (for per-bubble token attribution).
func normalizedTypeKey(t api.KBType) string {
	if t == "" || t == api.KBTypeUnknown {
		return "unknown"
	}
	return string(t)
}

// hintForType returns a short, type-specific agent hint. Legacy ledger rows
// (synthesized as KBTypeRepo) keep the existing "ledger archive — read on
// demand" guidance so prior agent prompts continue to work.
func hintForType(t api.KBType, legacy bool) string {
	switch t {
	case api.KBTypePersonal:
		return hintPersonal
	case api.KBTypeProfile:
		return hintProfile
	case api.KBTypeTeam:
		return hintTeam
	case api.KBTypeRepo:
		return hintRepoSyn
	case api.KBTypeCustom:
		return hintCustom
	default:
		_ = legacy // intentionally unused; reserved for future per-source nuance
		return ""
	}
}

// kbTypePriority encodes the documented sort order. Mirror of the table
// in cmd/ox/kb_list.go — duplicated rather than exported because the prime
// package must not import cmd/ox.
func kbTypePriority(t string) int {
	switch t {
	case string(api.KBTypePersonal):
		return 0
	case string(api.KBTypeProfile):
		return 1
	case string(api.KBTypeTeam):
		return 2
	case string(api.KBTypeRepo):
		return 3
	case string(api.KBTypeCustom):
		return 4
	default:
		return 5
	}
}

// EnsurePersonalKBPresent enforces the I2 invariant: the caller's personal
// bubble must always appear in the KB envelope. Returns the input unchanged
// today — the server-side EnsurePersonalKBMiddleware lazy-provisions the row
// during the kb-API call, so the merger result is expected to already
// contain it. This helper exists to log a defensive warning when the
// expectation is violated despite the kb-API source being reachable.
//
// kbSourceReachable reports whether the kb-API source contributed at least
// one row (true) — the proxy for "kb-API call succeeded with the feature
// flag on". When false (kb API was unavailable / flag off / OX_KB_DISABLE),
// the absence of a personal bubble is silently tolerated because legacy
// world doesn't have personal bubbles.
func EnsurePersonalKBPresent(kbInfos []KBInfo, kbSourceReachable bool) []KBInfo {
	for _, k := range kbInfos {
		if k.Type == string(api.KBTypePersonal) {
			return kbInfos
		}
	}
	if kbSourceReachable {
		// shouldn't happen — middleware provisions on demand. Logged as a
		// single-line key=value record so it's grep-friendly.
		slog.Info("personal_kb_missing", "reason", "kb_api_reachable_but_no_personal_bubble", "kb_count", len(kbInfos))
	}
	return kbInfos
}

// KBSourceReachable reports whether the merge result contains at least one
// row sourced from /api/v1/kb. Used as the proxy for "kb feature flag is
// on for this caller and the API call succeeded" — when true and the
// personal bubble is still missing, EnsurePersonalKBPresent emits a warn.
func KBSourceReachable(result kb.MergeResult) bool {
	for _, b := range result.Bubbles {
		if b.Source == kb.SourceKB {
			return true
		}
	}
	return false
}
