//go:build ledger_twin

package ledger_twin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/glance"
	"github.com/sageox/ox/internal/ledger"
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
	manifest = BuildManifest()
	manifest.LedgerPath = ledgerPath

	if err := GenerateTwinLedger(ledgerPath, manifest); err != nil {
		fmt.Fprintf(os.Stderr, "failed to generate twin ledger: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "\n╔══════════════════════════════════════════════════════════════╗\n")
	fmt.Fprintf(os.Stderr, "║  LEDGER TWIN: %s\n", ledgerPath)
	fmt.Fprintf(os.Stderr, "║  Sessions:    %d\n", len(manifest.Sessions))
	fmt.Fprintf(os.Stderr, "║  Murmurs:     %d\n", len(manifest.Murmurs))
	fmt.Fprintf(os.Stderr, "║  Windows:     %d\n", len(manifest.Windows))
	fmt.Fprintf(os.Stderr, "╚══════════════════════════════════════════════════════════════╝\n\n")

	code := m.Run()
	os.RemoveAll(ledgerPath)
	os.Exit(code)
}

func harvestMurmurs(t *testing.T, w Window) *glance.HarvestResult {
	t.Helper()
	result, err := glance.HarvestMurmurs(manifest.LedgerPath, w.Since, w.Until)
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
// Pre-crime detection tests (murmur-based)
// ═══════════════════════════════════════════════════════════════

func TestPreCrimePositive_HotZone(t *testing.T) {
	w := windowByName("hot_zone")
	result := harvestMurmurs(t, w)

	authors := glance.GroupByAuthor(result.Murmurs)
	conflicts := glance.DetectConflicts(result.Murmurs)

	assert.GreaterOrEqual(t, len(result.Murmurs), w.MinMurmurs, "murmur count")
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

func TestPreCrimeNegative_ParallelStreams(t *testing.T) {
	w := windowByName("parallel_streams")
	result := harvestMurmurs(t, w)

	authors := glance.GroupByAuthor(result.Murmurs)
	conflicts := glance.DetectConflicts(result.Murmurs)

	assert.GreaterOrEqual(t, len(result.Murmurs), w.MinMurmurs)
	assert.GreaterOrEqual(t, len(authors), w.MinAuthors)
	assert.Equal(t, 0, len(conflicts.Overlaps), "parallel streams should have zero collisions")
}

func TestPreCrime_PairConvergence(t *testing.T) {
	w := windowByName("pair_convergence")
	result := harvestMurmurs(t, w)

	authors := glance.GroupByAuthor(result.Murmurs)
	conflicts := glance.DetectConflicts(result.Murmurs)

	assert.GreaterOrEqual(t, len(result.Murmurs), w.MinMurmurs)
	assert.GreaterOrEqual(t, len(authors), w.MinAuthors)
	assert.Equal(t, 2, len(conflicts.Overlaps), "should detect exactly 2 collisions")

	fm := conflictFileMap(conflicts.Overlaps)
	assert.Equal(t, 2, fm["internal/auth/middleware.go"], "middleware.go: alice+bob")
	assert.Equal(t, 2, fm["cmd/ox/status.go"], "status.go: carol+dave")

	ps := pairSet(conflicts)
	for _, expected := range w.ExpectedPairs {
		assert.True(t, ps[expected], "expected overlap pair %s", expected)
	}
}

func TestPreCrime_SprintEndRush(t *testing.T) {
	w := windowByName("sprint_end_rush")
	result := harvestMurmurs(t, w)

	authors := glance.GroupByAuthor(result.Murmurs)
	conflicts := glance.DetectConflicts(result.Murmurs)

	assert.GreaterOrEqual(t, len(result.Murmurs), w.MinMurmurs)
	assert.GreaterOrEqual(t, len(authors), w.MinAuthors, "all 6 devs should appear")
	assert.GreaterOrEqual(t, len(conflicts.Overlaps), w.MinConflicts)

	fm := conflictFileMap(conflicts.Overlaps)
	assert.GreaterOrEqual(t, fm["docs/getting-started.md"], 2, "getting-started.md: frank+bob")
}

// ═══════════════════════════════════════════════════════════════
// Pattern detection tests (murmur-based)
// ═══════════════════════════════════════════════════════════════

func TestClusterBridge(t *testing.T) {
	w := windowByName("cluster_bridge")
	result := harvestMurmurs(t, w)

	conflicts := glance.DetectConflicts(result.Murmurs)
	assert.GreaterOrEqual(t, len(conflicts.Overlaps), w.MinConflicts)

	// carol bridges auth/ and cli/ clusters
	patterns := glance.DetectPatterns(result.Murmurs)
	var bridges []glance.Pattern
	for _, p := range patterns {
		if p.Type == "cluster_bridge" && p.Authors[0] == "carol" {
			bridges = append(bridges, p)
		}
	}
	assert.NotEmpty(t, bridges, "carol should be detected as a cluster bridge")
}

func TestHotFileDetection(t *testing.T) {
	w := windowByName("hot_zone")
	result := harvestMurmurs(t, w)

	patterns := glance.DetectPatterns(result.Murmurs)

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
	w := windowByName("parallel_streams")
	result := harvestMurmurs(t, w)

	patterns := glance.DetectPatterns(result.Murmurs)

	var silos []glance.Pattern
	for _, p := range patterns {
		if p.Type == "solo_silo" {
			silos = append(silos, p)
		}
	}
	assert.Len(t, silos, 3, "all 3 devs should be in solo silos")
}

// ═══════════════════════════════════════════════════════════════
// Escalation / velocity tests (murmur-based)
// ═══════════════════════════════════════════════════════════════

func TestPreCrimeEscalation(t *testing.T) {
	day1 := windowByName("escalation_day1")
	day2 := windowByName("escalation_day2")
	day3 := windowByName("escalation_day3")

	r1 := harvestMurmurs(t, day1)
	r2 := harvestMurmurs(t, day2)
	r3 := harvestMurmurs(t, day3)

	c1 := glance.DetectConflicts(r1.Murmurs)
	c2 := glance.DetectConflicts(r2.Murmurs)
	c3 := glance.DetectConflicts(r3.Murmurs)

	assert.GreaterOrEqual(t, len(c1.Overlaps), day1.MinConflicts, "day 1 conflicts")
	assert.GreaterOrEqual(t, len(c2.Overlaps), day2.MinConflicts, "day 2 conflicts")
	assert.GreaterOrEqual(t, len(c3.Overlaps), day3.MinConflicts, "day 3 conflicts")

	// Verify escalation via ConflictVelocity
	allMurmurs := append(append(r1.Murmurs, r2.Murmurs...), r3.Murmurs...)
	day := 24 * time.Hour
	points := glance.ConflictVelocity(allMurmurs, ts(10, 0, 0), ts(13, 0, 0), day, day)

	assert.Len(t, points, 3, "should have 3 daily velocity points")
	assert.True(t, glance.IsEscalating(points, 3),
		"collisions should be escalating: day1=%d day2=%d day3=%d",
		points[0].Conflicts, points[1].Conflicts, points[2].Conflicts)
}

// ═══════════════════════════════════════════════════════════════
// Cross-cutting tests
// ═══════════════════════════════════════════════════════════════

func TestFullEnrich(t *testing.T) {
	allSince := ts(10, 0, 0)
	allUntil := ts(25, 0, 0)
	result, err := glance.HarvestMurmurs(manifest.LedgerPath, allSince, allUntil)
	require.NoError(t, err)

	authors := glance.GroupByAuthor(result.Murmurs)
	conflicts := glance.DetectConflicts(result.Murmurs)

	var wipCount, fcCount int
	for _, m := range result.Murmurs {
		switch m.Topic {
		case "wip":
			wipCount++
		case "file-changes":
			fcCount++
		}
	}

	data := glance.ActivityData{
		Since:     allSince,
		Until:     allUntil,
		Repo:      "test-project",
		Authors:   authors,
		Conflicts: conflicts.Overlaps,
		Overlap:   conflicts.OverlapPairs(),
		Stats: glance.Stats{
			TotalMurmurs:    len(result.Murmurs),
			TotalAuthors:    len(authors),
			TotalConflicts:  len(conflicts.Overlaps),
			WIPCount:        wipCount,
			FileChangeCount: fcCount,
		},
	}
	data.Enrich()

	assert.NotEmpty(t, data.Headline)
	assert.NotEmpty(t, data.Guidance)
	assert.Contains(t, data.Headline, "signals from")
	assert.Contains(t, data.Headline, "collision")
	assert.Greater(t, wipCount, 0, "should have WIP murmurs")
	assert.Greater(t, fcCount, 0, "should have file-change murmurs")
	assert.Equal(t, 6, data.Stats.TotalAuthors, "all 6 devs across all windows")
}

func TestSameAuthorNoConflict(t *testing.T) {
	w := windowByName("sprint_end_rush")
	result := harvestMurmurs(t, w)

	conflicts := glance.DetectConflicts(result.Murmurs)

	for _, overlap := range conflicts.Overlaps {
		assert.GreaterOrEqual(t, len(overlap.Authors), 2,
			"conflict on %s should have 2+ distinct authors", overlap.FilePath)
	}
}

func TestWindowIsolation(t *testing.T) {
	w := windowByName("parallel_streams")
	result := harvestMurmurs(t, w)

	for _, m := range result.Murmurs {
		assert.False(t, m.Time.Before(w.Since),
			"murmur %s should not be before window since", m.ID)
		if !w.Until.IsZero() {
			assert.False(t, m.Time.After(w.Until),
				"murmur %s should not be after window until", m.ID)
		}
	}
}

// ═══════════════════════════════════════════════════════════════
// Low-level murmur file tests
// ═══════════════════════════════════════════════════════════════

// readMurmursInRange reads murmur files between since and until.
// Cannot use ledger.ReadMurmursInWindow because it uses time.Now().
// Surfaces I/O and unmarshal errors so broken fixtures fail loudly.
func readMurmursInRange(baseDir string, since, until time.Time) ([]ledger.MurmurFile, error) {
	var murmurs []ledger.MurmurFile
	current := since.Truncate(time.Hour)
	end := until
	if end.IsZero() {
		end = time.Now().UTC()
	}
	for !current.After(end) {
		dir := filepath.Join(baseDir, ledger.MurmurDateHourDir(current))
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				current = current.Add(time.Hour)
				continue
			}
			return nil, fmt.Errorf("read murmur dir %s: %w", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
				continue
			}
			path := filepath.Join(dir, e.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("read murmur file %s: %w", path, err)
			}
			var m ledger.MurmurFile
			if err := json.Unmarshal(data, &m); err != nil {
				return nil, fmt.Errorf("unmarshal murmur %s: %w", path, err)
			}
			if m.Timestamp.Before(since) {
				continue
			}
			if !until.IsZero() && m.Timestamp.After(until) {
				continue
			}
			murmurs = append(murmurs, m)
		}
		current = current.Add(time.Hour)
	}
	return murmurs, nil
}

func TestMurmurGeneration(t *testing.T) {
	for _, w := range manifest.Windows {
		if w.MinMurmurs == 0 && len(w.ExpectedTopics) == 0 {
			continue
		}
		t.Run(w.Name, func(t *testing.T) {
			murmurs, err := readMurmursInRange(manifest.LedgerPath, w.Since, w.Until)
			require.NoError(t, err)

			assert.GreaterOrEqual(t, len(murmurs), w.MinMurmurs, "murmur count")

			topicSet := make(map[string]bool)
			for _, m := range murmurs {
				topicSet[m.Topic] = true
			}
			for _, topic := range w.ExpectedTopics {
				assert.True(t, topicSet[topic], "expected topic %q", topic)
			}

			if w.ExpectedMurmurImportance != "" {
				found := false
				for _, m := range murmurs {
					if m.Importance == w.ExpectedMurmurImportance {
						found = true
						break
					}
				}
				assert.True(t, found, "expected at least one %s murmur", w.ExpectedMurmurImportance)
			}
		})
	}
}

func TestMurmurEscalationDensity(t *testing.T) {
	d1, err := readMurmursInRange(manifest.LedgerPath, ts(10, 0, 0), ts(10, 23, 59))
	require.NoError(t, err)
	d2, err := readMurmursInRange(manifest.LedgerPath, ts(11, 0, 0), ts(11, 23, 59))
	require.NoError(t, err)
	d3, err := readMurmursInRange(manifest.LedgerPath, ts(12, 0, 0), ts(12, 23, 59))
	require.NoError(t, err)

	// Raw count escalation
	assert.Less(t, len(d1), len(d2), "day2 murmurs (%d) > day1 (%d)", len(d2), len(d1))
	assert.Less(t, len(d2), len(d3), "day3 murmurs (%d) > day2 (%d)", len(d3), len(d2))

	// Conflict density escalation via glance pipeline
	toRecords := func(murmurs []ledger.MurmurFile) []glance.MurmurRecord {
		var records []glance.MurmurRecord
		for _, m := range murmurs {
			records = append(records, glance.MurmurRecord{
				ID:    m.ID,
				User:  m.PrincipalID,
				Topic: m.Topic,
				Files: extractFiles(m.Metadata),
			})
		}
		return records
	}

	c1 := glance.DetectConflicts(toRecords(d1))
	c2 := glance.DetectConflicts(toRecords(d2))
	c3 := glance.DetectConflicts(toRecords(d3))

	assert.Less(t, len(c1.Overlaps), len(c2.Overlaps),
		"day2 conflicts (%d) > day1 (%d)", len(c2.Overlaps), len(c1.Overlaps))
	assert.Less(t, len(c2.Overlaps), len(c3.Overlaps),
		"day3 conflicts (%d) > day2 (%d)", len(c3.Overlaps), len(c2.Overlaps))
}

func TestMurmurAgentIDs(t *testing.T) {
	allMurmurs, err := readMurmursInRange(manifest.LedgerPath, ts(10, 0, 0), ts(25, 0, 0))
	require.NoError(t, err)
	require.NotEmpty(t, allMurmurs)

	expectedByID := make(map[string]MurmurSpec, len(manifest.Murmurs))
	for _, spec := range manifest.Murmurs {
		expectedByID[spec.ID] = spec
	}
	for _, m := range allMurmurs {
		spec, ok := expectedByID[m.ID]
		require.True(t, ok, "unexpected murmur %s", m.ID)
		assert.Equal(t, spec.Dev.AgentID, m.AgentID, "murmur %s agent ID", m.ID)
		assert.Equal(t, spec.Dev.Username, m.PrincipalID, "murmur %s principal ID", m.ID)
		assert.Equal(t, "human", m.PrincipalType, "murmur %s principal type", m.ID)
		assert.Empty(t, m.AgentType, "agent_type should be omitted (matches production)")
		assert.Equal(t, "1", m.SchemaVersion)
	}
}

// extractFiles parses Metadata["files"] into a string slice (mirrors glance.extractFilesFromMetadata).
func extractFiles(metadata map[string]string) []string {
	raw := metadata["files"]
	if raw == "" {
		return nil
	}
	var files []string
	for _, f := range strings.Split(raw, ",") {
		f = strings.TrimSpace(f)
		if f != "" {
			files = append(files, f)
		}
	}
	return files
}

// ═══════════════════════════════════════════════════════════════
// Session harvesting tests (glance reads both murmurs AND sessions)
// ═══════════════════════════════════════════════════════════════

func harvestSessions(t *testing.T, w Window) *glance.HarvestSessionResult {
	t.Helper()
	// Widen the window by 24h in each direction to absorb UTC/local timezone
	// mismatch: session folder timestamps are parsed as UTC in ListSessionsSince
	// but window boundaries use time.Local.
	since := w.Since.Add(-24 * time.Hour)
	until := w.Until.Add(24 * time.Hour)
	result, err := glance.HarvestSessions(manifest.LedgerPath, since, until)
	require.NoError(t, err)
	return result
}

func TestSessionHarvest_HotZone(t *testing.T) {
	w := windowByName("hot_zone")
	result := harvestSessions(t, w)

	assert.GreaterOrEqual(t, len(result.Sessions), 3,
		"hot zone should have at least 3 sessions (alice, bob, dave)")

	authorSet := make(map[string]bool)
	for _, s := range result.Sessions {
		authorSet[s.User] = true
	}
	assert.True(t, authorSet["alice"], "alice should have sessions")
	assert.True(t, authorSet["bob"], "bob should have sessions")
	assert.True(t, authorSet["dave"], "dave should have sessions")
}

func TestSessionHarvest_FilesExtracted(t *testing.T) {
	w := windowByName("hot_zone")
	result := harvestSessions(t, w)

	var totalFiles int
	for _, s := range result.Sessions {
		totalFiles += len(s.Files)
	}
	assert.Greater(t, totalFiles, 0, "sessions should have extracted file paths from raw.jsonl")
}

func TestSessionConflicts_MergedWithMurmurs(t *testing.T) {
	// Use hot_zone which has sessions touching overlapping files
	w := windowByName("hot_zone")
	murmurResult := harvestMurmurs(t, w)
	sessionResult := harvestSessions(t, w)

	// Conflicts from murmurs only
	murmurOnlyConflicts := glance.DetectConflicts(murmurResult.Murmurs)

	// Conflicts from murmurs + sessions
	sessionRecords := glance.SessionFilesToMurmurRecords(sessionResult.Sessions)
	allRecords := append(murmurResult.Murmurs, sessionRecords...)
	mergedConflicts := glance.DetectConflicts(allRecords)

	// Merged should find at least as many conflicts as murmurs alone
	assert.GreaterOrEqual(t, len(mergedConflicts.Overlaps), len(murmurOnlyConflicts.Overlaps),
		"session data should add or maintain conflicts")

	// Sessions touch root.go, output.go, handler.go — verify these appear in merged conflicts
	fm := conflictFileMap(mergedConflicts.Overlaps)
	assert.GreaterOrEqual(t, fm["cmd/ox/root.go"], 2,
		"root.go should have conflicts from sessions+murmurs")
}

func TestGroupByAuthor_MergesSessionsAndMurmurs(t *testing.T) {
	w := windowByName("hot_zone")
	murmurResult := harvestMurmurs(t, w)
	sessionResult := harvestSessions(t, w)

	// Group with both sources
	authors := glance.GroupByAuthor(murmurResult.Murmurs, sessionResult.Sessions)

	var aliceSummary *glance.AuthorSummary
	for i := range authors {
		if authors[i].Name == "alice" {
			aliceSummary = &authors[i]
			break
		}
	}
	require.NotNil(t, aliceSummary, "alice should appear in authors")
	assert.Greater(t, aliceSummary.MurmurCount, 0, "alice should have murmurs")
	assert.Greater(t, aliceSummary.SessionCount, 0, "alice should have sessions")
	assert.Greater(t, aliceSummary.FilesTouched, 0, "alice should have files from both sources")
}

func TestSessionHarvest_ParallelStreams(t *testing.T) {
	w := windowByName("parallel_streams")
	result := harvestSessions(t, w)

	assert.GreaterOrEqual(t, len(result.Sessions), 3,
		"parallel streams should have at least 3 sessions")

	// Filter to only sessions whose names match the parallel_streams window IDs (Ox2xxx)
	var windowSessions []glance.SessionRecord
	for _, s := range result.Sessions {
		if strings.Contains(s.Name, "Ox200") {
			windowSessions = append(windowSessions, s)
		}
	}
	// These specific sessions touch isolated areas — no conflicts
	sessionRecords := glance.SessionFilesToMurmurRecords(windowSessions)
	conflicts := glance.DetectConflicts(sessionRecords)
	assert.Equal(t, 0, len(conflicts.Overlaps),
		"parallel streams sessions (Ox2xxx) should have zero file conflicts")
}

func TestSessionHarvest_PairConvergence(t *testing.T) {
	w := windowByName("pair_convergence")
	result := harvestSessions(t, w)

	sessionRecords := glance.SessionFilesToMurmurRecords(result.Sessions)
	conflicts := glance.DetectConflicts(sessionRecords)

	fm := conflictFileMap(conflicts.Overlaps)
	assert.GreaterOrEqual(t, fm["internal/auth/middleware.go"], 2,
		"middleware.go should have alice+bob conflict from sessions")
	assert.GreaterOrEqual(t, fm["cmd/ox/status.go"], 2,
		"status.go should have carol+dave conflict from sessions")
}

func TestHotZoneMurmurs(t *testing.T) {
	murmurs, err := readMurmursInRange(manifest.LedgerPath, ts(20, 10, 0), ts(20, 18, 0))
	require.NoError(t, err)

	assert.GreaterOrEqual(t, len(murmurs), 5)

	authorSet := make(map[string]bool)
	for _, m := range murmurs {
		authorSet[m.PrincipalID] = true
	}
	assert.True(t, authorSet["alice"], "alice should have murmurs in hot zone")
	assert.True(t, authorSet["bob"], "bob should have murmurs in hot zone")
	assert.True(t, authorSet["dave"], "dave should have murmurs in hot zone")
}
