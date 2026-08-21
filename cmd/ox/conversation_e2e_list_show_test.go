//go:build slow

// conversation_e2e_list_show_test.go — hermetic binary-level scenarios for
// `ox conversation list` and `ox conversation show`, plus the environment
// scenarios shared by the whole family (id validation, logged-out reads,
// index-miss copy, ephemeral no_team_context).
//
// Harness + fixture stager: conversation_e2e_harness_test.go.

package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestConversationE2E_List_Populated proves the full L0 path: binary →
// config → team-context checkout → INDEX.json → envelope. Phantom entries,
// hostile folder names, and junk index lines are dropped; derived fields
// (recorded_at from the UUIDv7, has_distillation from a stat) are present;
// rows sort newest first.
// Failure prevented: the binary cannot resolve the staged team context, or
// the index policy (drop phantoms, trust nothing else) regresses.
func TestConversationE2E_List_Populated(t *testing.T) {
	t.Parallel()
	e2e := setupConversationE2E(t)

	out, exit := e2e.Run(t, "conversation", "list")
	require.Equal(t, 0, exit, "out:\n%s", out)
	env, _ := decodeConversationEnvelope(t, out)
	require.True(t, env.Success, "out:\n%s", out)
	require.Nil(t, env.Error)
	require.NotEmpty(t, env.Guidance, "guidance must name the next disclosure rung")
	require.Positive(t, env.TokenEstimate)

	var data convE2EListData
	decodeConversationData(t, env, &data)
	require.Equal(t, convE2ETotalIndexed, data.TotalIndexed, "total_indexed counts parseable entries before dropping")
	require.Len(t, data.Conversations, convE2ELiveFolders, "phantom + hostile entries must be dropped")
	require.False(t, data.Truncated)

	// Newest first by UUIDv7-derived recorded_at.
	wantOrder := []string{convE2ELongRec, convE2ENoVTTRec, convE2ESkippedRec, convE2EBothRec, convE2ELegacyRec, convE2EFullRec}
	for i, row := range data.Conversations {
		require.Equal(t, wantOrder[i], row.RecordingID, "row %d out of order", i)
		require.NotEmpty(t, row.RecordedAt, "recorded_at must be derived for %s", row.RecordingID)
		require.Equal(t, "cnv_"+strings.TrimPrefix(row.RecordingID, "rec_"), row.ConversationID,
			"cnv_/rec_ twins share one UUID by prefix swap")
	}

	byRec := make(map[string]convE2EListRow, len(data.Conversations))
	for _, row := range data.Conversations {
		byRec[row.RecordingID] = row
	}
	require.True(t, byRec[convE2EFullRec].HasDistillation, "full folder has distillation.jsonl")
	require.True(t, byRec[convE2ESkippedRec].HasDistillation)
	require.False(t, byRec[convE2ELegacyRec].HasDistillation)
	require.Equal(t, 1, byRec[convE2EFullRec].DecisionCount)
	// Title fallback chain (D13): empty index title → metadata.json → folder.
	require.Equal(t, "Metadata Title Fallback", byRec[convE2ENoVTTRec].Title)
	require.Equal(t, "2026-08-11-22-32-full", byRec[convE2EFullRec].Title,
		"empty index + empty metadata title falls back to the folder name")
}

// TestConversationE2E_List_BareParentRunsList proves `ox conversation` with
// no subcommand behaves as list (discoverability contract).
func TestConversationE2E_List_BareParentRunsList(t *testing.T) {
	t.Parallel()
	e2e := setupConversationE2E(t)

	out, exit := e2e.Run(t, "conversation")
	require.Equal(t, 0, exit, "out:\n%s", out)
	env, _ := decodeConversationEnvelope(t, out)
	require.True(t, env.Success)
	var data convE2EListData
	decodeConversationData(t, env, &data)
	require.Len(t, data.Conversations, convE2ELiveFolders)
}

