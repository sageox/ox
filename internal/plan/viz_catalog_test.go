package plan

import (
	"strings"
	"testing"
)

// TestVizCatalog_ParsesWellFormed verifies the embedded catalog parses into
// patterns that each carry a use, a why, and a snippet body. Failure prevented:
// a malformed catalog ships empty/partial entries that give agents nothing to
// paste.
func TestVizCatalog_ParsesWellFormed(t *testing.T) {
	cat := VizCatalog()
	if len(cat) < 8 {
		t.Fatalf("expected a substantial catalog, got %d entries", len(cat))
	}
	for _, p := range cat {
		if p.ID == "" {
			t.Error("pattern with empty id")
		}
		if p.Use == "" {
			t.Errorf("pattern %q missing use:", p.ID)
		}
		if p.Why == "" {
			t.Errorf("pattern %q missing why:", p.ID)
		}
		if strings.TrimSpace(p.Body) == "" {
			t.Errorf("pattern %q has no snippet body", p.ID)
		}
	}
}

// TestVizCatalog_HasCorePatterns verifies the catalog covers the visualization
// vocabulary the request named (time series, dependency graphs, mockups,
// sparklines, Tufte density).
func TestVizCatalog_HasCorePatterns(t *testing.T) {
	want := []string{"sparkline", "dependency-graph", "swimlane-timeline", "device-mockup", "heatmap-table", "small-multiples"}
	for _, id := range want {
		if _, ok := VizPatternByID(id); !ok {
			t.Errorf("catalog missing core pattern %q", id)
		}
	}
}

// TestVizPatternByID_CaseInsensitiveAndMiss verifies lookup is case-insensitive
// and a miss is handled (not a panic / false hit).
func TestVizPatternByID_CaseInsensitiveAndMiss(t *testing.T) {
	if _, ok := VizPatternByID("SparkLine"); !ok {
		t.Error("lookup should be case-insensitive")
	}
	if _, ok := VizPatternByID("no-such-pattern"); ok {
		t.Error("unknown id should miss")
	}
}
