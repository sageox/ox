package plan

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// These tests pin the consult-mode guard in Save: topic-only input (Raw
// deliberately empty — see newTopicInput) must never persist. Before the
// guard, `ox plan enrich --topic "…" --persist` minted a real pln_ id around
// a zero-byte plan.md — an empty, creator-less entry in the plan gallery.

func plansOnDisk(t *testing.T, ledger string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(ledger, "data", "plans"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read plans dir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestSave_TopicOnlyInputSkipped(t *testing.T) {
	ledger := t.TempDir()
	withLedger(t, ledger)

	in := newTopicInput("Remove buildCoRecordVTT and converge pipelines", nil)
	dir, _, err := Save("/fake/git/root", in, sampleResult(), nil, Meta{Topic: in.Topic})
	if !errors.Is(err, ErrNothingToSave) {
		t.Fatalf("Save err = %v, want ErrNothingToSave", err)
	}
	if dir != "" {
		t.Fatalf("Save dir = %q, want empty", dir)
	}
	if got := plansOnDisk(t, ledger); len(got) != 0 {
		t.Fatalf("plan dirs created for topic-only input: %v", got)
	}
}

func TestSave_WhitespaceOnlyRawSkipped(t *testing.T) {
	ledger := t.TempDir()
	withLedger(t, ledger)

	in := Input{Raw: "  \n\t\n"}
	_, _, err := Save("/fake/git/root", in, Result{}, nil, Meta{Topic: "Whitespace"})
	if !errors.Is(err, ErrNothingToSave) {
		t.Fatalf("Save err = %v, want ErrNothingToSave", err)
	}
	if got := plansOnDisk(t, ledger); len(got) != 0 {
		t.Fatalf("plan dirs created for whitespace-only input: %v", got)
	}
}

// HTML-primary saves are exempt from the guard: the page is the plan of
// record and Raw is only its derived markdown, which may legitimately be
// empty if extraction yields nothing.
func TestSave_EmptyRawWithHTMLStillSaves(t *testing.T) {
	ledger := t.TempDir()
	withLedger(t, ledger)

	html := []byte("<h1>Authored HTML Plan</h1>")
	dir, _, err := Save("/fake/git/root", Input{}, Result{}, html, Meta{Topic: "Authored HTML Plan"})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, planHTMLFile)); err != nil {
		t.Fatalf("plan.html not written for HTML-primary save: %v", err)
	}
}