// TestConversationE2E_List_LimitAndSince exercises the windowing flags
// against the staged tree: --limit truncates honestly and --since filters on
// the derived recorded_at.
func TestConversationE2E_List_LimitAndSince(t *testing.T) {
	t.Parallel()
	e2e := setupConversationE2E(t)

	out, exit := e2e.Run(t, "conversation", "list", "--limit", "2")
	require.Equal(t, 0, exit, "out:\n%s", out)
	env, _ := decodeConversationEnvelope(t, out)
	var data convE2EListData
	decodeConversationData(t, env, &data)
	require.Len(t, data.Conversations, 2)
	require.True(t, data.Truncated, "more rows exist than the limit")
	require.Equal(t, convE2ETotalIndexed, data.TotalIndexed, "total_indexed reports index size, not the window")

	// --since pinned to the third-newest row's own derived recorded_at:
	// exactly the rows at or after that instant survive.
	out, exit = e2e.Run(t, "conversation", "list")
	require.Equal(t, 0, exit)
	env, _ = decodeConversationEnvelope(t, out)
	var all convE2EListData
	decodeConversationData(t, env, &all)
	require.Len(t, all.Conversations, convE2ELiveFolders)
	since := all.Conversations[2].RecordedAt
	require.NotEmpty(t, since)

	out, exit = e2e.Run(t, "conversation", "list", "--since", since)
	require.Equal(t, 0, exit, "out:\n%s", out)
	env, _ = decodeConversationEnvelope(t, out)
	var filtered convE2EListData
	decodeConversationData(t, env, &filtered)
	require.Len(t, filtered.Conversations, 3, "rows recorded before --since must be dropped")
	require.Equal(t, all.Conversations[2].RecordingID, filtered.Conversations[2].RecordingID)
}

// TestConversationE2E_List_EmptyTeam proves a synced team context with no
// discussions tree at all lists cleanly: success, zero rows, zero indexed —
// absence is data, not an error (D13).
func TestConversationE2E_List_EmptyTeam(t *testing.T) {
	t.Parallel()
	e2e := setupDistillHistoryE2E(t, nowConversationE2E())
	// No stageConversationDiscussions: the team context has no discussions/.

	out, exit := e2e.Run(t, "conversation", "list")
	require.Equal(t, 0, exit, "out:\n%s", out)
	env, _ := decodeConversationEnvelope(t, out)
	require.True(t, env.Success, "out:\n%s", out)
	var data convE2EListData
	decodeConversationData(t, env, &data)
	require.NotNil(t, data.Conversations)
	require.Empty(t, data.Conversations)
	require.Zero(t, data.TotalIndexed)
}

// TestConversationE2E_Show_WithSummary proves L1 against summary.json:
// human_summary served, participants resolved from the summary (unjoined,
// ghosts dropped), chapters and decisions deliberately absent (D19).
func TestConversationE2E_Show_WithSummary(t *testing.T) {
	t.Parallel()
	e2e := setupConversationE2E(t)

	out, exit := e2e.Run(t, "conversation", "show", convE2EFullCnv)
	require.Equal(t, 0, exit, "out:\n%s", out)
	env, _ := decodeConversationEnvelope(t, out)
	require.True(t, env.Success, "out:\n%s", out)

	var data convE2EShowData
	decodeConversationData(t, env, &data)
	require.Equal(t, convE2EFullCnv, data.ConversationID)
	require.Equal(t, convE2EFullRec, data.RecordingID)
	require.True(t, data.Summary.Available)
	require.Contains(t, data.Summary.HumanSummary, "trust the index file")
	require.Equal(t, []string{"Galex Yen", "Ryan"}, data.Participants,
		"participants come from summary.json names, empty names dropped (D12)")
	require.NotContains(t, string(env.Data), `"chapters"`, "show is summary-only (D19)")
	require.Contains(t, env.Guidance, "topics", "guidance names the next rung")
}

// TestConversationE2E_Show_WithoutSummary proves a folder whose summary has
// not landed yet is served as typed absence — never an error, never
// conflated with a bad id (D13). Also covers the metadata.json title
// fallback and the rec_ id spelling.
func TestConversationE2E_Show_WithoutSummary(t *testing.T) {
	t.Parallel()
	e2e := setupConversationE2E(t)

	out, exit := e2e.Run(t, "conversation", "show", convE2ENoVTTRec)
	require.Equal(t, 0, exit, "out:\n%s", out)
	env, _ := decodeConversationEnvelope(t, out)
	require.True(t, env.Success, "a missing summary is data, not an error")

	var data convE2EShowData
	decodeConversationData(t, env, &data)
	require.False(t, data.Summary.Available)
	require.Equal(t, "not_yet_generated", data.Summary.Reason)
	require.Empty(t, data.Summary.HumanSummary)
	require.Equal(t, "Metadata Title Fallback", data.Title)
}

// TestConversationE2E_Show_LegacySummaryMarkdown proves the summary.md
// fallback for pre-JSON folders (D19).
func TestConversationE2E_Show_LegacySummaryMarkdown(t *testing.T) {
	t.Parallel()
	e2e := setupConversationE2E(t)

	out, exit := e2e.Run(t, "conversation", "show", convE2ELegacyCnv)
	require.Equal(t, 0, exit, "out:\n%s", out)
	env, _ := decodeConversationEnvelope(t, out)
	require.True(t, env.Success)

	var data convE2EShowData
	decodeConversationData(t, env, &data)
	require.True(t, data.Summary.Available)
	require.Contains(t, data.Summary.HumanSummary, "Legacy Summary")
	require.Equal(t, "Legacy Era Discussion", data.Title, "index title wins when present")
}

