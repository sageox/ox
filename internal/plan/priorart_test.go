package plan

import (
	"context"
	"testing"

	"github.com/sageox/ox/internal/ledgersearch"
)

func TestDeriveQuery(t *testing.T) {
	tests := []struct {
		name string
		in   Input
		// want is a set of keywords that MUST appear; order-independent since
		// frequency ranking is exercised separately.
		wantContains []string
		wantEmpty    bool
	}{
		{
			name:      "empty plan yields empty query",
			in:        Input{},
			wantEmpty: true,
		},
		{
			name: "only stopwords yields empty query",
			in: Input{Sections: []Section{
				{Heading: "Add the new feature"},
			}},
			wantEmpty: true,
		},
		{
			name: "headings drive the query, stopwords dropped",
			in: Input{Sections: []Section{
				{Heading: "Implement OAuth token refresh"},
				{Heading: "Cache the refreshed credentials"},
			}},
			wantContains: []string{"oauth", "token", "refresh", "cache", "credentials"},
		},
		{
			name: "preamble title is included",
			in: Input{Sections: []Section{
				{Heading: "", Body: "Redesign the ledger sparse checkout"},
			}},
			wantContains: []string{"redesign", "ledger", "sparse", "checkout"},
		},
		{
			name: "short tokens filtered",
			in: Input{Sections: []Section{
				{Heading: "io db ui pipeline migration"},
			}},
			wantContains: []string{"pipeline", "migration"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deriveQuery(tt.in)
			if tt.wantEmpty {
				if got != "" {
					t.Fatalf("deriveQuery() = %q, want empty", got)
				}
				return
			}
			for _, kw := range tt.wantContains {
				if !containsToken(got, kw) {
					t.Errorf("deriveQuery() = %q, missing keyword %q", got, kw)
				}
			}
			// short tokens must never leak through
			for _, tok := range tokenizeWords(got) {
				if len(tok) < minKeyword {
					t.Errorf("deriveQuery() leaked short token %q in %q", tok, got)
				}
			}
		})
	}
}

func containsToken(query, tok string) bool {
	for _, t := range tokenizeWords(query) {
		if t == tok {
			return true
		}
	}
	return false
}

func TestExtractKeywordsFrequencyRanking(t *testing.T) {
	// "ledger" appears 3x, "session" 2x, "murmur" 1x — frequency ordering.
	text := "ledger session ledger murmur ledger session"
	got := extractKeywords(text)
	if len(got) == 0 {
		t.Fatal("extractKeywords returned nothing")
	}
	if got[0] != "ledger" {
		t.Errorf("most frequent term should lead: got[0]=%q, want ledger (full=%v)", got[0], got)
	}
}

func TestExtractKeywordsBoundsCount(t *testing.T) {
	// 12 distinct salient tokens; query must be capped so AND-match stays satisfiable.
	text := "alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima"
	got := extractKeywords(text)
	if len(got) > 8 {
		t.Errorf("extractKeywords returned %d terms, want <= 8 (%v)", len(got), got)
	}
}

func TestRankHitsThresholdAndCap(t *testing.T) {
	tests := []struct {
		name    string
		results []ledgersearch.Result
		wantIDs []string
	}{
		{
			name:    "no results",
			results: nil,
			wantIDs: nil,
		},
		{
			name: "weak matches thresholded out",
			results: []ledgersearch.Result{
				{Score: 0.5, SourceID: "weak-1", DocType: "session"},
				{Score: 0.55, SourceID: "weak-2", DocType: "session"},
			},
			wantIDs: nil,
		},
		{
			name: "strong match passes threshold",
			results: []ledgersearch.Result{
				{Score: 0.5, SourceID: "weak", DocType: "session"},
				{Score: 0.8, SourceID: "strong", DocType: "session"},
			},
			wantIDs: []string{"strong"},
		},
		{
			name: "sorted by score desc",
			results: []ledgersearch.Result{
				{Score: 0.7, SourceID: "mid", DocType: "session", CreatedAt: "2026-01-01T00:00:00Z"},
				{Score: 0.95, SourceID: "top", DocType: "session", CreatedAt: "2026-01-01T00:00:00Z"},
				{Score: 0.65, SourceID: "low", DocType: "session", CreatedAt: "2026-01-01T00:00:00Z"},
			},
			wantIDs: []string{"top", "mid", "low"},
		},
		{
			name: "capped at maxPriorArtHits",
			results: []ledgersearch.Result{
				{Score: 0.91, SourceID: "a", DocType: "session"},
				{Score: 0.92, SourceID: "b", DocType: "session"},
				{Score: 0.93, SourceID: "c", DocType: "session"},
				{Score: 0.94, SourceID: "d", DocType: "session"},
			},
			wantIDs: []string{"d", "c", "b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rankHits(tt.results)
			if len(got) != len(tt.wantIDs) {
				t.Fatalf("rankHits returned %d hits, want %d (%+v)", len(got), len(tt.wantIDs), got)
			}
			for i, id := range tt.wantIDs {
				if got[i].SourceID != id {
					t.Errorf("hit[%d].SourceID = %q, want %q", i, got[i].SourceID, id)
				}
			}
		})
	}
}

