package prime

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/kb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. envelope shape ---

// TestBuildKBInfos_ShapeAcrossThreeBubbles snapshots the JSON shape produced
// by the kb envelope when the merger returns one personal bubble, one team
// bubble, and one legacy ledger row — the canonical "three sources, one
// envelope" case from the plan.
//
// Failure prevented: silent envelope drift (renamed JSON keys, dropped
// fields, reordered rows) breaks every downstream agent that consumes prime
// output without a schema migration.
func TestBuildKBInfos_ShapeAcrossThreeBubbles(t *testing.T) {
	res := kb.MergeResult{
		Bubbles: []kb.Bubble{
			{
				KBID:       "kb_personal",
				Type:       api.KBTypePersonal,
				Slug:       "personal-abc",
				Name:       "Ryan's Personal",
				ViewerRole: "owner",
				LocalPath:  "/data/sageox/sageox.ai/kb/kb_personal",
				Source:     kb.SourceKB,
			},
			{
				KBID:       "kb_team",
				Type:       api.KBTypeTeam,
				Slug:       "platform",
				Name:       "Platform",
				ViewerRole: "member",
				LocalPath:  "/data/sageox/sageox.ai/kb/kb_team",
				Source:     kb.SourceKB,
			},
			{
				Type:      api.KBTypeRepo,
				Slug:      "my-app",
				Name:      "my-app",
				LocalPath: "/data/ledgers/my-app",
				RepoID:    "repo_xyz",
				Source:    kb.SourceLedger,
				Legacy:    true,
			},
		},
	}

	got := BuildKBInfos(res, nil)
	require.Len(t, got, 3)

	// snapshot via JSON to lock the field set + tag names
	b, err := json.Marshal(got)
	require.NoError(t, err)

	// personal first (priority 0), team next (priority 2), repo last (priority 3)
	assert.Equal(t, "personal-abc", got[0].Slug)
	assert.Equal(t, "personal", got[0].Type)
	assert.Equal(t, "platform", got[1].Slug)
	assert.Equal(t, "team", got[1].Type)
	assert.Equal(t, "my-app", got[2].Slug)
	assert.Equal(t, "repo", got[2].Type)
	assert.True(t, got[2].Legacy)

	// type-specific hints flow through
	assert.Contains(t, got[0].Hint, "personal scratchpad")
	assert.Contains(t, got[1].Hint, "team-wide")
	assert.Contains(t, got[2].Hint, "ledger archive")

	// JSON keys: lowercase, the documented contract
	jsonStr := string(b)
	assert.Contains(t, jsonStr, `"kb_id":"kb_personal"`)
	assert.Contains(t, jsonStr, `"viewer_role":"owner"`)
	assert.Contains(t, jsonStr, `"legacy":true`)
}

// --- B. personal bubble guarantee (I2) ---

// TestEnsurePersonalKBPresent_WarnsWhenKBSourceReachableButPersonalMissing
// captures the I2 invariant: the server's EnsurePersonalKBMiddleware should
// always lazy-provision a personal bubble during the kb-API call, so its
// absence under "kb source reachable" indicates a server-side regression
// the agent should know about.
//
// Failure prevented: a silent regression in the personal-bubble middleware
// would otherwise leave first-session callers with no scratchpad and no
// trace in logs that anything was wrong.
func TestEnsurePersonalKBPresent_WarnsWhenKBSourceReachableButPersonalMissing(t *testing.T) {
	logBuf := captureSlog(t)

	infos := []KBInfo{
		{Type: "team", Slug: "platform"},
		{Type: "repo", Slug: "my-app", Legacy: true},
	}
	got := EnsurePersonalKBPresent(infos, true /* kbSourceReachable */)

	// the helper does not fabricate a personal row — it just warns. The
	// non-personal entries must still flow through untouched.
	assert.Len(t, got, 2, "non-personal entries must still be returned")
	assert.Contains(t, logBuf.String(), "personal_kb_missing")
}

// TestEnsurePersonalKBPresent_SilentWhenKBSourceUnavailable verifies the
// legacy-only world is silent: when the kb API is not reachable, there is
// no personal bubble to expect, so emitting a warning would be noise.
//
// Failure prevented: false-positive warnings on every prime call from a
// caller without the kb-feature flag rolled out yet.
func TestEnsurePersonalKBPresent_SilentWhenKBSourceUnavailable(t *testing.T) {
	logBuf := captureSlog(t)

	infos := []KBInfo{
		{Type: "team", Slug: "platform", Legacy: true},
		{Type: "repo", Slug: "my-app", Legacy: true},
	}
	got := EnsurePersonalKBPresent(infos, false /* kbSourceReachable */)

	assert.Len(t, got, 2)
	assert.NotContains(t, logBuf.String(), "personal_kb_missing")
}

