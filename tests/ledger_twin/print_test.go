//go:build ledger_twin

package ledger_twin

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/sageox/ox/internal/glance"
)

func TestPrintWindow(t *testing.T) {
	windowName := os.Getenv("WINDOW")
	if windowName == "" {
		windowName = "hot_zone"
	}

	w := windowByName(windowName)

	result, err := glance.HarvestSessions(manifest.LedgerPath, w.Since, w.Until, glance.HarvestOptions{})
	if err != nil {
		t.Fatal(err)
	}

	authors := glance.GroupByAuthor(result.Sessions)
	conflicts := glance.DetectConflicts(result.Sessions)

	data := glance.ActivityData{
		Since:     w.Since,
		Until:     w.Until,
		Repo:      "ox",
		Authors:   authors,
		Conflicts: conflicts.Overlaps,
		Overlap:   conflicts.OverlapPairs(),
		Stats: glance.Stats{
			TotalSessions:  len(result.Sessions),
			TotalAuthors:   len(authors),
			TotalConflicts: len(conflicts.Overlaps),
		},
		Patterns: glance.DetectPatterns(result.Sessions),
		Velocity: glance.ConflictVelocity(result.Sessions, w.Since, w.Until, 24*time.Hour, 24*time.Hour),
	}
	data.Enrich()

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(data)
}
