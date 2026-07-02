package ledgersearch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSearch_FindsPlanFeedbackByReviewerWords verifies a human's review notes
// are findable by asking — the sacred-data queryability contract. Failure
// prevented: feedback lands in the ledger but `ox query --local` can never
// surface it, so "what did the reviewer flag on the auth plan?" comes back
// empty and the words are effectively lost to recall.
func TestSearch_FindsPlanFeedbackByReviewerWords(t *testing.T) {
	ledger := t.TempDir()
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	planDir := filepath.Join(ledger, "data", "plans", "2026-07-01-auth-plan")
	fbDir := filepath.Join(planDir, "feedback")
	if err := os.MkdirAll(fbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	round := map[string]any{
		"slug":       "auth-plan",
		"reviewer":   "sam",
		"created_at": now.Add(-time.Hour),
		"items": []map[string]any{{
			"anchor": "habc12345", "section": "Token Handling", "label": "refresh flow",
			"status": "request-change", "note": "rotate the signing keys quarterly",
		}},
	}
	b, _ := json.Marshal(round)
	if err := os.WriteFile(filepath.Join(fbDir, "round-20260701-110000.000000000-aaaa.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	// resolutions.json must never be scanned as a round.
	if err := os.WriteFile(filepath.Join(fbDir, "resolutions.json"), []byte(`[]`), 0o644); err != nil {
		t.Fatal(err)
	}

	results, err := Search(Options{LedgerPath: ledger, Query: "signing keys quarterly", Now: now})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	found := false
	for _, r := range results {
		if r.DocType == "plan-feedback" {
			found = true
			if r.SourceID != "2026-07-01-auth-plan" {
				t.Errorf("feedback hit must carry the dated plan slug, got %q", r.SourceID)
			}
		}
	}
	if !found {
		t.Fatalf("reviewer's words not findable: %+v", results)
	}

	// reviewer name is part of the doc — "what did sam flag" works too.
	results, err = Search(Options{LedgerPath: ledger, Query: "sam request-change", Now: now})
	if err != nil || len(results) == 0 {
		t.Fatalf("reviewer+status search failed: %v %+v", err, results)
	}
}
