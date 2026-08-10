// Package adaptertest is the shared conformance suite every session adapter
// runs against a transcript the real coding agent wrote.
//
// It exists because six adapters shipped parsers for formats their agent never
// emitted, and every one of them had a green test suite. Those suites were
// green because their fixtures were hand-authored in the same guessed format
// the parser expected — self-consistent, and therefore incapable of failing.
//
// The rule this package enforces: a fixture must be real agent output, and the
// assertions must be strong enough that deleting the parser turns the test red.
// An adapter with no real fixture does not quietly pass — it calls Unproven and
// says out loud what evidence is missing.
//
// The rules live in Check, which is pure and returns Problems. Run is a thin
// *testing.T wrapper over it. That split is deliberate: a conformance harness
// nobody has watched fail is a harness nobody has tested, and pure rules let
// this package's own tests prove each rule fires.
package adaptertest

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// Rule names each conformance check, so a failure says which contract broke
// and this package's self-test can assert on the specific rule.
type Rule string

const (
	RuleProvenance      Rule = "fixture_provenance"
	RuleNonEmpty        Rule = "reads_a_real_transcript"
	RuleRoles           Rule = "conversation_roles_survive"
	RuleContent         Rule = "content_is_not_blank"
	RuleToolCalls       Rule = "tool_calls_are_identified"
	RuleToolPairing     Rule = "tool_results_pair_to_their_call"
	RuleToolErrors      Rule = "failed_tools_are_flagged"
	RuleTimestamps      Rule = "timestamps_are_sane"
	RuleResumeExact     Rule = "incremental_resume_is_exact"
	RuleUnprovenIsNamed Rule = "unproven_gaps_are_named"
	RuleSuiteWiring     Rule = "suite_is_wired"
)

// Problem is one conformance violation.
type Problem struct {
	Rule    Rule
	Message string
}

func (p Problem) String() string { return string(p.Rule) + ": " + p.Message }

// Suite describes how to exercise one adapter's reader against its fixture.
//
// The closures exist because offsets are adapter-specific: JSONL adapters use a
// byte offset, SQLite adapters use a row count. The suite never interprets an
// offset — it only requires that resuming at one yields the tail of the whole.
type Suite struct {
	// Adapter is the canonical adapter name, used in failure messages.
	Adapter string

	// Provenance says where the fixture came from and when. Required, and
	// checked — a fixture nobody can trace back to a real agent run is the
	// thing this package exists to prevent.
	//
	// e.g. "captured from pi 0.64.0, ~/.pi/agent/sessions, 2026-04-04, paths anonymized"
	Provenance string

	// ReadAll returns every entry the adapter extracts from the fixture.
	ReadAll func() ([]adapterprotocol.RawEntry, error)

	// ReadFrom resumes at an opaque offset. Optional: when nil the incremental
	// rules are reported as unproven rather than silently skipped.
	ReadFrom func(offset int64) (entries []adapterprotocol.RawEntry, newOffset int64, err error)

	// EndOffset is the offset meaning "everything has been read". Required
	// when ReadFrom is set.
	EndOffset func() (int64, error)

	// ResumePoints are offsets partway through the fixture. Each must skip at
	// least one entry and yield exactly the tail of ReadAll — together those
	// catch an incremental reader that duplicates or skips turns across a
	// chunk boundary.
	//
	// The "skips at least one" requirement is load-bearing: a reader that
	// ignores the offset and replays everything also satisfies "is a suffix",
	// so the suffix check alone cannot see it.
	ResumePoints func() ([]int64, error)

	Want Want
}

// Want is the minimum the fixture must prove. Zero means "not asserted", so
// every non-zero field is a claim someone deliberately made about real data.
//
// Set the fields your fixture actually contains. Overstating them makes the
// test fail; understating them is how coverage quietly rots, so prefer the
// true counts over round numbers.
type Want struct {
	MinEntries     int
	UserTurns      int
	AssistantTurns int
	ToolCalls      int
	ToolResults    int

	// PairedResults is the number of tool results that must resolve to a tool
	// call with the same call id. This is the assertion that caught codex
	// dropping 54-91% of its pairs while still returning plenty of entries.
	PairedResults int

	// ErroredResults is the number of entries that must carry is_error.
	// Goose reported every failed command as a success until this was checked.
	ErroredResults int

	// Unproven names code paths this fixture cannot exercise, e.g. "tool calls
	// — no real transcript on any dev machine contains one". Reported on every
	// run so the gap stays visible instead of being rediscovered by an outage.
	Unproven []string
}

