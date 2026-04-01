//go:build ledger_twin

package ledger_twin

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/sageox/ox/internal/glance"
)

// TestCompare_MurmursOnly_vs_MurmursPlusSessions prints a side-by-side
// comparison of the glance pipeline with and without session data.
// Run with: go test -tags ledger_twin -run TestCompare -v ./tests/ledger_twin/
func TestCompare_MurmursOnly_vs_MurmursPlusSessions(t *testing.T) {
	// Use pair_convergence: alice+bob on middleware.go, carol+dave on status.go
	// Sessions have the file touches; murmurs have the WIP announcements.
	w := windowByName("pair_convergence")

	murmurResult, err := glance.HarvestMurmurs(manifest.LedgerPath, w.Since, w.Until)
	if err != nil {
		t.Fatal(err)
	}

	since := w.Since.Add(-24 * time.Hour)
	until := w.Until.Add(24 * time.Hour)
	sessResult, err := glance.HarvestSessions(manifest.LedgerPath, since, until)
	if err != nil {
		t.Fatal(err)
	}

	// ── Pipeline A: murmurs only (old behavior) ──
	authorsA := glance.GroupByAuthor(murmurResult.Murmurs)
	conflictsA := glance.DetectConflicts(murmurResult.Murmurs)
	var wipA, fcA int
	for _, m := range murmurResult.Murmurs {
		switch m.Topic {
		case "wip":
			wipA++
		case "file-changes":
			fcA++
		}
	}
	dataA := glance.ActivityData{
		Since:     w.Since,
		Until:     w.Until,
		Repo:      "ox",
		Authors:   authorsA,
		Conflicts: conflictsA.Overlaps,
		Overlap:   conflictsA.OverlapPairs(),
		Stats: glance.Stats{
			TotalMurmurs:    len(murmurResult.Murmurs),
			TotalAuthors:    len(authorsA),
			TotalConflicts:  len(conflictsA.Overlaps),
			WIPCount:        wipA,
			FileChangeCount: fcA,
		},
	}
	dataA.Enrich()

	// ── Pipeline B: murmurs + sessions (new behavior) ──
	sessionRecords := glance.SessionFilesToMurmurRecords(sessResult.Sessions)
	allRecords := append(murmurResult.Murmurs, sessionRecords...)
	authorsB := glance.GroupByAuthor(murmurResult.Murmurs, sessResult.Sessions)
	conflictsB := glance.DetectConflicts(allRecords)
	dataB := glance.ActivityData{
		Since:     w.Since,
		Until:     w.Until,
		Repo:      "ox",
		Authors:   authorsB,
		Conflicts: conflictsB.Overlaps,
		Overlap:   conflictsB.OverlapPairs(),
		Stats: glance.Stats{
			TotalMurmurs:    len(murmurResult.Murmurs),
			TotalSessions:   len(sessResult.Sessions),
			TotalAuthors:    len(authorsB),
			TotalConflicts:  len(conflictsB.Overlaps),
			WIPCount:        wipA,
			FileChangeCount: fcA,
		},
	}
	dataB.Enrich()

	// ── Print comparison ──
	sep := "════════════════════════════════════════════════════════════"

	fmt.Fprintf(os.Stderr, "\n%s\n", sep)
	fmt.Fprintf(os.Stderr, "  BEFORE (murmurs only)\n")
	fmt.Fprintf(os.Stderr, "%s\n", sep)
	printSummary(t, dataA)

	fmt.Fprintf(os.Stderr, "\n%s\n", sep)
	fmt.Fprintf(os.Stderr, "  AFTER (murmurs + sessions)\n")
	fmt.Fprintf(os.Stderr, "%s\n", sep)
	printSummary(t, dataB)

	fmt.Fprintf(os.Stderr, "\n%s\n", sep)
	fmt.Fprintf(os.Stderr, "  DELTA\n")
	fmt.Fprintf(os.Stderr, "%s\n", sep)
	fmt.Fprintf(os.Stderr, "  Authors:    %d → %d\n", dataA.Stats.TotalAuthors, dataB.Stats.TotalAuthors)
	fmt.Fprintf(os.Stderr, "  Conflicts:  %d → %d\n", dataA.Stats.TotalConflicts, dataB.Stats.TotalConflicts)
	fmt.Fprintf(os.Stderr, "  Sessions:   0 → %d\n", dataB.Stats.TotalSessions)

	// Show new conflicts that sessions surfaced
	beforeFiles := make(map[string]bool)
	for _, c := range dataA.Conflicts {
		beforeFiles[c.FilePath] = true
	}
	fmt.Fprintf(os.Stderr, "\n  New conflicts from sessions:\n")
	newCount := 0
	for _, c := range dataB.Conflicts {
		if !beforeFiles[c.FilePath] {
			authors := make([]string, 0, len(c.Authors))
			for a := range c.Authors {
				authors = append(authors, a)
			}
			fmt.Fprintf(os.Stderr, "    + %-40s (%v)\n", c.FilePath, authors)
			newCount++
		}
	}
	if newCount == 0 {
		fmt.Fprintf(os.Stderr, "    (none — sessions reinforced existing murmur conflicts)\n")
	}

	// Show new authors that sessions surfaced
	beforeAuthors := make(map[string]bool)
	for _, a := range dataA.Authors {
		beforeAuthors[a.Name] = true
	}
	fmt.Fprintf(os.Stderr, "\n  New authors from sessions:\n")
	newAuthors := 0
	for _, a := range dataB.Authors {
		if !beforeAuthors[a.Name] {
			fmt.Fprintf(os.Stderr, "    + %s (%d sessions, %d files)\n", a.Name, a.SessionCount, a.FilesTouched)
			newAuthors++
		}
	}
	if newAuthors == 0 {
		fmt.Fprintf(os.Stderr, "    (none — all session authors already had murmurs)\n")
	}

	// Show session detail per author
	fmt.Fprintf(os.Stderr, "\n  Author detail (sessions added):\n")
	for _, a := range dataB.Authors {
		if a.SessionCount > 0 {
			fmt.Fprintf(os.Stderr, "    %-10s  murmurs=%d  sessions=%d  files=%d\n",
				a.Name, a.MurmurCount, a.SessionCount, a.FilesTouched)
			for _, s := range a.Sessions {
				fmt.Fprintf(os.Stderr, "      📄 %s\n", s.Title)
				if len(s.Files) > 0 {
					fmt.Fprintf(os.Stderr, "         files: %v\n", s.Files)
				}
			}
		}
	}
	fmt.Fprintf(os.Stderr, "\n")
}

