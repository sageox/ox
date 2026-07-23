package recap

import (
	"testing"
	"time"

	"github.com/sageox/ox/pkg/sessionsummary"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- gatherDecisions ---
//
// Failure prevented: a decision the user's agent already recorded gets
// silently dropped (never resurfaces), or an empty/malformed summary.json
// crashes the report instead of degrading to "no decisions".

func TestGatherDecisions_ExtractsFromSummaryJSON(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	dir := f.WriteSession("s1", WithCreatedAt(fixtureNow))
	f.WriteSummary("s1", sessionsummary.SummarizeResponse{
		AgentSummary: &sessionsummary.AgentSummary{
			Decisions: []sessionsummary.Decision{
				{What: "Use errgroup for fan-out", Why: "bounded concurrency", Owner: "ryan"},
			},
		},
	})

	sessions := []SessionFacts{{Name: "s1", Dir: dir, Title: "Session s1", CreatedAt: fixtureNow}}
	decisions := gatherDecisions(f.Ledger, sessions)

	require.Len(t, decisions, 1)
	assert.Equal(t, "Use errgroup for fan-out", decisions[0].What)
	assert.Equal(t, "bounded concurrency", decisions[0].Why)
	assert.Equal(t, "ryan", decisions[0].Owner)
	assert.Equal(t, "Session s1", decisions[0].Session)
	assert.Equal(t, "session:s1", decisions[0].Receipt)
}

func TestGatherDecisions_NewestSessionsFirst(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	older := fixtureNow.Add(-48 * time.Hour)
	newer := fixtureNow

	dirOld := f.WriteSession("old", WithCreatedAt(older))
	f.WriteSummary("old", sessionsummary.SummarizeResponse{
		AgentSummary: &sessionsummary.AgentSummary{Decisions: []sessionsummary.Decision{{What: "old decision"}}},
	})
	dirNew := f.WriteSession("new", WithCreatedAt(newer))
	f.WriteSummary("new", sessionsummary.SummarizeResponse{
		AgentSummary: &sessionsummary.AgentSummary{Decisions: []sessionsummary.Decision{{What: "new decision"}}},
	})

	sessions := []SessionFacts{
		{Name: "old", Dir: dirOld, CreatedAt: older},
		{Name: "new", Dir: dirNew, CreatedAt: newer},
	}
	decisions := gatherDecisions(f.Ledger, sessions)

	require.Len(t, decisions, 2)
	assert.Equal(t, "new decision", decisions[0].What, "the most recent session's decision must lead")
	assert.Equal(t, "old decision", decisions[1].What)
}

func TestGatherDecisions_CappedAtMaxDecisions(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	var sessions []SessionFacts
	for i := 0; i < maxDecisions+3; i++ {
		name := "s" + string(rune('a'+i))
		created := fixtureNow.Add(time.Duration(i) * time.Hour)
		dir := f.WriteSession(name, WithCreatedAt(created))
		f.WriteSummary(name, sessionsummary.SummarizeResponse{
			AgentSummary: &sessionsummary.AgentSummary{Decisions: []sessionsummary.Decision{{What: "decision " + name}}},
		})
		sessions = append(sessions, SessionFacts{Name: name, Dir: dir, CreatedAt: created})
	}

	decisions := gatherDecisions(f.Ledger, sessions)
	assert.Len(t, decisions, maxDecisions)
}

func TestGatherDecisions_EmptySummaryYieldsNone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	dir := f.WriteSession("s1", WithCreatedAt(fixtureNow))
	// no summary.json written at all
	sessions := []SessionFacts{{Name: "s1", Dir: dir, CreatedAt: fixtureNow}}

	decisions := gatherDecisions(f.Ledger, sessions)
	assert.Empty(t, decisions)
}

func TestGatherDecisions_SummaryWithNoAgentSummaryYieldsNone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	dir := f.WriteSession("s1", WithCreatedAt(fixtureNow))
	f.WriteSummary("s1", sessionsummary.SummarizeResponse{Summary: "just prose, no structured agent data"})
	sessions := []SessionFacts{{Name: "s1", Dir: dir, CreatedAt: fixtureNow}}

	decisions := gatherDecisions(f.Ledger, sessions)
	assert.Empty(t, decisions)
}

func TestGatherDecisions_DecisionWithEmptyWhatSkipped(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	dir := f.WriteSession("s1", WithCreatedAt(fixtureNow))
	f.WriteSummary("s1", sessionsummary.SummarizeResponse{
		AgentSummary: &sessionsummary.AgentSummary{
			Decisions: []sessionsummary.Decision{
				{What: ""}, // malformed — must not surface as a blank claim
				{What: "real decision"},
			},
		},
	})
	sessions := []SessionFacts{{Name: "s1", Dir: dir, CreatedAt: fixtureNow}}

	decisions := gatherDecisions(f.Ledger, sessions)
	require.Len(t, decisions, 1)
	assert.Equal(t, "real decision", decisions[0].What)
}

func TestGatherDecisions_FallsBackToCacheWhenInPlaceMissing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	dir := f.WriteSession("s1", WithCreatedAt(fixtureNow))
	// summary.json lives only in the ledger cache (e.g. a teammate's synced session).
	cacheData := sessionsummary.SummarizeResponse{
		AgentSummary: &sessionsummary.AgentSummary{Decisions: []sessionsummary.Decision{{What: "cached decision"}}},
	}
	writeJSONFile(t, cachePathFor(f.Ledger, "s1", "summary.json"), cacheData)

	sessions := []SessionFacts{{Name: "s1", Dir: dir, CreatedAt: fixtureNow}}
	decisions := gatherDecisions(f.Ledger, sessions)

	require.Len(t, decisions, 1)
	assert.Equal(t, "cached decision", decisions[0].What)
}

// --- sortByCreatedDesc ---

func TestSortByCreatedDesc(t *testing.T) {
	t.Parallel()

	t1 := fixtureNow
	t2 := fixtureNow.Add(time.Hour)
	t3 := fixtureNow.Add(2 * time.Hour)

	facts := []SessionFacts{
		{Name: "middle", CreatedAt: t2},
		{Name: "oldest", CreatedAt: t1},
		{Name: "newest", CreatedAt: t3},
	}
	sortByCreatedDesc(facts)

	require.Len(t, facts, 3)
	assert.Equal(t, "newest", facts[0].Name)
	assert.Equal(t, "middle", facts[1].Name)
	assert.Equal(t, "oldest", facts[2].Name)
}

func TestSortByCreatedDesc_EmptyAndSingle(t *testing.T) {
	t.Parallel()

	empty := []SessionFacts{}
	sortByCreatedDesc(empty)
	assert.Empty(t, empty)

	single := []SessionFacts{{Name: "only"}}
	sortByCreatedDesc(single)
	assert.Equal(t, "only", single[0].Name)
}