// Run executes the conformance suite against *testing.T. Call it from the
// adapter's own package so the fixture and the parser stay in one place.
func Run(t *testing.T, s Suite) {
	t.Helper()

	problems := Check(s)

	byRule := map[Rule][]Problem{}
	for _, p := range problems {
		byRule[p.Rule] = append(byRule[p.Rule], p)
	}

	for _, rule := range allRules() {
		found := byRule[rule]
		t.Run(string(rule), func(t *testing.T) {
			for _, p := range found {
				t.Errorf("%s: %s", s.Adapter, p.Message)
			}
		})
	}

	for _, gap := range s.Want.Unproven {
		if strings.TrimSpace(gap) != "" {
			t.Logf("%s: UNPROVEN — %s", s.Adapter, gap)
		}
	}
}

func allRules() []Rule {
	return []Rule{
		RuleSuiteWiring, RuleProvenance, RuleNonEmpty, RuleRoles, RuleContent,
		RuleToolCalls, RuleToolPairing, RuleToolErrors, RuleTimestamps,
		RuleResumeExact, RuleUnprovenIsNamed,
	}
}

// Check runs every conformance rule and returns what failed. Pure: no
// *testing.T, so this package's own tests can feed it a deliberately broken
// adapter and assert the right rule fires.
func Check(s Suite) []Problem {
	var problems []Problem
	add := func(rule Rule, format string, args ...any) {
		problems = append(problems, Problem{Rule: rule, Message: fmt.Sprintf(format, args...)})
	}

	if s.Adapter == "" {
		add(RuleSuiteWiring, "Suite.Adapter is required")
	}
	if s.ReadAll == nil {
		add(RuleSuiteWiring, "Suite.ReadAll is required")
		return problems
	}
	if strings.TrimSpace(s.Provenance) == "" {
		add(RuleProvenance, "Suite.Provenance is required — a fixture that cannot be traced to a real agent run is exactly what this suite exists to reject")
	}

	entries, err := s.ReadAll()
	if err != nil {
		add(RuleNonEmpty, "reading the fixture failed: %v", err)
		return problems
	}

	problems = append(problems, checkShape(s, entries)...)
	problems = append(problems, checkTools(s, entries)...)
	problems = append(problems, checkTimestamps(s, entries)...)
	problems = append(problems, checkResume(s, entries)...)

	for _, gap := range s.Want.Unproven {
		if strings.TrimSpace(gap) == "" {
			add(RuleUnprovenIsNamed, "Want.Unproven contains an empty entry — name the gap or remove it")
		}
	}

	return problems
}

func checkShape(s Suite, entries []adapterprotocol.RawEntry) []Problem {
	var problems []Problem
	add := func(rule Rule, format string, args ...any) {
		problems = append(problems, Problem{Rule: rule, Message: fmt.Sprintf(format, args...)})
	}

	if len(entries) == 0 {
		add(RuleNonEmpty, "real transcript produced zero entries — the parser does not match what the agent writes (fixture: %s)", s.Provenance)
		return problems
	}
	if len(entries) < s.Want.MinEntries {
		add(RuleNonEmpty, "got %d entries, want at least %d", len(entries), s.Want.MinEntries)
	}

	if n := countRole(entries, "user"); n < s.Want.UserTurns {
		add(RuleRoles, "got %d user turns, want at least %d", n, s.Want.UserTurns)
	}
	if n := countRole(entries, "assistant"); n < s.Want.AssistantTurns {
		add(RuleRoles, "got %d assistant turns, want at least %d", n, s.Want.AssistantTurns)
	}

	// an adapter that emits the right number of entries with empty bodies
	// looks healthy in a count-only assertion and is useless in a ledger
	for i, e := range entries {
		if e.Role != "user" && e.Role != "assistant" {
			continue
		}
		if strings.TrimSpace(e.Content) == "" {
			add(RuleContent, "entry %d (role %s) has blank content", i, e.Role)
		}
	}

	return problems
}

