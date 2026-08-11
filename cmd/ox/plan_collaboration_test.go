package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/session"
)

// writeRawJSONL writes a synthetic recording raw.jsonl into dir and returns the
// recording state pointing at it.
func writeRawJSONL(t *testing.T, lines string) *session.RecordingState {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	return &session.RecordingState{SessionPath: dir}
}

// TestDeriveCollabSignals_Counts verifies the deterministic effort counts from a
// transcript: user prompts, tool calls, AskUserQuestion clarifications, and the
// span from first user turn to last entry.
// Failure prevented: collaboration-rigor signals are wrong, so any later
// judgment of plan thoughtfulness is built on bad counts.
func TestDeriveCollabSignals_Counts(t *testing.T) {
	raw := `{"type":"header","metadata":{"version":"1.0"}}
{"type":"user","ts":"2026-06-04T10:00:00Z","content":"do X"}
{"type":"assistant","ts":"2026-06-04T10:00:05Z","content":"thinking"}
{"type":"tool","ts":"2026-06-04T10:00:10Z","tool_name":"Grep"}
{"type":"tool","ts":"2026-06-04T10:01:00Z","tool_name":"mcp__conductor__AskUserQuestion"}
{"type":"user","ts":"2026-06-04T10:02:00Z","content":"answer"}
{"type":"user","ts":"2026-06-04T10:05:00Z","content":"go"}
{"type":"footer","closed_at":"2026-06-04T10:05:00Z"}
`
	got := deriveCollabSignals(writeRawJSONL(t, raw))
	if got == nil {
		t.Fatal("expected signals, got nil")
	}
	if got.UserPrompts != 3 {
		t.Errorf("UserPrompts = %d, want 3", got.UserPrompts)
	}
	if got.ToolCalls != 2 {
		t.Errorf("ToolCalls = %d, want 2", got.ToolCalls)
	}
	if got.AgentQuestions != 1 {
		t.Errorf("AgentQuestions = %d, want 1 (AskUserQuestion)", got.AgentQuestions)
	}
	// first user 10:00:00 → last entry 10:05:00 == 300s.
	if got.DurationSeconds != 300 {
		t.Errorf("DurationSeconds = %d, want 300", got.DurationSeconds)
	}
}

// TestDeriveCollabSignals_NoSignalSources verifies the all-empty cases return
// nil so the plan omits the collaboration block rather than writing all-zero.
func TestDeriveCollabSignals_NoSignalSources(t *testing.T) {
	// nil state
	if got := deriveCollabSignals(nil); got != nil {
		t.Errorf("nil state: want nil, got %+v", got)
	}
	// state with no raw.jsonl
	if got := deriveCollabSignals(&session.RecordingState{SessionPath: t.TempDir()}); got != nil {
		t.Errorf("missing raw.jsonl: want nil, got %+v", got)
	}
	// transcript with only assistant turns (nothing countable)
	raw := `{"type":"assistant","ts":"2026-06-04T10:00:00Z","content":"hi"}
{"type":"assistant","ts":"2026-06-04T10:00:05Z","content":"bye"}
`
	if got := deriveCollabSignals(writeRawJSONL(t, raw)); got != nil {
		t.Errorf("only-assistant transcript: want nil, got %+v", got)
	}
}

// TestAppendProducedPlan_NoRecordingNoOp verifies the reverse-link append is a
// safe no-op when there is no live recording.
// Failure prevented: a plan saved outside a recording errors instead of just
// skipping the reverse link.
func TestAppendProducedPlan_NoRecordingNoOp(t *testing.T) {
	// no recording state at all — the "saved outside a session" case.
	if err := appendProducedPlan(t.TempDir(), nil, "some-slug"); err != nil {
		t.Errorf("expected no-op nil, got %v", err)
	}
	// empty slug is a no-op even with live state.
	if err := appendProducedPlan(t.TempDir(), &session.RecordingState{SessionPath: t.TempDir()}, ""); err != nil {
		t.Errorf("empty slug: %v", err)
	}
}

