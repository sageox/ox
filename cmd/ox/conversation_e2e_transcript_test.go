//go:build slow

// conversation_e2e_transcript_test.go — hermetic binary-level scenarios for
// `ox conversation transcript`: cue ranges, time windows, citation URIs,
// revision pinning, the no-selector default window, and --full.
//
// Harness + fixture stager: conversation_e2e_harness_test.go. The full
// folder's transcript layer manifest pins revision 2; its six cues span
// media-clock seconds [0, 33).

package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestConversationE2E_Transcript_CueRange proves an explicit inclusive
// 1-based --cues window is served with cue ordinals intact and pinning
// reported unpinned (no revision was requested).
func TestConversationE2E_Transcript_CueRange(t *testing.T) {
	t.Parallel()
	e2e := setupConversationE2E(t)

	out, exit := e2e.Run(t, "conversation", "transcript", convE2EFullCnv, "--cues", "2-4")
	require.Equal(t, 0, exit, "out:\n%s", out)
	env, _ := decodeConversationEnvelope(t, out)
	require.True(t, env.Success, "out:\n%s", out)

	var data convE2ETranscriptData
	decodeConversationData(t, env, &data)
	require.Equal(t, 2, data.RevisionCurrent, "layer manifest pins revision 2")
	require.Zero(t, data.RevisionRequested)
	require.Equal(t, "unpinned", data.Pinning)
	require.Len(t, data.Cues, 3)
	require.Equal(t, []int{2, 4}, data.Window.Cues)
	require.False(t, data.Window.Truncated)
	require.False(t, data.Window.Clamped)
	require.Equal(t, 2, data.Cues[0].N)
	require.Equal(t, "00:00:04.000", data.Cues[0].Start)
	require.Contains(t, data.Cues[0].Text, "trusts the index file")
	require.Equal(t, "usr_a1b2c3d4e5f6a7b8c9d0e1f2", data.Cues[0].Speaker,
		"voice tags pass through as opaque usr_ ids (D12)")
}

// TestConversationE2E_Transcript_CueRangeClamped proves an out-of-range
// request is clamped to the available cues and reported clamped, not failed
// (D8's relaxed posture).
func TestConversationE2E_Transcript_CueRangeClamped(t *testing.T) {
	t.Parallel()
	e2e := setupConversationE2E(t)

	out, exit := e2e.Run(t, "conversation", "transcript", convE2EFullCnv, "--cues", "5-99")
	require.Equal(t, 0, exit, "out:\n%s", out)
	env, _ := decodeConversationEnvelope(t, out)
	require.True(t, env.Success)

	var data convE2ETranscriptData
	decodeConversationData(t, env, &data)
	require.Len(t, data.Cues, 2)
	require.Equal(t, []int{5, 6}, data.Window.Cues)
	require.True(t, data.Window.Clamped)
}

// TestConversationE2E_Transcript_TimeWindow proves a --from/--to media-clock
// window selects by closed-window overlap against half-open cue intervals:
// [9s, 16s] intersects cue 3 [9,15.5) and cue 4 [15.5,21) only.
func TestConversationE2E_Transcript_TimeWindow(t *testing.T) {
	t.Parallel()
	e2e := setupConversationE2E(t)

	out, exit := e2e.Run(t, "conversation", "transcript", convE2EFullCnv, "--from", "00:00:09", "--to", "00:00:16")
	require.Equal(t, 0, exit, "out:\n%s", out)
	env, _ := decodeConversationEnvelope(t, out)
	require.True(t, env.Success, "out:\n%s", out)

	var data convE2ETranscriptData
	decodeConversationData(t, env, &data)
	require.Equal(t, []int{3, 4}, data.Window.Cues)
	require.Len(t, data.Cues, 2)
	require.Contains(t, data.Cues[0].Text, "trust it and treat every gap")
}

// TestConversationE2E_Transcript_CitationURI_Pinned proves the headline
// acceptance flow: a sageox:// citation URI copied from a distillation atom
// yields its transcript slice in one command, revision-pinned when the
// cited revision is still current.
func TestConversationE2E_Transcript_CitationURI_Pinned(t *testing.T) {
	t.Parallel()
	e2e := setupConversationE2E(t)

	citation := convE2ECitation("@2", "#cue=5-6")
	out, exit := e2e.Run(t, "conversation", "transcript", citation)
	require.Equal(t, 0, exit, "out:\n%s", out)
	env, _ := decodeConversationEnvelope(t, out)
	require.True(t, env.Success, "out:\n%s", out)

	var data convE2ETranscriptData
	decodeConversationData(t, env, &data)
	require.Equal(t, 2, data.RevisionRequested)
	require.Equal(t, 2, data.RevisionCurrent)
	require.Equal(t, "pinned", data.Pinning)
	require.Equal(t, []int{5, 6}, data.Window.Cues)
	require.Len(t, data.Cues, 2)
	require.Contains(t, data.Cues[0].Text, "hire a forward deployed engineer")
}

