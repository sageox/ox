package adaptertest

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

// The suite's own job is to turn red when an adapter is broken. A conformance
// harness nobody has watched fail is a harness nobody has tested — so every
// rule below is fed a deliberately broken adapter, and the test asserts that
// the specific rule fires. Asserting the rule, not merely "something failed",
// is what keeps these from passing for the wrong reason.

func ts(offsetSec int) time.Time {
	return time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC).Add(time.Duration(offsetSec) * time.Second)
}

// goodTranscript is what a healthy adapter returns: a user turn, an assistant
// turn, and a tool call paired with its result.
func goodTranscript() []adapterprotocol.RawEntry {
	return []adapterprotocol.RawEntry{
		adapterruntime.UserEntry(ts(0), "read AGENTS.md"),
		adapterruntime.ToolUseWithID(ts(1), "read", `{"path":"AGENTS.md"}`, "call-1"),
		adapterruntime.ToolResultWithID(ts(2), "file contents", false, "call-1"),
		adapterruntime.AssistantEntry(ts(3), "here is the summary"),
	}
}

func suiteFor(entries []adapterprotocol.RawEntry) Suite {
	return Suite{
		Adapter:    "fixture",
		Provenance: "synthetic transcript owned by this package's own self-test",
		ReadAll:    func() ([]adapterprotocol.RawEntry, error) { return entries, nil },
		Want: Want{
			MinEntries:     4,
			UserTurns:      1,
			AssistantTurns: 1,
			ToolCalls:      1,
			ToolResults:    1,
			PairedResults:  1,
		},
	}
}

// assertFires runs Check and requires the named rule to be among the problems.
func assertFires(t *testing.T, s Suite, want Rule, why string) {
	t.Helper()
	problems := Check(s)
	for _, p := range problems {
		if p.Rule == want {
			return
		}
	}
	t.Errorf("rule %q did not fire — %s\nproblems: %s", want, why, render(problems))
}

func render(problems []Problem) string {
	if len(problems) == 0 {
		return "(none — the suite passed a broken adapter)"
	}
	var b strings.Builder
	for _, p := range problems {
		b.WriteString("\n  - " + p.String())
	}
	return b.String()
}

func TestCheck_PassesAHealthyAdapter(t *testing.T) {
	if problems := Check(suiteFor(goodTranscript())); len(problems) != 0 {
		t.Errorf("healthy adapter reported problems — the suite would block every correct adapter: %s", render(problems))
	}
}

// TestCheck_CatchesADeadParser is the headline case: every adapter this epic
// fixed returned zero entries against real data while its own tests were green.
func TestCheck_CatchesADeadParser(t *testing.T) {
	assertFires(t, suiteFor(nil), RuleNonEmpty,
		"an adapter that returns zero entries against real data is the exact bug this suite exists to catch")
}

func TestCheck_CatchesAReadError(t *testing.T) {
	s := suiteFor(goodTranscript())
	s.ReadAll = func() ([]adapterprotocol.RawEntry, error) { return nil, errors.New("no such table: sessions") }
	assertFires(t, s, RuleNonEmpty, "a reader that errors must not look like a pass")
}

func TestCheck_CatchesMissingConversationRoles(t *testing.T) {
	toolOnly := []adapterprotocol.RawEntry{
		adapterruntime.ToolUseWithID(ts(1), "read", "{}", "call-1"),
		adapterruntime.ToolResultWithID(ts(2), "out", false, "call-1"),
	}
	assertFires(t, suiteFor(toolOnly), RuleRoles, "a transcript with no user or assistant turns is not a conversation")
}

func TestCheck_CatchesBlankContent(t *testing.T) {
	blank := []adapterprotocol.RawEntry{
		adapterruntime.UserEntry(ts(0), "   "),
		adapterruntime.ToolUseWithID(ts(1), "read", "{}", "call-1"),
		adapterruntime.ToolResultWithID(ts(2), "out", false, "call-1"),
		adapterruntime.AssistantEntry(ts(3), "answer"),
	}
	assertFires(t, suiteFor(blank), RuleContent,
		"the right entry count with empty bodies is still a broken read")
}

func TestCheck_CatchesNamelessToolCall(t *testing.T) {
	entries := goodTranscript()
	entries[1].ToolName = ""
	assertFires(t, suiteFor(entries), RuleToolCalls, "a nameless tool call is unreadable in the ledger")
}

func TestCheck_CatchesToolCallWithNoCallID(t *testing.T) {
	entries := goodTranscript()
	entries[1].CallID = ""
	assertFires(t, suiteFor(entries), RuleToolCalls, "nothing can pair a result to a call with no id")
}

