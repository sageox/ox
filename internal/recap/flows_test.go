package recap

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session/contexttrace"
)

// TestKnowledgeFlow_FlipsOnFlows: real flows flip Available true; no flows keeps
// the honest placeholder. Failure prevented: the section fakes chains it doesn't
// have, or hides ones it does.
func TestKnowledgeFlow_FlipsOnFlows(t *testing.T) {
	got := knowledgeFlow([]string{"You consulted a prior session — s1"})
	if !got.Available || len(got.Flows) != 1 {
		t.Fatalf("expected available with 1 flow, got %+v", got)
	}
	if got.Pending != "" {
		t.Error("available flow section must not carry the placeholder text")
	}

	empty := knowledgeFlow(nil)
	if empty.Available || empty.Pending == "" {
		t.Errorf("no flows must stay on the placeholder, got %+v", empty)
	}
}

// TestGatherFlows_ReadsConsultedEvents drives the render-wire end to end: a
// consulted event written into a session's trace surfaces as a graded flow line.
func TestGatherFlows_ReadsConsultedEvents(t *testing.T) {
	ledger := t.TempDir()
	name := "2026-07-01T10-00-ryan-Ox1"
	// Write the trace where readTrace resolves it first (the ledger cache).
	cacheDir := filepath.Join(ledger, ".sageox", "cache", "sessions", name)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	w := contexttrace.NewWriter(cacheDir)
	if err := w.Append(contexttrace.Event{
		Type:      contexttrace.EventConsulted,
		Mechanism: contexttrace.MechanismRetrieval,
		RefType:   "session",
		Ref:       "2026-06-01-maya-Ox2",
		Seq:       14,
	}); err != nil {
		t.Fatal(err)
	}
	// A plain provided event must NOT become a flow.
	_ = w.Append(contexttrace.Event{Type: contexttrace.EventProvided, Doc: "MEMORY.md"})

	sessions := []SessionFacts{{Name: name, Title: "Auth work", CreatedAt: time.Now()}}
	flows := gatherFlows(ledger, sessions)
	if len(flows) != 1 {
		t.Fatalf("expected 1 flow from the consulted event, got %d: %v", len(flows), flows)
	}
	if want := "Auth work"; !contains(flows[0], want) {
		t.Errorf("flow %q must carry the session receipt %q", flows[0], want)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