// TestEnsurePersonalKBPresent_NoOpWhenPresent confirms the happy path: a
// personal bubble in the input is left exactly as-is, no warn emitted.
//
// Failure prevented: a regression where the helper accidentally rewrites
// the slice (dedup, sort, nil-slice replacement) would silently corrupt
// what the agent receives.
func TestEnsurePersonalKBPresent_NoOpWhenPresent(t *testing.T) {
	logBuf := captureSlog(t)

	infos := []KBInfo{
		{Type: "personal", Slug: "personal-abc"},
		{Type: "team", Slug: "platform"},
	}
	got := EnsurePersonalKBPresent(infos, true)

	assert.Equal(t, infos, got)
	assert.NotContains(t, logBuf.String(), "personal_kb_missing")
}

// TestKBSourceReachable_TrueWhenKBRowsPresent / _FalseOnLegacyOnly map
// merger results to the boolean prime uses to gate the warn.
//
// Failure prevented: misclassifying a legacy-only result as "kb-API
// reachable" would generate spurious warns; misclassifying a populated
// kb-API result as unreachable would suppress a real regression alert.
func TestKBSourceReachable(t *testing.T) {
	t.Run("true when at least one kb-source row", func(t *testing.T) {
		res := kb.MergeResult{Bubbles: []kb.Bubble{
			{Type: api.KBTypeTeam, Slug: "platform", Source: kb.SourceKB},
			{Type: api.KBTypeRepo, Slug: "my-app", Source: kb.SourceLedger, Legacy: true},
		}}
		assert.True(t, KBSourceReachable(res))
	})

	t.Run("false on legacy-only result", func(t *testing.T) {
		res := kb.MergeResult{Bubbles: []kb.Bubble{
			{Type: api.KBTypeTeam, Slug: "platform", Source: kb.SourceTeamLegacy, Legacy: true},
			{Type: api.KBTypeRepo, Slug: "my-app", Source: kb.SourceLedger, Legacy: true},
		}}
		assert.False(t, KBSourceReachable(res))
	})

	t.Run("false on empty result", func(t *testing.T) {
		assert.False(t, KBSourceReachable(kb.MergeResult{}))
	})
}

// --- C. sort + token attribution ---

// TestBuildKBInfos_SortOrder verifies the documented sort: type-priority,
// then non-legacy before legacy within a type, then slug.
//
// Failure prevented: shuffled output across releases would churn snapshot
// tests and surprise agents that index into KB[0] expecting the personal
// bubble.
func TestBuildKBInfos_SortOrder(t *testing.T) {
	res := kb.MergeResult{
		Bubbles: []kb.Bubble{
			{Type: api.KBTypeTeam, Slug: "zeta", Source: kb.SourceTeamLegacy, Legacy: true},
			{Type: api.KBTypeTeam, Slug: "alpha", Source: kb.SourceKB},
			{Type: api.KBTypePersonal, Slug: "personal-abc", Source: kb.SourceKB},
			{Type: api.KBTypeRepo, Slug: "my-app", Source: kb.SourceLedger, Legacy: true},
		},
	}

	got := BuildKBInfos(res, nil)
	require.Len(t, got, 4)

	// personal (priority 0) → team non-legacy (priority 2, !legacy) →
	// team legacy → repo
	assert.Equal(t, "personal-abc", got[0].Slug)
	assert.Equal(t, "alpha", got[1].Slug)
	assert.False(t, got[1].Legacy)
	assert.Equal(t, "zeta", got[2].Slug)
	assert.True(t, got[2].Legacy)
	assert.Equal(t, "my-app", got[3].Slug)
}

// TestBuildKBInfos_TokenAttributionSplitsByType verifies KB[].Tokens is
// derived from the per-kb-type rollup so consumers of the deprecated
// CumulativeContextTokensBySource mirror see consistent totals.
//
// Failure prevented: KB token totals diverging from the deprecated mirror
// would make the rollout sanity-check ("the new field equals the sum of the
// old ones") fail and erode confidence in the migration.
func TestBuildKBInfos_TokenAttributionSplitsByType(t *testing.T) {
	res := kb.MergeResult{
		Bubbles: []kb.Bubble{
			{Type: api.KBTypeTeam, Slug: "alpha", Source: kb.SourceKB},
			{Type: api.KBTypeTeam, Slug: "beta", Source: kb.SourceKB},
			{Type: api.KBTypeRepo, Slug: "my-app", Source: kb.SourceLedger, Legacy: true},
		},
	}
	tokens := map[string]int64{
		"team": 1000, // split across the two team bubbles
		"repo": 50,
	}

	got := BuildKBInfos(res, tokens)
	require.Len(t, got, 3)

	// summed by type, the per-kb totals must equal the input rollup
	sum := map[string]int{}
	for _, k := range got {
		sum[k.Type] += k.Tokens
	}
	assert.Equal(t, 1000, sum["team"], "team tokens must equal the per-type rollup")
	assert.Equal(t, 50, sum["repo"], "repo tokens must equal the per-type rollup")
}

