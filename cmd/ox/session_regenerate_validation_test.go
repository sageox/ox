package main

import (
	"strings"
	"testing"

	intsess "github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/pkg/sessionsummary"
)

// substantiveSummary builds a SummarizeResponse that passes both
// ValidateSummaryContent and ValidateSummaryRichness for a non-trivial
// session. Used as the positive-control fixture across the gate tests.
func substantiveSummary() *sessionsummary.SummarizeResponse {
	return &sessionsummary.SummarizeResponse{
		Title:   "Wire tokenopt + validation gates into ox session regenerate",
		Summary: "Threaded the live ox-session-stop pipeline through the regenerate path so Phase 2 LLM regen produces the same artifact quality the daemon enforces. Added IsStubSummary structural detector and validation-gate replacement.",
		KeyActions: []string{
			"Extracted needsHydration helper to detect LFS pointer stubs",
			"Wired writeOptimizedJSONLForSummary into regenerate before BuildSummaryPrompt",
			"Mirrored daemon validation gates (content + richness) into regenerate",
		},
		Outcome:    "success",
		AhaMoments: []sessionsummary.AhaMoment{{Seq: 1, Type: "decision"}},
	}
}

// TestApplyValidationGates_PassThroughOnSubstantive: positive control.
// A summary that satisfies both content and richness validators must
// be returned unchanged with no warning message.
func TestApplyValidationGates_PassThroughOnSubstantive(t *testing.T) {
	in := substantiveSummary()
	out, msg := applyValidationGates(in, 100)
	if msg != "" {
		t.Errorf("expected empty message on valid input; got %q", msg)
	}
	if out != in {
		t.Errorf("expected identity pass-through on valid input; got replacement")
	}
}

// TestApplyValidationGates_ContentFailureReplacesWithStub: when
// ValidateSummaryContent rejects (e.g. empty title — the ox-g5zw
// fingerprint), the gate must REPLACE the response with a failure-
// marker stub so the bad summary doesn't get persisted on the ledger.
//
// The replacement carries ScoreReason — that's the load-bearing
// distinction so internal/session.IsStubSummary recognizes it as a
// deliberate failure marker (which DOES write to disk) rather than a
// silent LocalSummary stub (which doesn't). Without ScoreReason set,
// WriteSessionArtifacts would skip writing summary.json/.md and the
// teammate-visible artifact would just go missing.
func TestApplyValidationGates_ContentFailureReplacesWithStub(t *testing.T) {
	in := substantiveSummary()
	in.Title = "" // tripping ValidateSummaryContent's title-required rule

	out, msg := applyValidationGates(in, 100)
	if out == in {
		t.Fatal("content-failure must REPLACE the response, not flag it; original would otherwise leak to ledger")
	}
	if msg == "" {
		t.Error("expected a non-empty message naming the validation failure")
	}
	if !strings.Contains(msg, "content validation failed") {
		t.Errorf("message should name content validation; got %q", msg)
	}
	if out.ScoreReason == "" {
		t.Error("ScoreReason must be set so IsStubSummary recognizes this as a failure marker, not a silent stub")
	}
	if intsess.IsStubSummary(out) {
		t.Errorf("ScoreReason-bearing failure marker must NOT match IsStubSummary (or its summary.json gets dropped); got IsStubSummary=true")
	}
}

// TestApplyValidationGates_RichnessFailureReplacesWithStub: same
// replacement contract for richness failures (the ox-0pxt fingerprint:
// missing key_actions on a non-trivial session).
func TestApplyValidationGates_RichnessFailureReplacesWithStub(t *testing.T) {
	in := substantiveSummary()
	in.KeyActions = nil // tripping ValidateSummaryRichness on a 100-entry session

	out, msg := applyValidationGates(in, 100)
	if out == in {
		t.Fatal("richness-failure must REPLACE the response, not flag it")
	}
	if !strings.Contains(msg, "richness validation failed") {
		t.Errorf("message should name richness validation; got %q", msg)
	}
	if out.ScoreReason == "" {
		t.Error("ScoreReason must be set so the failure marker survives WriteSessionArtifacts")
	}
	if intsess.IsStubSummary(out) {
		t.Errorf("richness-failure stub must persist (ScoreReason set); got IsStubSummary=true")
	}
}

// TestApplyValidationGates_TrivialSessionSkipsRichness: a thin summary
// on a TRIVIAL session (entry_count below RichnessMinEntries) must not
// be replaced — the validator deliberately exempts short sessions where
// there genuinely may not be 3 key_actions to enumerate.
//
// Without this carve-out, every quick one-turn-question session would
// fail richness and ship a failure marker instead of its real summary.
func TestApplyValidationGates_TrivialSessionSkipsRichness(t *testing.T) {
	in := substantiveSummary()
	in.KeyActions = nil
	in.AhaMoments = nil

	// entryCount well below RichnessMinEntries (=20) — richness skipped.
	out, msg := applyValidationGates(in, 5)
	if out != in {
		t.Errorf("trivial session must pass through unchanged; got replacement (msg=%q)", msg)
	}
	if msg != "" {
		t.Errorf("trivial session should produce no message; got %q", msg)
	}
}

// TestApplyValidationGates_ContentBeforeRichness: when both gates would
// fail, content runs first. This matters because content failures
// indicate something fundamentally broken (no title, contaminated text)
// — surfacing that diagnostic is more useful than burying it under a
// "missing key_actions" complaint.
func TestApplyValidationGates_ContentBeforeRichness(t *testing.T) {
	in := substantiveSummary()
	in.Title = ""      // content failure
	in.KeyActions = nil // richness failure too

	_, msg := applyValidationGates(in, 100)
	if !strings.Contains(msg, "content validation failed") {
		t.Errorf("content gate must run before richness; got %q", msg)
	}
	if strings.Contains(msg, "richness") {
		t.Errorf("only the first failing gate's message should surface; got %q", msg)
	}
}
