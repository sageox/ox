package session

import (
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeEntries returns n SessionEntries with index-encoded content for assertion.
func makeEntries(n int) []SessionEntry {
	out := make([]SessionEntry, n)
	for i := 0; i < n; i++ {
		out[i] = SessionEntry{
			Type:    EntryTypeUser,
			Content: contentFor(i),
		}
	}
	return out
}

func contentFor(i int) string {
	return "entry-" + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

func TestBuildSegmentRanges_Empty(t *testing.T) {
	ranges := BuildSegmentRanges(nil)
	assert.Empty(t, ranges)
}

func TestBuildSegmentRanges_NoPauseEvents(t *testing.T) {
	lc := []LifecycleEvent{
		{Action: LifecycleActionStart, Seq: 0},
		{Action: LifecycleActionStop, Seq: 10},
	}
	assert.Empty(t, BuildSegmentRanges(lc))
}

func TestBuildSegmentRanges_SinglePauseResume(t *testing.T) {
	lc := []LifecycleEvent{
		{Action: LifecycleActionStart, Seq: 0},
		{Action: LifecycleActionPause, Seq: 5},
		{Action: LifecycleActionResume, Seq: 10},
		{Action: LifecycleActionStop, Seq: 20},
	}
	ranges := BuildSegmentRanges(lc)
	require.Len(t, ranges, 1)
	assert.Equal(t, SegmentRange{Start: 5, End: 10}, ranges[0])
}

func TestBuildSegmentRanges_MultipleCycles(t *testing.T) {
	lc := []LifecycleEvent{
		{Action: LifecycleActionPause, Seq: 5},
		{Action: LifecycleActionResume, Seq: 10},
		{Action: LifecycleActionPause, Seq: 15},
		{Action: LifecycleActionResume, Seq: 18},
		{Action: LifecycleActionPause, Seq: 22},
		{Action: LifecycleActionResume, Seq: 25},
	}
	ranges := BuildSegmentRanges(lc)
	require.Len(t, ranges, 3)
	assert.Equal(t, SegmentRange{Start: 5, End: 10}, ranges[0])
	assert.Equal(t, SegmentRange{Start: 15, End: 18}, ranges[1])
	assert.Equal(t, SegmentRange{Start: 22, End: 25}, ranges[2])
}

func TestBuildSegmentRanges_OpenEndedPause(t *testing.T) {
	lc := []LifecycleEvent{
		{Action: LifecycleActionPause, Seq: 5},
	}
	ranges := BuildSegmentRanges(lc)
	require.Len(t, ranges, 1)
	assert.Equal(t, 5, ranges[0].Start)
	assert.Equal(t, segmentOpenEnded, ranges[0].End)
}

func TestBuildSegmentRanges_TerminalClosesOpenPause(t *testing.T) {
	for _, term := range []LifecycleAction{LifecycleActionStop, LifecycleActionClearFinalize, LifecycleActionExpired} {
		term := term
		t.Run(string(term), func(t *testing.T) {
			lc := []LifecycleEvent{
				{Action: LifecycleActionPause, Seq: 5},
				{Action: term, Seq: 10},
			}
			ranges := BuildSegmentRanges(lc)
			require.Len(t, ranges, 1)
			assert.Equal(t, SegmentRange{Start: 5, End: 10}, ranges[0])
		})
	}
}

func TestBuildSegmentRanges_ResumeWithoutPause(t *testing.T) {
	lc := []LifecycleEvent{
		{Action: LifecycleActionResume, Seq: 10},
	}
	assert.Empty(t, BuildSegmentRanges(lc))
}

func TestBuildSegmentRanges_DoublePause(t *testing.T) {
	// Second pause while already paused is ignored — the first pause's
	// range stays open until the next resume.
	lc := []LifecycleEvent{
		{Action: LifecycleActionPause, Seq: 5},
		{Action: LifecycleActionPause, Seq: 7},
		{Action: LifecycleActionResume, Seq: 10},
	}
	ranges := BuildSegmentRanges(lc)
	require.Len(t, ranges, 1)
	assert.Equal(t, SegmentRange{Start: 5, End: 10}, ranges[0])
}

func TestIsSeqExcluded(t *testing.T) {
	ranges := []SegmentRange{
		{Start: 5, End: 10},
		{Start: 15, End: 20},
	}
	cases := map[int]bool{
		0: false, 4: false, 5: true, 9: true, 10: false,
		14: false, 15: true, 19: true, 20: false, 100: false,
	}
	for seq, want := range cases {
		assert.Equalf(t, want, IsSeqExcluded(seq, ranges), "seq=%d", seq)
	}
}

func TestApplySegmentMask_NoLifecycle(t *testing.T) {
	entries := makeEntries(10)
	got := ApplySegmentMask(entries, nil)
	assert.Len(t, got, 10)
	for i, e := range got {
		assert.Equal(t, contentFor(i), e.Content)
	}
}

func TestApplySegmentMask_SinglePause(t *testing.T) {
	entries := makeEntries(10)
	lc := []LifecycleEvent{
		{Action: LifecycleActionPause, Seq: 3},
		{Action: LifecycleActionResume, Seq: 7},
	}
	got := ApplySegmentMask(entries, lc)
	require.Len(t, got, 6, "exclude indexes 3,4,5,6 → 4 entries dropped from 10")
	assert.Equal(t, contentFor(0), got[0].Content)
	assert.Equal(t, contentFor(2), got[2].Content)
	assert.Equal(t, contentFor(7), got[3].Content, "first kept after resume is index 7")
	assert.Equal(t, contentFor(9), got[5].Content)
}

func TestApplySegmentMask_OpenEndedPauseExcludesToEOS(t *testing.T) {
	entries := makeEntries(10)
	lc := []LifecycleEvent{
		{Action: LifecycleActionPause, Seq: 6},
	}
	got := ApplySegmentMask(entries, lc)
	require.Len(t, got, 6)
	for i := 0; i < 6; i++ {
		assert.Equal(t, contentFor(i), got[i].Content)
	}
}

func TestApplySegmentMask_MultipleRanges(t *testing.T) {
	entries := makeEntries(20)
	lc := []LifecycleEvent{
		{Action: LifecycleActionPause, Seq: 3},
		{Action: LifecycleActionResume, Seq: 5},
		{Action: LifecycleActionPause, Seq: 12},
		{Action: LifecycleActionResume, Seq: 15},
	}
	got := ApplySegmentMask(entries, lc)
	// excluded indices: 3,4 (2 dropped) and 12,13,14 (3 dropped) → 15 remain
	require.Len(t, got, 15)
	// pick a few representative entries to verify ordering
	assert.Equal(t, contentFor(0), got[0].Content)
	assert.Equal(t, contentFor(2), got[2].Content)
	assert.Equal(t, contentFor(5), got[3].Content) // first kept after first resume
	assert.Equal(t, contentFor(11), got[9].Content)
	assert.Equal(t, contentFor(15), got[10].Content) // first kept after second resume
	assert.Equal(t, contentFor(19), got[14].Content)
}

func TestApplySegmentMask_DoesNotMutateInput(t *testing.T) {
	entries := makeEntries(5)
	lc := []LifecycleEvent{
		{Action: LifecycleActionPause, Seq: 1},
		{Action: LifecycleActionResume, Seq: 3},
	}
	_ = ApplySegmentMask(entries, lc)
	assert.Len(t, entries, 5, "input length unchanged")
	for i, e := range entries {
		assert.Equal(t, contentFor(i), e.Content, "input content unchanged at %d", i)
	}
}

func TestCountMaskedEntries(t *testing.T) {
	entries := makeEntries(10)
	lc := []LifecycleEvent{
		{Action: LifecycleActionPause, Seq: 3},
		{Action: LifecycleActionResume, Seq: 7},
	}
	assert.Equal(t, 4, CountMaskedEntries(entries, lc))
	assert.Equal(t, 0, CountMaskedEntries(entries, nil))
}

// TestApplySegmentMask_Property is a randomized property test verifying the
// invariant: ApplySegmentMask(entries, lc) returns exactly the entries whose
// 0-indexed position is NOT in any [pause_i, resume_i) range built from lc.
//
// We avoid a third-party generator (gopter / rapid) to keep dependencies
// minimal and run the loop with a fixed seed for determinism.
func TestApplySegmentMask_Property(t *testing.T) {
	const trials = 200
	rng := rand.New(rand.NewSource(1))
	now := time.Now()

	for trial := 0; trial < trials; trial++ {
		n := rng.Intn(50) + 1
		entries := makeEntries(n)

		// build a random lifecycle: 0..6 pause/resume events with seqs in [0,n].
		var lc []LifecycleEvent
		paused := false
		for k := rng.Intn(7); k > 0; k-- {
			seq := rng.Intn(n + 1)
			if paused {
				lc = append(lc, LifecycleEvent{Action: LifecycleActionResume, Seq: seq, At: now})
				paused = false
			} else {
				lc = append(lc, LifecycleEvent{Action: LifecycleActionPause, Seq: seq, At: now})
				paused = true
			}
		}

		ranges := BuildSegmentRanges(lc)
		got := ApplySegmentMask(entries, lc)

		// Recompute expected by linear scan.
		expected := make([]SessionEntry, 0, n)
		for i, e := range entries {
			if !IsSeqExcluded(i, ranges) {
				expected = append(expected, e)
			}
		}

		require.Len(t, got, len(expected), "trial=%d lc=%v", trial, lc)
		for i := range got {
			assert.Equalf(t, expected[i].Content, got[i].Content, "trial=%d i=%d", trial, i)
		}
	}
}

// TestCountMaskedEntriesByTotal_MatchesCountMaskedEntries — Failure
// prevented: the allocation-free variant drifts from CountMaskedEntries
// semantics (over/under-counting overlapping ranges, ignoring open-ended
// pauses, off-by-one on resume seq). Pin them to identical results across
// a few representative lifecycles.
func TestCountMaskedEntriesByTotal_MatchesCountMaskedEntries(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name      string
		total     int
		lifecycle []LifecycleEvent
	}{
		{
			name:      "no lifecycle",
			total:     100,
			lifecycle: nil,
		},
		{
			name:  "single closed pause [5, 12)",
			total: 100,
			lifecycle: []LifecycleEvent{
				{Action: LifecycleActionPause, Seq: 5, At: now},
				{Action: LifecycleActionResume, Seq: 12, At: now.Add(1 * time.Minute)},
			},
		},
		{
			name:  "open-ended pause clipped by total",
			total: 50,
			lifecycle: []LifecycleEvent{
				{Action: LifecycleActionPause, Seq: 20, At: now},
			},
		},
		{
			name:  "multiple non-overlapping pauses",
			total: 100,
			lifecycle: []LifecycleEvent{
				{Action: LifecycleActionPause, Seq: 5, At: now},
				{Action: LifecycleActionResume, Seq: 10, At: now.Add(1 * time.Minute)},
				{Action: LifecycleActionPause, Seq: 40, At: now.Add(2 * time.Minute)},
				{Action: LifecycleActionResume, Seq: 55, At: now.Add(3 * time.Minute)},
			},
		},
		{
			name:  "total smaller than highest seq",
			total: 8,
			lifecycle: []LifecycleEvent{
				{Action: LifecycleActionPause, Seq: 5, At: now},
				{Action: LifecycleActionResume, Seq: 12, At: now.Add(1 * time.Minute)},
			},
		},
		{
			name:      "zero total",
			total:     0,
			lifecycle: []LifecycleEvent{{Action: LifecycleActionPause, Seq: 0, At: now}},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := make([]SessionEntry, c.total)
			want := CountMaskedEntries(stub, c.lifecycle)
			got := CountMaskedEntriesByTotal(c.total, c.lifecycle)
			assert.Equalf(t, want, got, "alloc-free variant must match slice variant")
		})
	}
}