func checkTools(s Suite, entries []adapterprotocol.RawEntry) []Problem {
	var problems []Problem
	add := func(rule Rule, format string, args ...any) {
		problems = append(problems, Problem{Rule: rule, Message: fmt.Sprintf(format, args...)})
	}

	calls := toolCalls(entries)
	if len(calls) < s.Want.ToolCalls {
		add(RuleToolCalls, "got %d tool calls, want at least %d", len(calls), s.Want.ToolCalls)
	}
	for i, e := range calls {
		if e.ToolName == "" {
			add(RuleToolCalls, "tool call %d has no tool_name — a nameless call is unreadable in the ledger", i)
		}
		if e.CallID == "" {
			add(RuleToolCalls, "tool call %d (%s) has no call_id — nothing can pair a result to it", i, e.ToolName)
		}
	}

	results := toolResults(entries)
	if len(results) < s.Want.ToolResults {
		add(RuleToolPairing, "got %d tool results, want at least %d", len(results), s.Want.ToolResults)
	}

	callIDs := map[string]bool{}
	for _, e := range calls {
		if e.CallID != "" {
			callIDs[e.CallID] = true
		}
	}

	var paired int
	for i, e := range results {
		if e.CallID == "" {
			add(RuleToolPairing, "tool result %d is an orphan with no call_id", i)
			continue
		}
		if callIDs[e.CallID] {
			paired++
		}
	}
	if paired < s.Want.PairedResults {
		add(RuleToolPairing, "only %d of %d tool results paired to a call, want at least %d — results that never pair surface as nameless blobs",
			paired, len(results), s.Want.PairedResults)
	}

	if s.Want.ErroredResults > 0 {
		var errored int
		for _, e := range entries {
			if e.IsError {
				errored++
			}
		}
		if errored < s.Want.ErroredResults {
			add(RuleToolErrors, "%d entries flagged is_error, want at least %d — a failed tool recorded as a success misleads every later reader",
				errored, s.Want.ErroredResults)
		}
	}

	return problems
}

func checkTimestamps(_ Suite, entries []adapterprotocol.RawEntry) []Problem {
	var problems []Problem
	add := func(format string, args ...any) {
		problems = append(problems, Problem{Rule: RuleTimestamps, Message: fmt.Sprintf(format, args...)})
	}

	// misreading a millisecond field as seconds lands ~54,000 years out,
	// and misreading seconds as milliseconds lands in 1970
	lower := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	upper := time.Now().AddDate(1, 0, 0)

	for i, e := range entries {
		ts, err := time.Parse(time.RFC3339, e.Timestamp)
		if err != nil {
			add("entry %d timestamp %q does not parse as RFC3339: %v", i, e.Timestamp, err)
			continue
		}
		if ts.Before(lower) || ts.After(upper) {
			add("entry %d timestamp %s is outside any plausible range — unit mismatch?", i, ts)
		}
	}

	return problems
}