// TestConversationE2E_Transcript_CitationURI_RevisionMismatch proves honest
// drift reporting (D8): a citation pinning a stale revision is still served,
// with pinning=revision_mismatch and an explanatory warning — never silent
// renumbering, never a refusal.
func TestConversationE2E_Transcript_CitationURI_RevisionMismatch(t *testing.T) {
	t.Parallel()
	e2e := setupConversationE2E(t)

	citation := convE2ECitation("@1", "#cue=5-6")
	out, exit := e2e.Run(t, "conversation", "transcript", citation)
	require.Equal(t, 0, exit, "the requested range is always served\nout:\n%s", out)
	env, _ := decodeConversationEnvelope(t, out)
	require.True(t, env.Success)

	var data convE2ETranscriptData
	decodeConversationData(t, env, &data)
	require.Equal(t, 1, data.RevisionRequested)
	require.Equal(t, 2, data.RevisionCurrent)
	require.Equal(t, "revision_mismatch", data.Pinning)
	require.Len(t, data.Cues, 2, "the cited range is served despite the mismatch")

	found := false
	for _, w := range env.Warnings {
		if strings.Contains(w, "drift") {
			found = true
		}
	}
	require.True(t, found, "a mismatch must warn about possible cue drift; warnings=%v", env.Warnings)
}

// TestConversationE2E_Transcript_DefaultWindow proves the no-selector
// invocation is windowed by construction (D15): the first 100 cues with
// truncated=true on a long transcript, and the whole file with
// truncated=false when it fits.
func TestConversationE2E_Transcript_DefaultWindow(t *testing.T) {
	t.Parallel()
	e2e := setupConversationE2E(t)

	// Long transcript: capped at 100, reported truncated.
	out, exit := e2e.Run(t, "conversation", "transcript", convE2ELongCnv)
	require.Equal(t, 0, exit, "out:\n%s", out)
	env, _ := decodeConversationEnvelope(t, out)
	require.True(t, env.Success)
	var data convE2ETranscriptData
	decodeConversationData(t, env, &data)
	require.Len(t, data.Cues, 100, "no-selector default window is 100 cues")
	require.Equal(t, []int{1, 100}, data.Window.Cues)
	require.True(t, data.Window.Truncated)

	// Short transcript: everything fits, nothing truncated.
	out, exit = e2e.Run(t, "conversation", "transcript", convE2EFullCnv)
	require.Equal(t, 0, exit, "out:\n%s", out)
	env, _ = decodeConversationEnvelope(t, out)
	var short convE2ETranscriptData
	decodeConversationData(t, env, &short)
	require.Len(t, short.Cues, 6)
	require.False(t, short.Window.Truncated)
}

// TestConversationE2E_Transcript_Full proves --full serves every cue past
// the default window — the explicit, help-texted human escape hatch.
func TestConversationE2E_Transcript_Full(t *testing.T) {
	t.Parallel()
	e2e := setupConversationE2E(t)

	out, exit := e2e.Run(t, "conversation", "transcript", convE2ELongCnv, "--full")
	require.Equal(t, 0, exit, "out:\n%s", out)
	env, _ := decodeConversationEnvelope(t, out)
	require.True(t, env.Success)

	var data convE2ETranscriptData
	decodeConversationData(t, env, &data)
	require.Len(t, data.Cues, convE2ELongCueCount)
	require.Equal(t, []int{1, convE2ELongCueCount}, data.Window.Cues)
	require.False(t, data.Window.Truncated)
	require.Equal(t, convE2ELongCueCount, data.Cues[convE2ELongCueCount-1].N)
}

// TestConversationE2E_Transcript_NotAvailable proves a folder whose
// transcript has not landed yet returns the typed transcript_not_available
// error — absence with a reason, never conflated with a bad id (D13).
func TestConversationE2E_Transcript_NotAvailable(t *testing.T) {
	t.Parallel()
	e2e := setupConversationE2E(t)

	out, exit := e2e.Run(t, "conversation", "transcript", convE2ENoVTTCnv)
	require.Equal(t, 1, exit, "out:\n%s", out)
	env, _ := decodeConversationEnvelope(t, out)
	require.False(t, env.Success)
	require.NotNil(t, env.Error)
	require.Equal(t, "transcript_not_available", env.Error.Code)
}

// TestConversationE2E_Transcript_InvalidSelectors proves structurally bad
// windows are usage errors (exit 2) rejected before any disk read: cue 0,
// reversed ranges, cues+window exclusivity, and --full with a selector.
func TestConversationE2E_Transcript_InvalidSelectors(t *testing.T) {
	t.Parallel()
	e2e := setupConversationE2E(t)

	cases := []struct {
		name string
		args []string
	}{
		{"cue zero", []string{"--cues", "0-3"}},
		{"reversed range", []string{"--cues", "4-2"}},
		{"cues and window", []string{"--cues", "1-2", "--from", "00:00:01", "--to", "00:00:05"}},
		{"full with selector", []string{"--full", "--cues", "1-2"}},
		{"half-open window", []string{"--from", "00:00:01"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			args := append([]string{"conversation", "transcript", convE2EFullCnv}, tc.args...)
			out, exit := e2e.Run(t, args...)
			require.Equal(t, 2, exit, "structurally invalid selectors are usage errors\nout:\n%s", out)
			env, _ := decodeConversationEnvelope(t, out)
			require.False(t, env.Success)
			require.NotNil(t, env.Error)
			require.Equal(t, "invalid_selector", env.Error.Code)
		})
	}
}
