//go:build slow

// conversation_e2e_topics_test.go — hermetic binary-level scenarios for
// `ox conversation topics` and `ox conversation topic`: draft episodes,
// skipped episodes, bi-temporal atoms, and --include-superseded tombstones.
//
// Harness + fixture stager: conversation_e2e_harness_test.go. The full
// folder's distillation is a draft episode with two topics and three atoms,
// one of which is a superseded tombstone.

package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestConversationE2E_Topics_Draft proves L2 for a draft episode (D10:
// drafts are first-class): projected status, topic rows with projected-
// current atom counts, citation URIs, and honest superseded totals — with
// no atom bodies at this rung (D15).
func TestConversationE2E_Topics_Draft(t *testing.T) {
	t.Parallel()
	e2e := setupConversationE2E(t)

	out, exit := e2e.Run(t, "conversation", "topics", convE2EFullCnv)
	require.Equal(t, 0, exit, "out:\n%s", out)
	env, _ := decodeConversationEnvelope(t, out)
	require.True(t, env.Success, "out:\n%s", out)

	var data convE2ETopicsData
	decodeConversationData(t, env, &data)
	require.Equal(t, "draft", data.Episode.Status)
	require.NotEmpty(t, data.Episode.ExtractedAt)
	require.Empty(t, data.Episode.SkippedReason)
	require.Equal(t, 2, data.AtomsTotal, "projected-current atoms only")
	require.Equal(t, 1, data.AtomsSuperseded, "the tombstoned atom is counted, not hidden")

	require.Len(t, data.Topics, 2)
	byID := make(map[string]int, len(data.Topics))
	for i, tp := range data.Topics {
		byID[tp.ID] = i
		require.NotEmpty(t, tp.Title)
		require.NotEmpty(t, tp.CueURIs, "topic rows carry walkable citation URIs")
	}
	require.Contains(t, byID, convE2ETopicHiring)
	require.Contains(t, byID, convE2ETopicIndexTrust)
	require.Equal(t, 1, data.Topics[byID[convE2ETopicHiring]].AtomCount,
		"the superseded atom must not inflate the current count")
	require.Equal(t, 1, data.Topics[byID[convE2ETopicIndexTrust]].AtomCount)
	require.NotContains(t, string(env.Data), `"atoms":`, "no atom bodies at the topics rung (D15)")
	require.Contains(t, env.Guidance, "topic", "guidance names the next rung")
}

// TestConversationE2E_Topics_Skipped proves a skipped episode returns its
// header with status and skipped_reason (D10) instead of erroring or
// pretending topics exist.
func TestConversationE2E_Topics_Skipped(t *testing.T) {
	t.Parallel()
	e2e := setupConversationE2E(t)

	out, exit := e2e.Run(t, "conversation", "topics", convE2ESkippedCnv)
	require.Equal(t, 0, exit, "out:\n%s", out)
	env, _ := decodeConversationEnvelope(t, out)
	require.True(t, env.Success, "out:\n%s", out)

	var data convE2ETopicsData
	decodeConversationData(t, env, &data)
	require.Equal(t, "skipped", data.Episode.Status)
	require.Equal(t, "cluster_exhausted_v2", data.Episode.SkippedReason)
	require.Empty(t, data.Topics)
	require.Zero(t, data.AtomsTotal)
}

// TestConversationE2E_Topics_NoDistillation proves a conversation without a
// distillation episode returns the typed no_distillation error — absence
// with a reason, never conflated with a bad id (D13).
func TestConversationE2E_Topics_NoDistillation(t *testing.T) {
	t.Parallel()
	e2e := setupConversationE2E(t)

	out, exit := e2e.Run(t, "conversation", "topics", convE2ELegacyCnv)
	require.Equal(t, 1, exit, "out:\n%s", out)
	env, _ := decodeConversationEnvelope(t, out)
	require.False(t, env.Success)
	require.NotNil(t, env.Error)
	require.Equal(t, "no_distillation", env.Error.Code)
}

// TestConversationE2E_Topic_CurrentAtoms proves L3 defaults to projected-
// current atoms only (D11): the superseded tombstone is excluded from the
// atom list but reported in the counts, and served atoms carry their quote,
// source citation URIs, and confidence.
func TestConversationE2E_Topic_CurrentAtoms(t *testing.T) {
	t.Parallel()
	e2e := setupConversationE2E(t)

	out, exit := e2e.Run(t, "conversation", "topic", convE2EFullCnv, convE2ETopicHiring)
	require.Equal(t, 0, exit, "out:\n%s", out)
	env, _ := decodeConversationEnvelope(t, out)
	require.True(t, env.Success, "out:\n%s", out)

	var data convE2ETopicData
	decodeConversationData(t, env, &data)
	require.Equal(t, convE2ETopicHiring, data.Topic.ID)
	require.Equal(t, "Hiring", data.Topic.Title)
	require.Equal(t, 1, data.AtomsTotal)
	require.Equal(t, 1, data.AtomsSuperseded)
	require.Len(t, data.Atoms, 1, "default view is projected-current only")

	atom := data.Atoms[0]
	require.Equal(t, convE2EAtomHire, atom.ID)
	require.Equal(t, "decision", atom.Kind)
	require.Equal(t, "high", atom.Signal)
	require.InDelta(t, 0.95, atom.Conf, 1e-9)
	require.NotNil(t, atom.Quote)
	require.Equal(t, 5, atom.Quote.CueRef)
	require.NotNil(t, atom.Source)
	require.NotEmpty(t, atom.Source.URIs, "source citation URIs make the atom walkable")
	require.Empty(t, atom.ValidTo, "current atoms carry no tombstone fields")
	require.Empty(t, atom.SupersededBy)
}

// TestConversationE2E_Topic_IncludeSuperseded proves --include-superseded
// opts into tombstones with valid_from/valid_to/superseded_by so succession
// chains are auditable (D11).
func TestConversationE2E_Topic_IncludeSuperseded(t *testing.T) {
	t.Parallel()
	e2e := setupConversationE2E(t)

	out, exit := e2e.Run(t, "conversation", "topic", convE2EFullCnv, convE2ETopicHiring, "--include-superseded")
	require.Equal(t, 0, exit, "out:\n%s", out)
	env, _ := decodeConversationEnvelope(t, out)
	require.True(t, env.Success)

	var data convE2ETopicData
	decodeConversationData(t, env, &data)
	require.Len(t, data.Atoms, 2)
	require.Equal(t, 1, data.AtomsTotal, "counts stay honest regardless of the view")
	require.Equal(t, 1, data.AtomsSuperseded)

	var tombstone *convE2EAtom
	for i := range data.Atoms {
		if data.Atoms[i].ID == convE2EAtomSuperseded {
			tombstone = &data.Atoms[i]
		}
	}
	require.NotNil(t, tombstone, "the tombstone must be served under --include-superseded")
	require.NotEmpty(t, tombstone.ValidFrom)
	require.NotEmpty(t, tombstone.ValidTo)
	require.Equal(t, convE2EAtomHire, tombstone.SupersededBy, "the succession chain points at the superseding atom")
}

// TestConversationE2E_Topic_NotFound proves a strictly valid tp_ id that no
// topic carries is the typed topic_not_found runtime error (D21) — distinct
// from invalid_id, which is a usage error.
func TestConversationE2E_Topic_NotFound(t *testing.T) {
	t.Parallel()
	e2e := setupConversationE2E(t)

	out, exit := e2e.Run(t, "conversation", "topic", convE2EFullCnv, "tp_01a012cb-9764-7555-a3f3-cccccccccccc")
	require.Equal(t, 1, exit, "out:\n%s", out)
	env, _ := decodeConversationEnvelope(t, out)
	require.False(t, env.Success)
	require.NotNil(t, env.Error)
	require.Equal(t, "topic_not_found", env.Error.Code)
}
