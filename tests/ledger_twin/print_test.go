//go:build ledger_twin

package ledger_twin

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/sageox/ox/internal/whatsup"
)

func TestPrintWindow(t *testing.T) {
	windowName := os.Getenv("WINDOW")
	if windowName == "" {
		windowName = "hot_zone"
	}

	w := windowByName(windowName)

	result, err := whatsup.HarvestSessions(manifest.LedgerPath, w.Since, w.Until, whatsup.HarvestOptions{})
	if err != nil {
		t.Fatal(err)
	}

	authors := whatsup.GroupByAuthor(result.Sessions)
	conflicts := whatsup.DetectConflicts(result.Sessions)

	data := whatsup.ActivityData{
		Since:     w.Since,
		Until:     w.Until,
		Repo:      "ox",
		Authors:   authors,
		Conflicts: conflicts.Overlaps,
		Overlap:   conflicts.OverlapPairs(),
		Stats: whatsup.Stats{
			TotalSessions:  len(result.Sessions),
			TotalAuthors:   len(authors),
			TotalConflicts: len(conflicts.Overlaps),
		},
		Patterns: whatsup.DetectPatterns(result.Sessions),
		Velocity: whatsup.ConflictVelocity(result.Sessions, w.Since, w.Until, 24*time.Hour, 24*time.Hour),
	}
	data.Enrich()

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(data)
}
