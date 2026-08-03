package main

import (
	"strings"
	"testing"

	"github.com/sageox/ox/internal/prime"
)

func TestDiscoverGuides_BundledTopicsPresent(t *testing.T) {
	guides, err := discoverGuides()
	if err != nil {
		t.Fatalf("discoverGuides: %v", err)
	}

	// the plan calls for these five bundled topics
	wantTopics := map[string]bool{
		"team-rules":      false,
		"agents-md":       false,
		"team-context":    false,
		"murmur-vs-rule":  false,
		"getting-started": false,
	}

	for _, g := range guides {
		if _, ok := wantTopics[g.Topic]; ok {
			wantTopics[g.Topic] = true
		}
		if g.Title == "" {
			t.Errorf("guide %q has empty title", g.Topic)
		}
		if g.Description == "" {
			t.Errorf("guide %q has empty description", g.Topic)
		}
	}

	for topic, found := range wantTopics {
		if !found {
			t.Errorf("expected bundled guide %q to be present", topic)
		}
	}
}

func TestDiscoverGuides_SortedByTopic(t *testing.T) {
	guides, err := discoverGuides()
	if err != nil {
		t.Fatalf("discoverGuides: %v", err)
	}
	for i := 1; i < len(guides); i++ {
		if guides[i-1].Topic > guides[i].Topic {
			t.Errorf("guides not sorted: %s before %s", guides[i-1].Topic, guides[i].Topic)
		}
	}
}

func TestReadGuideFrontmatter_ExtractsTitleAndDescription(t *testing.T) {
	title, desc := readGuideFrontmatter("team-rules.md")
	if title == "" {
		t.Errorf("team-rules.md should have a title")
	}
	if desc == "" {
		t.Errorf("team-rules.md should have a description")
	}
	if !strings.Contains(strings.ToLower(title), "team rules") {
		t.Errorf("expected team-rules title to mention 'team rules', got %q", title)
	}
}

// TestGuideTopicsReferencedByPrime_Exist verifies every `ox guide <topic>`
// pointer embedded in prime's steering text resolves to a bundled guide.
//
// Prime's knowledge-bubble block is deliberately compressed and defers its
// long form to `ox guide knowledge-bubbles`; if that guide is renamed or
// dropped, the compression turns into a dead end — the agent is told where
// to read the detail and finds "unknown topic". This test is the only
// cross-check between the two packages (internal/prime cannot import
// cmd/ox), so it must live here.
func TestGuideTopicsReferencedByPrime_Exist(t *testing.T) {
	guides, err := discoverGuides()
	if err != nil {
		t.Fatalf("discoverGuides: %v", err)
	}

	bundled := make(map[string]bool, len(guides))
	for _, g := range guides {
		bundled[g.Topic] = true
	}

	// topic -> the prime text that points at it
	referenced := map[string]string{
		"knowledge-bubbles": "prime.KBGuidanceText (the <knowledge-bubbles> block)",
	}

	for topic, source := range referenced {
		if !bundled[topic] {
			t.Errorf("guide %q is referenced by %s but not bundled in cmd/ox/guides/", topic, source)
		}
	}
}

// TestKBGuidancePointsAtBundledGuide pins the pointer itself: prime's KB
// block must keep telling agents where the long form lives. Dropping the
// line would strand every detail migrated out of prime into the guide.
func TestKBGuidancePointsAtBundledGuide(t *testing.T) {
	if !strings.Contains(prime.KBGuidanceText, "ox guide knowledge-bubbles") {
		t.Errorf("prime KB guidance must point at `ox guide knowledge-bubbles`, got:\n%s", prime.KBGuidanceText)
	}
}