func TestParseSessionAuthorDate(t *testing.T) {
	tests := []struct {
		name       string
		sourceID   string
		createdAt  string
		wantAuthor string
		wantDate   string
	}{
		{
			name:       "well-formed session name",
			sourceID:   "2026-02-13T14-56-alice-OxAb12",
			createdAt:  "2026-02-13T14:56:00Z",
			wantAuthor: "alice",
			wantDate:   "2026-02-13",
		},
		{
			name:       "multi-segment username",
			sourceID:   "2026-03-01T09-00-bob-smith-OxCd34",
			createdAt:  "",
			wantAuthor: "bob-smith",
			wantDate:   "2026-03-01",
		},
		{
			name:       "murmur id yields no author, date from createdAt",
			sourceID:   "01HXYZmurmurid",
			createdAt:  "2026-04-10T12:00:00Z",
			wantAuthor: "",
			wantDate:   "2026-04-10",
		},
		{
			name:       "non-conforming name, no timestamp",
			sourceID:   "random",
			createdAt:  "",
			wantAuthor: "",
			wantDate:   "",
		},
		{
			// plan dir name "YYYY-MM-DD-<slug>": no author, date from prefix.
			name:       "plan dir name yields date from prefix, no author",
			sourceID:   "2026-05-21-cache-layer",
			createdAt:  "",
			wantAuthor: "",
			wantDate:   "2026-05-21",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			author, date := parseSessionAuthorDate(tt.sourceID, tt.createdAt)
			if author != tt.wantAuthor {
				t.Errorf("author = %q, want %q", author, tt.wantAuthor)
			}
			if date != tt.wantDate {
				t.Errorf("date = %q, want %q", date, tt.wantDate)
			}
		})
	}
}