// checkResume proves the resume contract: reading from a mid-transcript offset
// must yield exactly the tail of a full read. Anything else is either a
// duplicated turn (the ledger double-counts) or a dropped one (it vanishes).
func checkResume(s Suite, full []adapterprotocol.RawEntry) []Problem {
	var problems []Problem
	add := func(format string, args ...any) {
		problems = append(problems, Problem{Rule: RuleResumeExact, Message: fmt.Sprintf(format, args...)})
	}

	if s.ReadFrom == nil {
		return nil // incremental reading is not claimed; nothing to prove
	}
	if s.EndOffset == nil {
		add("ReadFrom is set but EndOffset is not")
		return problems
	}

	end, err := s.EndOffset()
	if err != nil {
		add("EndOffset failed: %v", err)
		return problems
	}

	if got, _, err := s.ReadFrom(0); err != nil {
		add("read from offset 0 failed: %v", err)
	} else if !reflect.DeepEqual(got, full) {
		add("read from offset 0 returned %d entries, full read returned %d — the two paths disagree", len(got), len(full))
	}

	if got, _, err := s.ReadFrom(end); err != nil {
		add("read from the end offset failed: %v", err)
	} else if len(got) != 0 {
		add("read from the end offset returned %d entries — a live poll would re-emit them forever", len(got))
	}

	if s.ResumePoints == nil {
		return problems
	}
	points, err := s.ResumePoints()
	if err != nil {
		add("ResumePoints failed: %v", err)
		return problems
	}

	for i, offset := range points {
		got, newOffset, err := s.ReadFrom(offset)
		if err != nil {
			add("resume point %d (offset %d) failed: %v", i, offset, err)
			continue
		}
		// An empty result is a suffix of anything, so the DeepEqual below
		// accepts it whenever the reader returns a non-nil empty slice — and
		// whether it does is an allocation detail, not a behavior. A reader
		// that returns nothing at every offset stops recording after the first
		// chunk and looks perfectly healthy. Reject it explicitly.
		if len(got) == 0 {
			add("resume point %d (offset %d) returned nothing — a live poll would never see the rest of the transcript", i, offset)
			continue
		}

		// a mid-transcript resume point must skip something; a reader that
		// replays the whole transcript would otherwise pass the suffix check
		// below trivially, because the whole list is a suffix of itself
		if len(got) >= len(full) {
			add("resume point %d (offset %d) returned %d of %d entries — it skipped nothing, so the offset is being ignored",
				i, offset, len(got), len(full))
			continue
		}
		want := full[len(full)-len(got):]
		if !reflect.DeepEqual(got, want) {
			add("resume point %d (offset %d) did not return the tail of the full read — entries are duplicated or skipped\n  got:  %s\n  want: %s",
				i, offset, summarize(got), summarize(want))
		}
		// A stalled offset hurts more than a backwards one: the reader returns
		// the correct tail and reports the same position, so every subsequent
		// poll re-emits those entries and the ledger duplicates without bound.
		if newOffset <= offset {
			add("resume point %d returned %d entries but did not advance the offset (%d -> %d) — every later poll re-emits them",
				i, len(got), offset, newOffset)
		}
	}

	return problems
}

// Unproven marks an adapter that has no real fixture yet. It skips loudly with
// the reason, so the gap is visible in test output instead of looking like
// coverage that already exists.
func Unproven(t *testing.T, adapter, reason string) {
	t.Helper()
	if strings.TrimSpace(reason) == "" {
		t.Fatalf("%s: Unproven requires a reason naming the evidence that is missing", adapter)
	}
	t.Skipf("%s: UNPROVEN against real agent output — %s", adapter, reason)
}

func countRole(entries []adapterprotocol.RawEntry, role string) int {
	var n int
	for _, e := range entries {
		if e.Role == role {
			n++
		}
	}
	return n
}

// toolCalls and toolResults split the tool role by which side of the exchange
// an entry carries. An entry with neither is malformed and shows up as an
// orphan in the pairing check.
func toolCalls(entries []adapterprotocol.RawEntry) []adapterprotocol.RawEntry {
	var out []adapterprotocol.RawEntry
	for _, e := range entries {
		if e.ToolName != "" && e.ToolOutput == "" {
			out = append(out, e)
		}
	}
	return out
}

func toolResults(entries []adapterprotocol.RawEntry) []adapterprotocol.RawEntry {
	var out []adapterprotocol.RawEntry
	for _, e := range entries {
		if e.ToolOutput != "" {
			out = append(out, e)
		}
	}
	return out
}

func summarize(entries []adapterprotocol.RawEntry) string {
	var b strings.Builder
	b.WriteString("[")
	for i, e := range entries {
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString(e.Role)
		if e.ToolName != "" {
			b.WriteString(":" + e.ToolName)
		}
	}
	b.WriteString("]")
	return b.String()
}
