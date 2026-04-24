package summaryeval

import (
	"strings"
	"testing"
)

// TestLoadCorpus_Curated verifies the on-disk golden corpus under
// testdata/corpus parses cleanly and contains the expected 18 sessions.
// Failure prevented: a malformed reference.json or an accidentally-deleted
// session directory silently reduces the eval's coverage without anyone
// noticing.
func TestLoadCorpus_Curated(t *testing.T) {
	corpus, err := LoadCorpus("testdata/corpus")
	if err != nil {
		t.Fatalf("load curated corpus: %v", err)
	}

	const want = 18
	if len(corpus) != want {
		names := make([]string, 0, len(corpus))
		for _, g := range corpus {
			names = append(names, g.Name)
		}
		t.Fatalf("expected %d curated sessions, got %d: %v", want, len(corpus), names)
	}

	// Every reference must have the fields scoring depends on. A silently
	// empty title or summary would score zero against any candidate and
	// skew the eval.
	validOutcomes := map[string]bool{"success": true, "partial": true, "failed": true}
	for _, g := range corpus {
		if strings.TrimSpace(g.Reference.Title) == "" {
			t.Errorf("%s: empty title", g.Name)
		}
		if strings.TrimSpace(g.Reference.Summary) == "" {
			t.Errorf("%s: empty summary", g.Name)
		}
		if len(g.Reference.KeyActions) < 3 {
			t.Errorf("%s: only %d key_actions (<3)", g.Name, len(g.Reference.KeyActions))
		}
		if !validOutcomes[g.Reference.Outcome] {
			t.Errorf("%s: invalid outcome %q", g.Name, g.Reference.Outcome)
		}
	}
}