// TestConversationE2E_InvalidIDs proves strict D16 validation at the binary
// level: folder names, bare UUIDs, and wrong prefixes are usage errors
// (exit 2, code invalid_id) — never treated as lookups.
func TestConversationE2E_InvalidIDs(t *testing.T) {
	t.Parallel()
	e2e := setupConversationE2E(t)

	cases := []struct {
		name string
		args []string
	}{
		{"folder name", []string{"conversation", "show", "2026-08-11-22-32-full"}},
		{"bare uuid", []string{"conversation", "show", convE2EFullUUID}},
		{"wrong prefix", []string{"conversation", "show", "ses_" + convE2EFullUUID}},
		{"uuid v4 not v7", []string{"conversation", "show", "cnv_019ff2f5-2079-4be1-b05e-8caad2772e61"}},
		{"transcript bad id", []string{"conversation", "transcript", "not-an-id"}},
		{"topics bad id", []string{"conversation", "topics", "nope"}},
		{"topic bad topic id", []string{"conversation", "topic", convE2EFullCnv, "first-topic"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, exit := e2e.Run(t, tc.args...)
			require.Equal(t, 2, exit, "invalid ids are usage errors\nout:\n%s", out)
			env, _ := decodeConversationEnvelope(t, out)
			require.False(t, env.Success)
			require.NotNil(t, env.Error)
			require.Equal(t, "invalid_id", env.Error.Code)
		})
	}
}

// TestConversationE2E_IndexMiss proves a strictly valid id with no live
// index entry hard-fails with the typed not_indexed error and the "not
// indexed yet" copy (D3) — a runtime failure (exit 1), not a usage error.
func TestConversationE2E_IndexMiss(t *testing.T) {
	t.Parallel()
	e2e := setupConversationE2E(t)

	out, exit := e2e.Run(t, "conversation", "show", convE2EUnindexedRec)
	require.Equal(t, 1, exit, "out:\n%s", out)
	env, _ := decodeConversationEnvelope(t, out)
	require.False(t, env.Success)
	require.NotNil(t, env.Error)
	require.Equal(t, "not_indexed", env.Error.Code)
	require.Contains(t, env.Error.Message, "not indexed yet", "error copy must say why and imply the fix is server-side")
}

// TestConversationE2E_LoggedOut proves the whole local read path works with
// no auth on the machine (D14): auth.json deleted, reads still succeed.
// Failure prevented: an auth or network dependency sneaking into the
// local-first path.
func TestConversationE2E_LoggedOut(t *testing.T) {
	t.Parallel()
	e2e := setupConversationE2E(t)
	removeConversationAuth(t, e2e)

	out, exit := e2e.Run(t, "conversation", "list")
	require.Equal(t, 0, exit, "logged-out list failed\nout:\n%s", out)
	env, _ := decodeConversationEnvelope(t, out)
	require.True(t, env.Success)

	out, exit = e2e.Run(t, "conversation", "show", convE2EFullCnv)
	require.Equal(t, 0, exit, "logged-out show failed\nout:\n%s", out)
	env, _ = decodeConversationEnvelope(t, out)
	require.True(t, env.Success)

	out, exit = e2e.Run(t, "conversation", "transcript", convE2EFullCnv, "--cues", "1-2")
	require.Equal(t, 0, exit, "logged-out transcript failed\nout:\n%s", out)
	env, _ = decodeConversationEnvelope(t, out)
	require.True(t, env.Success)
}

// TestConversationE2E_EphemeralNoTeamContext proves the typed
// no_team_context error when no local checkout is resolvable (D14:
// ephemeral mode / pre-first-sync): the registered team rows are removed so
// resolution finds nothing under the rerouted XDG tree.
func TestConversationE2E_EphemeralNoTeamContext(t *testing.T) {
	t.Parallel()
	e2e := setupConversationE2E(t)
	writeLocalConfigTeams(t, e2e.workspace, nil)

	out, exit := e2e.Run(t, "conversation", "list")
	require.Equal(t, 1, exit, "out:\n%s", out)
	env, _ := decodeConversationEnvelope(t, out)
	require.False(t, env.Success)
	require.NotNil(t, env.Error)
	require.Equal(t, "no_team_context", env.Error.Code)
}
