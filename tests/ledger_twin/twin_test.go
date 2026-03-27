//go:build ledger_twin

package ledger_twin

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/sageox/ox/internal/glance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var manifest *TwinManifest

func TestMain(m *testing.M) {
	ledgerPath, err := os.MkdirTemp("", "glance-ledger-twin-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp dir: %v\n", err)
		os.Exit(1)
	}
	// Intentionally NOT cleaning up — leave for human inspection.

	manifest = BuildManifest()
	manifest.LedgerPath = ledgerPath

	if err := GenerateTwinLedger(ledgerPath, manifest); err != nil {
		fmt.Fprintf(os.Stderr, "failed to generate twin ledger: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "\n╔══════════════════════════════════════════════════════════════╗\n")
	fmt.Fprintf(os.Stderr, "║  LEDGER TWIN: %s\n", ledgerPath)
	fmt.Fprintf(os.Stderr, "║  Sessions:    %d\n", len(manifest.Sessions))
	fmt.Fprintf(os.Stderr, "║  Windows:     %d\n", len(manifest.Windows))
	fmt.Fprintf(os.Stderr, "║  Inspect:     ls %s/sessions/\n", ledgerPath)
	fmt.Fprintf(os.Stderr, "╚══════════════════════════════════════════════════════════════╝\n\n")

	os.Exit(m.Run())
}

func harvest(t *testing.T, w Window) *glance.HarvestResult {
	t.Helper()
	result, err := glance.HarvestSessions(manifest.LedgerPath, w.Since, w.Until, glance.HarvestOptions{})
	require.NoError(t, err)
	return result
}

func pairSet(report *glance.ConflictReport) map[string]bool {
	pairs := report.OverlapPairs()
	set := make(map[string]bool, len(pairs))
	for _, p := range pairs {
		set[p.Pair[0]+"|"+p.Pair[1]] = true
	}
	return set
}

func conflictFileMap(overlaps []glance.FileOverlap) map[string]int {
	m := make(map[string]int, len(overlaps))
	for _, o := range overlaps {
		m[o.FilePath] = len(o.Authors)
	}
	return m
}

func windowByName(name string) Window {
	for _, w := range manifest.Windows {
		if w.Name == name {
			return w
		}
	}
	panic("window not found: " + name)
}

// ═══════════════════════════════════════════════════════════════
// Conflict detection tests
// ═══════════════════════════════════════════════════════════════

func TestHotZone(t *testing.T) {
	w := windowByName("hot_zone")
	result := harvest(t, w)

	authors := glance.GroupByAuthor(result.Sessions)
	conflicts := glance.DetectConflicts(result.Sessions)

	assert.GreaterOrEqual(t, len(result.Sessions), w.MinSessions, "session count")
	assert.GreaterOrEqual(t, len(authors), w.MinAuthors, "author count")
	assert.GreaterOrEqual(t, len(conflicts.Overlaps), w.MinConflicts, "conflict count")

	fm := conflictFileMap(conflicts.Overlaps)
	assert.GreaterOrEqual(t, fm["cmd/ox/root.go"], 3, "root.go should have 3+ authors")
	assert.GreaterOrEqual(t, fm["internal/cli/output.go"], 3, "output.go should have 3+ authors")
	assert.GreaterOrEqual(t, fm["internal/auth/handler.go"], 2, "handler.go should have 2+ authors")

	ps := pairSet(conflicts)
	for _, expected := range w.ExpectedPairs {
		assert.True(t, ps[expected], "expected overlap pair %s", expected)
	}
}

func TestParallelStreams(t *testing.T) {
	w := windowByName("parallel_streams")
	result := harvest(t, w)

	authors := glance.GroupByAuthor(result.Sessions)
	conflicts := glance.DetectConflicts(result.Sessions)

	assert.GreaterOrEqual(t, len(result.Sessions), w.MinSessions)
	assert.GreaterOrEqual(t, len(authors), w.MinAuthors)
	assert.Equal(t, 0, len(conflicts.Overlaps), "parallel streams should have zero conflicts")
}

func TestPairConvergence(t *testing.T) {
	w := windowByName("pair_convergence")
	result := harvest(t, w)

	authors := glance.GroupByAuthor(result.Sessions)
	conflicts := glance.DetectConflicts(result.Sessions)

	assert.GreaterOrEqual(t, len(result.Sessions), w.MinSessions)
	assert.GreaterOrEqual(t, len(authors), w.MinAuthors)
	assert.Equal(t, 2, len(conflicts.Overlaps), "should have exactly 2 conflicts")

	fm := conflictFileMap(conflicts.Overlaps)
	assert.Equal(t, 2, fm["internal/auth/middleware.go"], "middleware.go: alice+bob")
	assert.Equal(t, 2, fm["cmd/ox/status.go"], "status.go: carol+dave")

	ps := pairSet(conflicts)
	for _, expected := range w.ExpectedPairs {
		assert.True(t, ps[expected], "expected overlap pair %s", expected)
	}
}

func TestSprintEndRush(t *testing.T) {
	w := windowByName("sprint_end_rush")
	result := harvest(t, w)

	authors := glance.GroupByAuthor(result.Sessions)
	conflicts := glance.DetectConflicts(result.Sessions)

	assert.GreaterOrEqual(t, len(result.Sessions), w.MinSessions)
	assert.GreaterOrEqual(t, len(authors), w.MinAuthors, "all 6 devs should appear")
	assert.GreaterOrEqual(t, len(conflicts.Overlaps), w.MinConflicts)

	fm := conflictFileMap(conflicts.Overlaps)
	assert.GreaterOrEqual(t, fm["docs/getting-started.md"], 2, "getting-started.md: frank+bob")
}

func TestActiveRecording(t *testing.T) {
	w := windowByName("active_recording")
	result := harvest(t, w)

	assert.GreaterOrEqual(t, len(result.Sessions), w.MinSessions)

	recordingCount := 0
	for _, s := range result.Sessions {
		if s.Recording {
			recordingCount++
		}
	}
	assert.Equal(t, w.ExpectedRecording, recordingCount, "recording sessions")
}

// ═══════════════════════════════════════════════════════════════
// Pattern detection tests
// ═══════════════════════════════════════════════════════════════

func TestClusterBridge(t *testing.T) {
	w := windowByName("cluster_bridge")
	result := harvest(t, w)

	conflicts := glance.DetectConflicts(result.Sessions)
	assert.GreaterOrEqual(t, len(conflicts.Overlaps), w.MinConflicts)

	// carol bridges auth/ and cli/ clusters
	patterns := glance.DetectPatterns(result.Sessions)
	var bridges []glance.Pattern
	for _, p := range patterns {
		if p.Type == "cluster_bridge" && p.Authors[0] == "carol" {
			bridges = append(bridges, p)
		}
	}
	assert.NotEmpty(t, bridges, "carol should be detected as a cluster bridge")
}

func TestHotFileDetection(t *testing.T) {
	// In the hot zone, root.go and output.go are touched by 3 authors each
	w := windowByName("hot_zone")
	result := harvest(t, w)

	patterns := glance.DetectPatterns(result.Sessions)

	var hotFiles []glance.Pattern
	for _, p := range patterns {
		if p.Type == "hot_file" {
			hotFiles = append(hotFiles, p)
		}
	}
	assert.NotEmpty(t, hotFiles, "hot zone should have hot files")

	hotFileNames := make(map[string]bool)
	for _, p := range hotFiles {
		hotFileNames[p.Files[0]] = true
	}
	assert.True(t, hotFileNames["cmd/ox/root.go"], "root.go should be a hot file")
	assert.True(t, hotFileNames["internal/cli/output.go"], "output.go should be a hot file")
}

func TestSoloSiloDetection(t *testing.T) {
	// In parallel streams, each dev works in isolation
	w := windowByName("parallel_streams")
	result := harvest(t, w)

	patterns := glance.DetectPatterns(result.Sessions)

	var silos []glance.Pattern
	for _, p := range patterns {
		if p.Type == "solo_silo" {
			silos = append(silos, p)
		}
	}
	assert.Len(t, silos, 3, "all 3 devs should be in solo silos")
}

// ═══════════════════════════════════════════════════════════════
// Escalation / velocity tests
// ═══════════════════════════════════════════════════════════════

func TestEscalation(t *testing.T) {
	day1 := windowByName("escalation_day1")
	day2 := windowByName("escalation_day2")
	day3 := windowByName("escalation_day3")

	r1 := harvest(t, day1)
	r2 := harvest(t, day2)
	r3 := harvest(t, day3)

	c1 := glance.DetectConflicts(r1.Sessions)
	c2 := glance.DetectConflicts(r2.Sessions)
	c3 := glance.DetectConflicts(r3.Sessions)

	assert.GreaterOrEqual(t, len(c1.Overlaps), day1.MinConflicts, "day 1 conflicts")
	assert.GreaterOrEqual(t, len(c2.Overlaps), day2.MinConflicts, "day 2 conflicts")
	assert.GreaterOrEqual(t, len(c3.Overlaps), day3.MinConflicts, "day 3 conflicts")

	// Verify escalation via ConflictVelocity
	allSessions := append(append(r1.Sessions, r2.Sessions...), r3.Sessions...)
	day := 24 * time.Hour
	points := glance.ConflictVelocity(allSessions, ts(10, 0, 0), ts(13, 0, 0), day, day)

	assert.Len(t, points, 3, "should have 3 daily velocity points")
	assert.True(t, glance.IsEscalating(points, 3),
		"conflicts should be escalating: day1=%d day2=%d day3=%d",
		points[0].Conflicts, points[1].Conflicts, points[2].Conflicts)
}

// ═══════════════════════════════════════════════════════════════
// Subagent filtering test
// ═══════════════════════════════════════════════════════════════

func TestSubagentExcluded(t *testing.T) {
	w := windowByName("subagent_excluded")

	result := harvest(t, w)
	assert.Equal(t, w.MinSessions, len(result.Sessions), "should exclude subagent sessions by default")

	resultWithSub, err := glance.HarvestSessions(
		manifest.LedgerPath, w.Since, w.Until,
		glance.HarvestOptions{IncludeSubagents: true},
	)
	require.NoError(t, err)
	assert.Greater(t, len(resultWithSub.Sessions), len(result.Sessions),
		"including subagents should return more sessions")
}

// ═══════════════════════════════════════════════════════════════
// Cross-cutting tests
// ═══════════════════════════════════════════════════════════════

func TestFullEnrich(t *testing.T) {
	allSince := ts(10, 0, 0)
	allUntil := ts(25, 0, 0)
	result, err := glance.HarvestSessions(manifest.LedgerPath, allSince, allUntil, glance.HarvestOptions{})
	require.NoError(t, err)

	authors := glance.GroupByAuthor(result.Sessions)
	conflicts := glance.DetectConflicts(result.Sessions)

	data := glance.ActivityData{
		Since:     allSince,
		Until:     allUntil,
		Repo:      "test-project",
		Authors:   authors,
		Conflicts: conflicts.Overlaps,
		Overlap:   conflicts.OverlapPairs(),
		Stats: glance.Stats{
			TotalSessions:  len(result.Sessions),
			TotalAuthors:   len(authors),
			TotalConflicts: len(conflicts.Overlaps),
		},
	}
	data.Enrich()

	assert.NotEmpty(t, data.Headline)
	assert.NotEmpty(t, data.Guidance)
	assert.Contains(t, data.Headline, "sessions by")
	assert.Contains(t, data.Headline, "conflict")
	assert.Equal(t, 6, data.Stats.TotalAuthors, "all 6 devs across all windows")
}

func TestSameAuthorNoConflict(t *testing.T) {
	w := windowByName("sprint_end_rush")
	result := harvest(t, w)

	conflicts := glance.DetectConflicts(result.Sessions)

	for _, overlap := range conflicts.Overlaps {
		assert.GreaterOrEqual(t, len(overlap.Authors), 2,
			"conflict on %s should have 2+ distinct authors", overlap.FilePath)
	}
}

func TestMailmapResolution(t *testing.T) {
	mailmap := map[string]string{"alice": "Alice Johnson"}

	result, err := glance.HarvestSessions(
		manifest.LedgerPath, ts(20, 10, 0), ts(20, 18, 0),
		glance.HarvestOptions{Mailmap: mailmap},
	)
	require.NoError(t, err)

	foundAlice := false
	for _, s := range result.Sessions {
		if s.User == "Alice Johnson" {
			foundAlice = true
		}
		assert.NotEqual(t, "alice", s.User, "raw 'alice' should be remapped")
	}
	assert.True(t, foundAlice, "should find sessions attributed to 'Alice Johnson'")
}

func TestWindowIsolation(t *testing.T) {
	w2 := windowByName("parallel_streams")
	result := harvest(t, w2)

	for _, s := range result.Sessions {
		assert.False(t, s.Time.Before(w2.Since),
			"session %s should not be before window since", s.Name)
		if !w2.Until.IsZero() {
			assert.False(t, s.Time.After(w2.Until),
				"session %s should not be after window until", s.Name)
		}
	}
}