// TestAppendProducedPlan_RecordsAndDedups pins the behavior the old
// agent-id-keyed signature could not express: the caller hands in the session it
// already resolved (possibly found by workspace, with no agent id anywhere), and
// the slug still lands on the accumulator.
//
// Asserts the FILE, not the struct. The reverse link reaches session stop — a
// different process — only through .recording.json, so an assertion on the
// in-memory slice would stay green with session.SaveRecordingState replaced by
// `return nil`, i.e. it could not catch the exact failure it names.
//
// Failure prevented: produced_plans stays empty for every plan saved without
// SAGEOX_AGENT_ID set — which is how it ended up unset on all 2887 sessions.
func TestAppendProducedPlan_RecordsAndDedups(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	rec := startFakeRecording(t, root, session.RecordingState{
		AgentID:   "Oxdedup",
		SessionID: "ses_01920000-0000-7000-8000-0000000dedup",
	})

	if err := appendProducedPlan(root, rec, "my-plan"); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if got := readRecordingFromDisk(t, rec.SessionPath).ProducedPlans; len(got) != 1 || got[0] != "my-plan" {
		t.Fatalf("on-disk produced_plans = %v, want [my-plan]", got)
	}

	// Second append models the second PROCESS: dedup is only real if the first
	// write was durable and is read back, which is why this reloads rather than
	// reusing the struct.
	fresh := readRecordingFromDisk(t, rec.SessionPath)
	if err := appendProducedPlan(root, fresh, "my-plan"); err != nil {
		t.Fatalf("dedup append: %v", err)
	}
	if got := readRecordingFromDisk(t, rec.SessionPath).ProducedPlans; len(got) != 1 {
		t.Errorf("dedup failed on disk, got %v", got)
	}
}

// TestResolvePlanProvenance_AuthorSurvivesMissingAgentID is the regression gate
// for the provenance early return: agent detection must not be a precondition
// for facts that do not come from the agent.
//
// Failure prevented: `if agentID == "" { return nil, nil }` at the top of
// resolvePlanProvenance discarded the author, the repo id AND the session on
// every plan saved without SAGEOX_AGENT_ID exported. Measured over 233 real
// plans: author populated on 5.2%.
//
// Asserts an EXACT seeded name, not merely non-empty. `AuthorName != ""` reads
// stronger than it is: it would stay green if the resolver were swapped for
// AttributionName, which writes the user's full name — declared PII — into a
// team-visible ledger artifact. Seeding the git identity in a sandboxed HOME
// also makes the test hermetic; the ambient version passed or failed on whether
// the machine happened to be logged in.
//
// The session half of the same early return is covered separately, in
// TestResolvePlanProvenance_SessionSurvivesMissingAgentID — asserting only the
// author here left that half unguarded, proven by mutation.
func TestResolvePlanProvenance_AuthorSurvivesMissingAgentID(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	t.Setenv("SAGEOX_AGENT_ID", "")
	gitConfig(t, root, "user.name", "Person A")
	gitConfig(t, root, "user.email", "person-a@example.com")

	prov, _ := resolvePlanProvenance(root)
	if prov == nil {
		t.Fatal("provenance is nil with no agent id: author was resolvable and got discarded")
	}
	if prov.AuthorName != "Person A." {
		t.Errorf("AuthorName = %q, want %q — the privacy-safe DisplayName, never the full Name (PII in a shared ledger)",
			prov.AuthorName, "Person A.")
	}
	// The agent-keyed fields SHOULD be empty here — this asserts the fix kept
	// the honest answer rather than inventing an agent to keep the struct full.
	if prov.AgentID != "" {
		t.Errorf("AgentID = %q, want empty with SAGEOX_AGENT_ID unset", prov.AgentID)
	}
}

// TestResolvePlanProvenance_UnattributableSaveStaysHonest pins the measurability
// contract. identity.ResolveAttribution never fails — DisplayName bottoms out at
// the literal "Anonymous" — so stamping it unconditionally would make an
// unattributable plan indistinguishable from an attributed one in the artifact,
// and "% of plans with an author" (the metric this whole fix is judged by) would
// read 100% forever regardless of whether attribution actually works.
//
// Red-first: set prov.AuthorName = attr.DisplayName unconditionally → AuthorName
// becomes "Anonymous" and this test fails.
func TestResolvePlanProvenance_UnattributableSaveStaysHonest(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	t.Setenv("SAGEOX_AGENT_ID", "")
	t.Setenv("SAGEOX_TOKEN", "")
	t.Setenv("USER", "")
	t.Setenv("USERNAME", "")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "nonexistent-gitconfig"))
	gitConfig(t, root, "user.name", "")
	gitConfig(t, root, "user.email", "")

	prov, _ := resolvePlanProvenance(root)
	if prov != nil && prov.AuthorName != "" {
		t.Errorf("AuthorName = %q with nothing resolvable, want empty — a name-shaped placeholder makes attribution coverage unmeasurable",
			prov.AuthorName)
	}
}