func printSummary(t *testing.T, data glance.ActivityData) {
	t.Helper()
	fmt.Fprintf(os.Stderr, "  Headline: %s\n", data.Headline)
	fmt.Fprintf(os.Stderr, "  Stats:    murmurs=%d authors=%d conflicts=%d sessions=%d\n",
		data.Stats.TotalMurmurs, data.Stats.TotalAuthors,
		data.Stats.TotalConflicts, data.Stats.TotalSessions)

	if len(data.Conflicts) > 0 {
		fmt.Fprintf(os.Stderr, "  Conflicts:\n")
		for _, c := range data.Conflicts {
			authors := make([]string, 0, len(c.Authors))
			for a := range c.Authors {
				authors = append(authors, a)
			}
			fmt.Fprintf(os.Stderr, "    %-40s %v\n", c.FilePath, authors)
		}
	}

	if len(data.Overlap) > 0 {
		fmt.Fprintf(os.Stderr, "  Overlap pairs:\n")
		for _, p := range data.Overlap {
			fmt.Fprintf(os.Stderr, "    %s ↔ %s  (%d shared files)\n", p.Pair[0], p.Pair[1], p.SharedFiles)
		}
	}

	if len(data.Actions) > 0 {
		fmt.Fprintf(os.Stderr, "  Actions:\n")
		for _, a := range data.Actions {
			fmt.Fprintf(os.Stderr, "    [%s] %s\n", a.Risk, a.Text)
		}
	}

	if len(data.Authors) > 0 {
		fmt.Fprintf(os.Stderr, "  Authors:\n")
		for _, a := range data.Authors {
			fmt.Fprintf(os.Stderr, "    %-10s  murmurs=%-2d sessions=%-2d files=%-2d wip=%q\n",
				a.Name, a.MurmurCount, a.SessionCount, a.FilesTouched,
				truncate(a.WIPStatus, 60))
		}
	}

	j, _ := json.MarshalIndent(data.Stats, "  ", "  ")
	fmt.Fprintf(os.Stderr, "  Raw stats: %s\n", string(j))
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
