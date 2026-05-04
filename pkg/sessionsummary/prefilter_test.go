package sessionsummary

import (
	"strings"
	"testing"
)

func mkEntry(typ, content string) Entry {
	return Entry{Type: typ, Content: content}
}

// TestPrefilter_RealSession_LetItThrough verifies the prefilter does NOT
// trigger on a normal substantive session. Failure prevented: the
// prefilter starts skipping real sessions, hiding actual work from the
// team ledger and breaking the cost-savings argument (we'd be saving
// tokens by losing summaries we wanted).
func TestPrefilter_RealSession_LetItThrough(t *testing.T) {
	entries := []Entry{
		mkEntry("user", "Help me investigate the daemon's session-finalize loop. It looks like sessions are getting stuck in pending."),
		mkEntry("assistant", "I'll start by looking at the workflow in session_finalize.go and the manager's queue logic."),
		mkEntry("tool", ""),
		mkEntry("assistant", "Found it — there's a race in the queue.Complete path."),
		mkEntry("tool", ""),
		mkEntry("assistant", "Fixed by adding a mutex around the dedup-key map. Tests pass."),
	}
	resp, skip := MaybeBuildSkipSummary(entries)
	if skip {
		t.Fatalf("prefilter triggered on substantive session; resp=%+v", resp)
	}
	if resp != nil {
		t.Errorf("expected nil response when not skipping, got %+v", resp)
	}
}

// TestPrefilter_TwoEntryUserAssistant_LetThrough verifies the change
// from minMeaningfulEntries=3 to =2 lets a substantive single-exchange
// Q&A through to the real LLM summarizer. Failure prevented: the
// prefilter over-fires on legitimate two-turn sessions (CodeRabbit
// flagged this on PR #583) and quietly suppresses summaries the user
// would have wanted.
//
// The session here has only one user prompt + one assistant response,
// but the user content is substantive (>= minUserContentChars) so none
// of the three low-value heuristics should fire.
func TestPrefilter_TwoEntryUserAssistant_LetThrough(t *testing.T) {
	entries := []Entry{
		mkEntry("user", "Walk me through the data flow when a session is finalized — specifically how the daemon coordinates LFS upload, ledger commit, and summary writeback."),
		mkEntry("assistant", "The daemon's session-finalize handler runs after the calling agent's hook fires; it reads the cached raw.jsonl, runs the summary LLM, and the CLI commits + pushes both the artifacts and the meta.json. Here's the sequence diagram..."),
	}
	resp, skip := MaybeBuildSkipSummary(entries)
	if skip {
		t.Fatalf("two-entry user→assistant session must NOT be prefiltered when content is substantive; resp=%+v", resp)
	}
	if resp != nil {
		t.Errorf("expected nil response when not skipping, got %+v", resp)
	}
}

// TestPrefilter_TooFewEntries verifies the entry-count floor. Failure
// prevented: a session with one user prompt and an empty agent (e.g.,
// the agent crashed before responding) sails through to an LLM call
// that hallucinates an aha_moments list from nothing.
func TestPrefilter_TooFewEntries(t *testing.T) {
	entries := []Entry{
		// One substantive user prompt, no assistant — agent died.
		mkEntry("user", "Investigate why my deploy is failing on the auth service. The error mentions JWT signature validation."),
	}
	resp, skip := MaybeBuildSkipSummary(entries)
	if !skip {
		t.Fatal("expected prefilter to trigger on single-entry session")
	}
	if resp == nil || resp.Summary == "" {
		t.Fatalf("expected populated stub summary, got %+v", resp)
	}
	if !strings.Contains(resp.Summary, "JWT signature validation") {
		t.Errorf("expected user prompt echoed in summary, got: %q", resp.Summary)
	}
	if resp.QualityCategory != QualityCategorySkip {
		t.Errorf("QualityCategory=%q, want %q so EvaluateQualityCategory routes this to discard",
			resp.QualityCategory, QualityCategorySkip)
	}
	if resp.SummaryStatus != SummaryStatusOK {
		t.Errorf("SummaryStatus=%q, want %q (deterministic summary IS valid)", resp.SummaryStatus, SummaryStatusOK)
	}
}

// TestPrefilter_NoAssistant verifies sessions with zero assistant turns
// trigger the prefilter even when entry count is otherwise OK. Failure
// prevented: agent crash or hook failure produces a session that's
// "long" only in the system/header/tool entries — no actual response
// to the user. LLM summary of that is hallucination.
func TestPrefilter_NoAssistant(t *testing.T) {
	entries := []Entry{
		mkEntry("user", "Walk me through the codebase structure and explain how the daemon and CLI talk to each other through IPC."),
		mkEntry("system", "system reminder content"),
		mkEntry("tool", ""),
		mkEntry("tool", ""),
		mkEntry("user", "Are you still there?"),
	}
	_, skip := MaybeBuildSkipSummary(entries)
	if !skip {
		t.Fatal("expected prefilter to trigger when no assistant response present")
	}
}