func TestAnnotationFromHit(t *testing.T) {
	// The crisp label is "person · date · summary" — the slug must NOT appear in
	// Why (it's the locator in SourceURL and becomes the link). RefKind/Date/
	// Summary are the structured fields the renderer composes + links.
	tests := []struct {
		name           string
		hit            priorArtHit
		wantExpert     string
		wantSource     string
		wantRefKind    string
		wantDate       string
		wantSummaryNon bool // expect a non-empty Summary
		whyContains    []string
		whyOmits       []string
	}{
		{
			name: "session hit with author and date",
			hit: priorArtHit{
				Score: 0.9, DocType: "session",
				SourceID: "2026-02-13T14-56-alice-OxAb12",
				Author:   "alice", Date: "2026-02-13",
			},
			wantExpert:  "alice",
			wantSource:  "2026-02-13T14-56-alice-OxAb12",
			wantRefKind: "session",
			wantDate:    "2026-02-13",
			whyContains: []string{"alice", "2026-02-13"},
			// the slug must not be duplicated into the label
			whyOmits: []string{"OxAb12", "worked on"},
		},
		{
			name: "session hit with relevance summary",
			hit: priorArtHit{
				Score: 0.9, DocType: "session",
				SourceID: "2026-02-13T14-56-alice-OxAb12",
				Author:   "alice", Date: "2026-02-13",
				Text: "## the cache warming path for cold starts was added here later",
			},
			wantExpert:     "alice",
			wantSource:     "2026-02-13T14-56-alice-OxAb12",
			wantRefKind:    "session",
			wantDate:       "2026-02-13",
			wantSummaryNon: true,
			whyContains:    []string{"alice", "2026-02-13", "cache warming path"},
			// markdown header marker stripped; slug not present
			whyOmits: []string{"##", "OxAb12"},
		},
		{
			name: "murmur hit without author",
			hit: priorArtHit{
				Score: 0.7, DocType: "murmur", SourceID: "mid", Author: "", Date: "",
			},
			wantExpert:  "",
			wantSource:  "mid",
			wantRefKind: "murmur",
			whyContains: []string{"a teammate"},
			whyOmits:    []string{"mid", "mentioned"},
		},
		{
			name: "plan hit crisp label",
			hit: priorArtHit{
				Score: 0.85, DocType: "plan",
				SourceID: "2026-05-21-cache-layer", Author: "", Date: "2026-05-21",
			},
			wantExpert:  "",
			wantSource:  "2026-05-21-cache-layer",
			wantRefKind: "plan",
			wantDate:    "2026-05-21",
			whyContains: []string{"a teammate", "2026-05-21"},
			whyOmits:    []string{"cache-layer", "planned"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := annotationFromHit(tt.hit)
			if a.Type != BadgePriorArt {
				t.Errorf("Type = %q, want %q", a.Type, BadgePriorArt)
			}
			if a.Kind != BadgeDeterministic {
				t.Errorf("Kind = %q, want %q", a.Kind, BadgeDeterministic)
			}
			if a.Expert != tt.wantExpert {
				t.Errorf("Expert = %q, want %q", a.Expert, tt.wantExpert)
			}
			if a.SourceURL != tt.wantSource {
				t.Errorf("SourceURL = %q, want %q", a.SourceURL, tt.wantSource)
			}
			if a.RefKind != tt.wantRefKind {
				t.Errorf("RefKind = %q, want %q", a.RefKind, tt.wantRefKind)
			}
			if a.Date != tt.wantDate {
				t.Errorf("Date = %q, want %q", a.Date, tt.wantDate)
			}
			if (a.Summary != "") != tt.wantSummaryNon {
				t.Errorf("Summary = %q, wantNonEmpty=%v", a.Summary, tt.wantSummaryNon)
			}
			for _, frag := range tt.whyContains {
				if !contains(a.Why, frag) {
					t.Errorf("Why = %q, missing %q", a.Why, frag)
				}
			}
			for _, frag := range tt.whyOmits {
				if contains(a.Why, frag) {
					t.Errorf("Why = %q, should omit %q", a.Why, frag)
				}
			}
		})
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestDetectFailOpen verifies the detector returns (nil, nil) when there is no
// ledger to search — the core fail-open contract.
func TestDetectFailOpen(t *testing.T) {
	d := &priorArtDetector{}

	// empty plan: deriveQuery returns "", short-circuits before any path resolution.
	anns, err := d.Detect(context.Background(), Input{}, t.TempDir())
	if err != nil {
		t.Fatalf("Detect on empty plan returned error: %v", err)
	}
	if anns != nil {
		t.Errorf("Detect on empty plan returned %d annotations, want nil", len(anns))
	}

	// real query but uninitialized gitRoot: ledger path resolution fails open.
	in := Input{Sections: []Section{{Heading: "Implement OAuth token refresh"}}}
	anns, err = d.Detect(context.Background(), in, t.TempDir())
	if err != nil {
		t.Fatalf("Detect with no ledger returned error: %v", err)
	}
	if anns != nil {
		t.Errorf("Detect with no ledger returned %d annotations, want nil", len(anns))
	}

	// empty gitRoot also fails open.
	anns, err = d.Detect(context.Background(), in, "")
	if err != nil {
		t.Fatalf("Detect with empty gitRoot returned error: %v", err)
	}
	if anns != nil {
		t.Errorf("Detect with empty gitRoot returned %d annotations, want nil", len(anns))
	}
}

// TestDetectorRegistered confirms init() registered the detector exactly once
// under its canonical name.
func TestDetectorRegistered(t *testing.T) {
	ds, _ := snapshotRegistry()
	found := 0
	for _, d := range ds {
		if d.Name() == "prior-art" {
			found++
		}
	}
	if found == 0 {
		t.Fatal("prior-art detector not registered via init()")
	}
}