// TestCheck_CatchesUnpairedToolResult is the codex defect: plenty of entries,
// correct text, but results that never resolve to their call.
func TestCheck_CatchesUnpairedToolResult(t *testing.T) {
	entries := goodTranscript()
	entries[2].CallID = "some-other-call"
	assertFires(t, suiteFor(entries), RuleToolPairing,
		"results that never pair surface in the ledger as nameless blobs")
}

func TestCheck_CatchesOrphanResultWithNoCallID(t *testing.T) {
	entries := goodTranscript()
	entries[2].CallID = ""
	assertFires(t, suiteFor(entries), RuleToolPairing, "an orphan result with no call id cannot be attributed")
}

// TestCheck_CatchesUnflaggedToolFailure is the goose defect: a failed command
// recorded as a success.
func TestCheck_CatchesUnflaggedToolFailure(t *testing.T) {
	s := suiteFor(goodTranscript())
	s.Want.ErroredResults = 1 // the fixture claims a failing tool
	assertFires(t, s, RuleToolErrors, "a failed tool recorded as a success misleads every later reader")
}

// TestCheck_CatchesTimestampUnitMismatch is the OpenCode defect: a millisecond
// field read as seconds lands ~54,000 years out; the inverse lands in 1970.
func TestCheck_CatchesTimestampUnitMismatch(t *testing.T) {
	entries := goodTranscript()
	entries[0] = adapterruntime.UserEntry(time.UnixMilli(ts(0).Unix()).UTC(), "read AGENTS.md")
	assertFires(t, suiteFor(entries), RuleTimestamps, "a timestamp in 1970 means the units were misread")
}

func TestCheck_CatchesUnparseableTimestamp(t *testing.T) {
	entries := goodTranscript()
	entries[0].Timestamp = "not a timestamp"
	assertFires(t, suiteFor(entries), RuleTimestamps, "an unparseable timestamp must not pass")
}

func TestCheck_CatchesTooFewEntries(t *testing.T) {
	s := suiteFor(goodTranscript()[:2])
	assertFires(t, s, RuleNonEmpty, "a fixture that shrank below its declared minimum has lost coverage")
}

// --- incremental resume ---

func incrementalSuite(readFrom func(int64) ([]adapterprotocol.RawEntry, int64, error)) Suite {
	full := goodTranscript()
	s := suiteFor(full)
	s.ReadFrom = readFrom
	s.EndOffset = func() (int64, error) { return int64(len(full)), nil }
	s.ResumePoints = func() ([]int64, error) { return []int64{2}, nil }
	return s
}

// honestReader treats the offset as an entry index, which is the simplest
// stand-in for the byte offsets and row counts real adapters use.
func honestReader(full []adapterprotocol.RawEntry) func(int64) ([]adapterprotocol.RawEntry, int64, error) {
	return func(offset int64) ([]adapterprotocol.RawEntry, int64, error) {
		if offset >= int64(len(full)) {
			return nil, offset, nil
		}
		return full[offset:], int64(len(full)), nil
	}
}

func TestCheck_PassesAnExactIncrementalReader(t *testing.T) {
	if problems := Check(incrementalSuite(honestReader(goodTranscript()))); len(problems) != 0 {
		t.Errorf("an incremental reader that resumes exactly was rejected: %s", render(problems))
	}
}

// TestCheck_CatchesDuplicatingResume covers a reader that replays turns it has
// already emitted — the ledger double-counts them on every poll.
func TestCheck_CatchesDuplicatingResume(t *testing.T) {
	full := goodTranscript()
	duplicating := func(offset int64) ([]adapterprotocol.RawEntry, int64, error) {
		if offset >= int64(len(full)) {
			return nil, offset, nil
		}
		return full, int64(len(full)), nil // ignores the offset entirely
	}
	assertFires(t, incrementalSuite(duplicating), RuleResumeExact,
		"a reader that ignores the offset replays the whole transcript on every poll")
}

// TestCheck_CatchesSkippingResume covers the opposite failure: turns lost
// across a chunk boundary and never recorded at all. The reader returns the
// right COUNT of entries, so only a suffix comparison catches it.
func TestCheck_CatchesSkippingResume(t *testing.T) {
	full := goodTranscript()
	skipping := func(offset int64) ([]adapterprotocol.RawEntry, int64, error) {
		if offset >= int64(len(full)) {
			return nil, offset, nil
		}
		out := make([]adapterprotocol.RawEntry, len(full)-int(offset))
		copy(out, full[:len(out)])
		return out, int64(len(full)), nil
	}
	assertFires(t, incrementalSuite(skipping), RuleResumeExact,
		"the right number of wrong entries must not pass a length-only check")
}

func TestCheck_CatchesResumeAtEndReturningEntries(t *testing.T) {
	full := goodTranscript()
	neverDone := func(int64) ([]adapterprotocol.RawEntry, int64, error) {
		return full, int64(len(full)), nil
	}
	assertFires(t, incrementalSuite(neverDone), RuleResumeExact,
		"a reader that keeps returning entries past the end offset re-emits them forever")
}