// TestPrefilter_TerseUser verifies sessions where the user said almost
// nothing get skipped. Failure prevented: a one-word user prompt ("ok",
// "thanks") followed by some assistant chatter is not worth a real
// summary; we'd burn tokens producing one.
func TestPrefilter_TerseUser(t *testing.T) {
	entries := []Entry{
		mkEntry("user", "ok"),
		mkEntry("assistant", "anything else?"),
		mkEntry("user", "no"),
		mkEntry("assistant", "okay, ending session."),
	}
	resp, skip := MaybeBuildSkipSummary(entries)
	if !skip {
		t.Fatal("expected prefilter to trigger on near-empty user content")
	}
	// Both prompts should be echoed — they're under maxPromptsToEcho.
	if !strings.Contains(resp.Summary, "ok") || !strings.Contains(resp.Summary, "no") {
		t.Errorf("expected both prompts echoed in summary, got: %q", resp.Summary)
	}
}

// TestPrefilter_FewPromptsEchoedAsList verifies that with multiple short
// prompts (<= maxPromptsToEcho), the summary lists them all. Failure
// prevented: only the first prompt being echoed loses the user's
// actual sequence of asks, which is the only signal a low-value
// session HAS.
func TestPrefilter_FewPromptsEchoedAsList(t *testing.T) {
	entries := []Entry{
		mkEntry("user", "hi"),
		mkEntry("user", "what's the time"),
		mkEntry("user", "thanks"),
	}
	resp, skip := MaybeBuildSkipSummary(entries)
	if !skip {
		t.Fatal("expected prefilter to trigger")
	}
	want := []string{"hi", "what's the time", "thanks"}
	for _, w := range want {
		if !strings.Contains(resp.Summary, w) {
			t.Errorf("missing prompt %q in summary: %q", w, resp.Summary)
		}
	}
}

// TestPrefilter_ManyPromptsEchoFirst verifies that with > maxPromptsToEcho
// prompts, only the first is echoed (rest summarized as count). Failure
// prevented: long lists of prompts make the summary unreadable wall of
// text; the first prompt is usually where the intent is anyway.
func TestPrefilter_ManyPromptsEchoFirst(t *testing.T) {
	entries := []Entry{
		mkEntry("user", "first prompt with the original intent expressed clearly"),
		mkEntry("user", "second"),
		mkEntry("user", "third"),
		mkEntry("user", "fourth"),
		mkEntry("user", "fifth"),
		// no assistant — triggers silent criterion
	}
	resp, skip := MaybeBuildSkipSummary(entries)
	if !skip {
		t.Fatal("expected prefilter to trigger")
	}
	if !strings.Contains(resp.Summary, "first prompt with the original intent") {
		t.Errorf("first prompt not echoed: %q", resp.Summary)
	}
	if !strings.Contains(resp.Summary, "plus 4 more") {
		t.Errorf("expected 'plus 4 more' note, got: %q", resp.Summary)
	}
}

// TestPrefilter_EmptySession verifies the empty-entries case returns
// (nil, false). The empty case is handled by the caller (it has its
// own existing path); we don't claim it.
func TestPrefilter_EmptySession(t *testing.T) {
	resp, skip := MaybeBuildSkipSummary(nil)
	if skip || resp != nil {
		t.Errorf("expected (nil, false) for empty session, got resp=%+v skip=%v", resp, skip)
	}
}

// TestPrefilter_QualityCategorySkip verifies the synthesized summary's
// quality_category is "skip" so EvaluateQualityCategory routes it to
// QualityDiscard. Failure prevented: prefilter-generated stubs leak
// onto the team ledger because their category is treated as upload-
// worthy. This test pins the contract: prefilter outputs always
// flag as skip.
func TestPrefilter_QualityCategorySkip(t *testing.T) {
	entries := []Entry{mkEntry("user", "hi")}
	resp, skip := MaybeBuildSkipSummary(entries)
	if !skip {
		t.Fatal("expected skip")
	}
	if resp.QualityCategory != QualityCategorySkip {
		t.Errorf("QualityCategory=%q, want %q so it routes to QualityDiscard",
			resp.QualityCategory, QualityCategorySkip)
	}
	if !IsSkipCategory(resp) {
		t.Error("IsSkipCategory(resp) returned false on a prefilter-generated stub")
	}
}