// TestBuildKBInfos_EmptyResultReturnsNil makes the empty case explicit so
// callers can compare to nil (idiomatic Go) and json.Marshal omits the
// field via the omitempty tag.
//
// Failure prevented: a [] vs nil drift would change the JSON output shape
// and break agents that branch on the field's presence.
func TestBuildKBInfos_EmptyResultReturnsNil(t *testing.T) {
	got := BuildKBInfos(kb.MergeResult{}, nil)
	assert.Nil(t, got)
}

// --- D. deprecated mirror coexistence ---

// TestOutput_KBAndDeprecatedMirrorsCoexist verifies the contract that for
// one release the new envelope and the deprecated TeamContext + Ledger
// fields all serialize together. The plan: "keep team_context/ledger
// populated for one release (mirrors)".
//
// Failure prevented: dropping the deprecated fields prematurely would
// strand any agent prompt that still reads the old keys.
func TestOutput_KBAndDeprecatedMirrorsCoexist(t *testing.T) {
	out := Output{
		AgentID: "OxTEST",
		KB: []KBInfo{
			{Type: "personal", Slug: "personal-abc"},
			{Type: "team", Slug: "platform"},
		},
		TeamContext: &TeamContextInfo{TeamID: "team_abc", TeamName: "Platform"},
		Ledger:      &LedgerInfo{Exists: true, Path: "/data/ledger"},
	}

	b, err := json.Marshal(out)
	require.NoError(t, err)

	jsonStr := string(b)
	assert.Contains(t, jsonStr, `"kb":`, "kb envelope must serialize")
	assert.Contains(t, jsonStr, `"team_context":`, "deprecated team_context mirror must serialize")
	assert.Contains(t, jsonStr, `"ledger":`, "deprecated ledger mirror must serialize")
}

// captureSlog redirects the default slog logger to a buffer for the test
// duration and restores it on cleanup. Returns a *bytes.Buffer the test can
// inspect for emitted log lines.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	buf := &bytes.Buffer{}
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return buf
}

// TestBuildKBInfos_ChannelTypeHintAndSort verifies a channel bubble carries
// the channel-specific hint and sorts between custom and unknown — the slot
// reserved for it before KBTypeChannel is promoted into internal/api.
//
// Failure prevented: a channel row arriving from the kb-API source (after the
// mono rollout) would either lose its agent-facing hint (default empty string)
// or sort into the wrong bucket, surprising agents that index by position.
func TestBuildKBInfos_ChannelTypeHintAndSort(t *testing.T) {
	res := kb.MergeResult{
		Bubbles: []kb.Bubble{
			{Type: api.KBTypeCustom, Slug: "custom-thing", Source: kb.SourceKB},
			{Type: api.KBType("channel"), Slug: "wip-broadcast", Source: kb.SourceKB},
			{Type: api.KBTypeUnknown, Slug: "future-kind", Source: kb.SourceKB},
		},
	}

	got := BuildKBInfos(res, nil)
	require.Len(t, got, 3)

	// custom (4) → channel (5) → unknown (6)
	assert.Equal(t, "custom-thing", got[0].Slug)
	assert.Equal(t, "wip-broadcast", got[1].Slug)
	assert.Equal(t, "channel", got[1].Type)
	assert.Contains(t, got[1].Hint, "channel bubble")
	assert.Contains(t, got[1].Hint, "manual session recording")
	assert.Equal(t, "future-kind", got[2].Slug)
}

// sanity: ensure captureSlog actually captures (a meta-test for the helper
// so a broken capture doesn't hide a missing-warn assertion).
func TestCaptureSlog_RoundTrips(t *testing.T) {
	buf := captureSlog(t)
	slog.Warn("probe", "k", "v")
	if !strings.Contains(buf.String(), "probe") {
		t.Fatalf("slog capture failed; buf=%q", buf.String())
	}
}
