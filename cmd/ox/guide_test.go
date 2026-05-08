package main

import (
	"strings"
	"testing"
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
