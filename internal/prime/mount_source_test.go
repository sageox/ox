package prime

import (
	"strings"
	"testing"
)

func TestTeamMountSourceSummary(t *testing.T) {
	var none *TeamMountSource
	if none.Contributed() || none.Summary() != "" {
		t.Error("a nil mount must report no contribution and no summary")
	}
	if (&TeamMountSource{Path: "/m"}).Summary() != "" {
		t.Error("a drive that added nothing must stay silent")
	}

	got := (&TeamMountSource{Path: "/m", Docs: 2, Rules: 1, Agents: 1, Commands: 3, Memory: 4}).Summary()
	for _, want := range []string{"/m", "2 docs", "1 rules", "1 coworker agents", "3 coworker commands", "4 memory files"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q is missing %q", got, want)
		}
	}
	if strings.Contains((&TeamMountSource{Path: "/m", Rules: 1}).Summary(), "docs") {
		t.Error("zero counts must be left out of the summary")
	}
}