func TestCheck_CatchesBackwardsOffset(t *testing.T) {
	full := goodTranscript()
	rewinds := func(offset int64) ([]adapterprotocol.RawEntry, int64, error) {
		if offset >= int64(len(full)) {
			return nil, offset, nil
		}
		return full[offset:], 0, nil // reports an offset before where it started
	}
	assertFires(t, incrementalSuite(rewinds), RuleResumeExact,
		"an offset that moves backwards makes the next poll re-read what it already emitted")
}

// TestCheck_CatchesAResumeThatReturnsNothing covers a reader that stops
// producing entries partway through. An empty slice is a suffix of any list, so
// the suffix comparison alone accepts it — and whether it does depends on
// whether the adapter returns nil or a pre-allocated empty slice, which is an
// allocation detail rather than a behavior.
func TestCheck_CatchesAResumeThatReturnsNothing(t *testing.T) {
	full := goodTranscript()
	empty := func(offset int64) ([]adapterprotocol.RawEntry, int64, error) {
		if offset == 0 {
			return full, int64(len(full)), nil
		}
		return []adapterprotocol.RawEntry{}, int64(len(full)), nil // non-nil, empty
	}
	assertFires(t, incrementalSuite(empty), RuleResumeExact,
		"a reader that returns nothing mid-transcript stops the recording and looks healthy")
}

// TestCheck_CatchesAStalledOffset covers the duplication case: correct entries,
// but the offset never advances, so every poll re-emits them.
func TestCheck_CatchesAStalledOffset(t *testing.T) {
	full := goodTranscript()
	stalled := func(offset int64) ([]adapterprotocol.RawEntry, int64, error) {
		if offset >= int64(len(full)) {
			return nil, offset, nil
		}
		return full[offset:], offset, nil // right tail, offset never moves
	}
	assertFires(t, incrementalSuite(stalled), RuleResumeExact,
		"an offset that never advances makes the ledger duplicate every turn on every poll")
}

func TestCheck_CatchesResumeReadError(t *testing.T) {
	failing := func(int64) ([]adapterprotocol.RawEntry, int64, error) {
		return nil, 0, errors.New("seek past end of file")
	}
	assertFires(t, incrementalSuite(failing), RuleResumeExact, "a failing resume must not look like a pass")
}

func TestCheck_RequiresEndOffsetWhenIncremental(t *testing.T) {
	s := incrementalSuite(honestReader(goodTranscript()))
	s.EndOffset = nil
	assertFires(t, s, RuleResumeExact, "claiming an incremental reader without an end offset is unprovable")
}

func TestCheck_SkipsResumeRulesWhenNotClaimed(t *testing.T) {
	s := suiteFor(goodTranscript()) // no ReadFrom
	for _, p := range Check(s) {
		if p.Rule == RuleResumeExact {
			t.Errorf("resume rules fired for an adapter that never claimed an incremental reader: %s", p)
		}
	}
}

// --- guardrails on the suite's own inputs ---

func TestCheck_RejectsAFixtureWithNoProvenance(t *testing.T) {
	s := suiteFor(goodTranscript())
	s.Provenance = "  "
	assertFires(t, s, RuleProvenance, "untraceable fixtures are how guessed formats ship")
}

func TestCheck_RejectsAnUnwiredSuite(t *testing.T) {
	assertFires(t, Suite{Adapter: "fixture", Provenance: "x"}, RuleSuiteWiring,
		"a suite with no reader proves nothing and must not pass")
}

func TestCheck_RejectsAnEmptyUnprovenGap(t *testing.T) {
	s := suiteFor(goodTranscript())
	s.Want.Unproven = []string{""}
	assertFires(t, s, RuleUnprovenIsNamed, "an unnamed coverage gap cannot be acted on")
}

func TestCheck_AcceptsANamedUnprovenGap(t *testing.T) {
	s := suiteFor(goodTranscript())
	s.Want.Unproven = []string{"tool errors — no real transcript on this machine contains a failing tool"}
	if problems := Check(s); len(problems) != 0 {
		t.Errorf("a named gap should be reported, not treated as a failure: %s", render(problems))
	}
}

func TestUnproven_SkipsLoudlyWithAReason(t *testing.T) {
	var skipped, failed bool
	t.Run("inner", func(inner *testing.T) {
		defer func() {
			skipped = inner.Skipped()
			failed = inner.Failed()
		}()
		Unproven(inner, "amp", "no amp install or transcript on any dev machine")
	})
	if failed || !skipped {
		t.Errorf("Unproven with a reason should skip loudly: skipped=%v failed=%v", skipped, failed)
	}
}
