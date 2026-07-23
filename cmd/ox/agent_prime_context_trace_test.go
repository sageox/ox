package main

import (
	"strings"
	"testing"

	"github.com/sageox/ox/internal/prime"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/teamdocs"
	"github.com/sageox/ox/internal/tokens"
)

func TestEmitProvidedContextTrace_FullContext(t *testing.T) {
	dir := t.TempDir()

	teamCtx := &prime.TeamContextInfo{
		TeamName:        "SageOx",
		MemoryContent:   "# Memory\nsome content",
		SoulHint:        "/path/to/SOUL.md",
		TeamHint:        "/path/to/TEAM.md",
		HasAgentContext: true,
		TeamDocs: []teamdocs.TeamDoc{
			{Name: "glossary.md"},
			{Name: "principles.md"},
		},
	}
	teamInstructions := &prime.TeamInstructions{
		TeamName: "SageOx",
		Files:    []string{"AGENTS.md", "CLAUDE.md"},
		Tokens:   1500,
	}

	emitProvidedContextTrace(dir, teamCtx, teamInstructions)

	events, err := contexttrace.ReadEvents(contexttrace.NewWriter(dir).Path())
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}

	// expected: instructions(1) + memory(1) + soul(1) + team(1) + agent-context(1) + docs(2) = 7
	if len(events) != 7 {
		t.Fatalf("got %d events, want 7", len(events))
	}

	// first event should be team instructions
	if events[0].Source != contexttrace.SourceTeamContext || len(events[0].Docs) != 2 {
		t.Errorf("event[0]: source=%q, docs=%v", events[0].Source, events[0].Docs)
	}
	if events[0].InlineTokens != 1500 {
		t.Errorf("event[0]: inline_tokens=%d, want 1500", events[0].InlineTokens)
	}

	// memory event
	if events[1].Source != contexttrace.SourceTeamMemory || events[1].Doc != "MEMORY.md" {
		t.Errorf("event[1]: source=%q, doc=%q", events[1].Source, events[1].Doc)
	}
	// InlineTokens must be populated (bd ox-32f6) — this was the one
	// "provided" event that always recorded inline_tokens=0.
	wantMemoryTokens := tokens.EstimateTokens(teamCtx.MemoryContent)
	if events[1].InlineTokens != wantMemoryTokens {
		t.Errorf("event[1]: inline_tokens=%d, want %d (tokens.EstimateTokens(MemoryContent))", events[1].InlineTokens, wantMemoryTokens)
	}
	if events[1].InlineTokens == 0 {
		t.Error("event[1]: inline_tokens=0 for non-empty MemoryContent — the exact regression this fix prevents")
	}

	// all events should have timestamps
	for i, ev := range events {
		if ev.Timestamp == "" {
			t.Errorf("event[%d]: missing timestamp", i)
		}
	}
}

func TestEmitProvidedContextTrace_NilTeamCtx(t *testing.T) {
	dir := t.TempDir()

	teamInstructions := &prime.TeamInstructions{
		TeamName: "SageOx",
		Files:    []string{"AGENTS.md"},
		Tokens:   800,
	}

	emitProvidedContextTrace(dir, nil, teamInstructions)

	events, err := contexttrace.ReadEvents(contexttrace.NewWriter(dir).Path())
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}

	// only team instructions event
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Source != contexttrace.SourceTeamContext {
		t.Errorf("source = %q, want team-context", events[0].Source)
	}
}

func TestEmitProvidedContextTrace_EmptySessionDir(t *testing.T) {
	// should return immediately without error
	emitProvidedContextTrace("", &prime.TeamContextInfo{TeamName: "test"}, nil)
	// no file created, nothing to verify — just ensures no panic
}

func TestEmitProvidedContextTrace_NoInstructions(t *testing.T) {
	dir := t.TempDir()

	teamCtx := &prime.TeamContextInfo{
		TeamName:      "SageOx",
		MemoryContent: "# Memory",
	}

	emitProvidedContextTrace(dir, teamCtx, nil)

	events, err := contexttrace.ReadEvents(contexttrace.NewWriter(dir).Path())
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}

	// only memory event (no instructions, no soul/team/agent-ctx/docs)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Doc != "MEMORY.md" {
		t.Errorf("doc = %q, want MEMORY.md", events[0].Doc)
	}
}

// TestEmitProvidedContextTrace_MemoryInlineTokens pins the MEMORY.md event's
// InlineTokens computation (bd ox-32f6). Before this fix, this was the one
// "provided" event that always recorded inline_tokens=0 across every trace
// — team memory is the most consistently-inlined content in prime output,
// so prime effectively could not see its own injection cost. Uses the same
// tokens.EstimateTokens heuristic as TeamInstructions.Tokens, so the two
// "provided" events stay directly comparable.
func TestEmitProvidedContextTrace_MemoryInlineTokens(t *testing.T) {
	dir := t.TempDir()
	content := strings.Repeat("word ", 100) // 500 chars, well above the len/4 rounding noise
	teamCtx := &prime.TeamContextInfo{TeamName: "SageOx", MemoryContent: content}

	emitProvidedContextTrace(dir, teamCtx, nil)

	events, err := contexttrace.ReadEvents(contexttrace.NewWriter(dir).Path())
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}

	want := tokens.EstimateTokens(content)
	if want == 0 {
		t.Fatal("test content must estimate to a non-zero token count — fixture bug, not a product bug")
	}
	if events[0].InlineTokens != want {
		t.Errorf("InlineTokens = %d, want %d (tokens.EstimateTokens(content))", events[0].InlineTokens, want)
	}
}

func TestEmitProvidedContextTrace_WithTeamDocs(t *testing.T) {
	dir := t.TempDir()

	teamCtx := &prime.TeamContextInfo{
		TeamName: "SageOx",
		TeamDocs: []teamdocs.TeamDoc{
			{Name: "glossary.md"},
			{Name: "principles.md"},
			{Name: "onboarding.md"},
		},
	}

	emitProvidedContextTrace(dir, teamCtx, nil)

	events, err := contexttrace.ReadEvents(contexttrace.NewWriter(dir).Path())
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}

	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}

	for i, ev := range events {
		if ev.Source != contexttrace.SourceTeamDocs {
			t.Errorf("event[%d]: source=%q, want team-docs", i, ev.Source)
		}
		if !ev.ReadOnDemand {
			t.Errorf("event[%d]: expected read_on_demand=true", i)
		}
	}

	// verify doc names
	names := []string{"glossary.md", "principles.md", "onboarding.md"}
	for i, name := range names {
		if events[i].Doc != name {
			t.Errorf("event[%d]: doc=%q, want %q", i, events[i].Doc, name)
		}
	}
}
