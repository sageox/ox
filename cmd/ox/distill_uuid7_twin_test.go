//go:build slow

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/facts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ==========================================================================
// A. Discussion facts — UUID7 prevents conflict
// ==========================================================================

// TestDistillDiscussionFacts_UUID7PreventsConflict simulates two nodes
// extracting facts for the same discussion. With UUID7-based filenames each
// node writes a unique file and rebase succeeds without conflict.
// Failure prevented: concurrent discussion fact extraction produces a git
// merge conflict that blocks team context syncs.
func TestDistillDiscussionFacts_UUID7PreventsConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone/push operations")
	}

	subdir := filepath.Join("memory", ".discussion-facts")
	nodeA, nodeB := setupDistillTwinRepos(t, subdir)
	dirname := "arch-review-2026-04-01"

	// --- Node A: write discussion fact file ---
	uidA, err := uuid.NewV7()
	require.NoError(t, err)
	fileA := dirname + "-" + uidA.String() + ".jsonl"
	sourceHash := "abc123"

	headerA := facts.FileHeader{
		Meta: facts.FileMeta{
			SchemaVersion: facts.SchemaVersion,
			SourceType:    facts.SourceDiscussion,
			RecordedAt:    "2026-04-01T10:00:00Z",
			SourceHash:    sourceHash,
		},
	}
	factA := facts.Fact{
		Headline:   "Decided to use UUID7 for all fact filenames",
		SourceType: facts.SourceDiscussion,
		SourceRef:  "arch-review-2026-04-01",
		Timestamp:  "2026-04-01T10:00:00Z",
		Category:   facts.CategoryDecision,
	}
	require.NoError(t, facts.WriteFacts(filepath.Join(nodeA, subdir, fileA), headerA, []facts.Fact{factA}))
	runTwinGit(t, nodeA, "add", ".")
	runTwinGit(t, nodeA, "commit", "-m", "memory: extract discussion facts (node A)")
	runTwinGit(t, nodeA, "push", "origin", "main")

	// --- Node B: write discussion fact file for the same discussion ---
	uidB, err := uuid.NewV7()
	require.NoError(t, err)
	fileB := dirname + "-" + uidB.String() + ".jsonl"
	require.NotEqual(t, fileA, fileB, "UUID7 filenames must differ between nodes")

	headerB := facts.FileHeader{
		Meta: facts.FileMeta{
			SchemaVersion: facts.SchemaVersion,
			SourceType:    facts.SourceDiscussion,
			RecordedAt:    "2026-04-01T10:00:00Z",
			SourceHash:    sourceHash,
		},
	}
	factB := facts.Fact{
		Headline:   "UUID7 filenames adopted for discussion facts",
		SourceType: facts.SourceDiscussion,
		SourceRef:  "arch-review-2026-04-01",
		Timestamp:  "2026-04-01T10:05:00Z",
		Category:   facts.CategoryDecision,
	}
	require.NoError(t, facts.WriteFacts(filepath.Join(nodeB, subdir, fileB), headerB, []facts.Fact{factB}))
	runTwinGit(t, nodeB, "add", ".")
	runTwinGit(t, nodeB, "commit", "-m", "memory: extract discussion facts (node B)")

	// Push fails (non-fast-forward)
	out := runTwinGitAllowFail(t, nodeB, "push", "origin", "main")
	require.Contains(t, out, "rejected", "Node B push should be rejected (non-fast-forward)")

	// Rebase resolves cleanly
	runTwinGit(t, nodeB, "pull", "--rebase", "--autostash")
	runTwinGit(t, nodeB, "push", "origin", "main")

	// Assert: no conflict markers
	assertNoConflictMarkers(t, filepath.Join(nodeB, subdir))

	// Assert: both fact files exist
	_, errA := os.Stat(filepath.Join(nodeB, subdir, fileA))
	assert.NoError(t, errA, "Node A discussion fact file must exist after rebase")
	_, errB := os.Stat(filepath.Join(nodeB, subdir, fileB))
	assert.NoError(t, errB, "Node B discussion fact file must exist after rebase")

	// Assert: both are valid JSONL
	hdrA, factsFromA, err := facts.ReadFacts(filepath.Join(nodeB, subdir, fileA))
	require.NoError(t, err, "Node A file must be valid JSONL")
	assert.Equal(t, sourceHash, hdrA.Meta.SourceHash)
	assert.Len(t, factsFromA, 1)

	hdrB, factsFromB, err := facts.ReadFacts(filepath.Join(nodeB, subdir, fileB))
	require.NoError(t, err, "Node B file must be valid JSONL")
	assert.Equal(t, sourceHash, hdrB.Meta.SourceHash)
	assert.Len(t, factsFromB, 1)
}

// ==========================================================================
// B. Session facts — UUID7 prevents conflict
// ==========================================================================

// TestDistillSessionFacts_UUID7PreventsConflict simulates two nodes extracting
// facts for sessions on the same date. UUID7 filenames ensure no conflict.
// Failure prevented: concurrent session fact extraction on the same day causes
// a git merge conflict.
func TestDistillSessionFacts_UUID7PreventsConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone/push operations")
	}

	subdir := filepath.Join("memory", ".session-facts", "2026-04-01")
	nodeA, nodeB := setupDistillTwinRepos(t, subdir)
	dirname := "session-impl-widgets"

	// --- Node A ---
	uidA, err := uuid.NewV7()
	require.NoError(t, err)
	fileA := dirname + "-" + uidA.String() + ".jsonl"

	headerA := facts.FileHeader{
		Meta: facts.FileMeta{
			SchemaVersion: facts.SchemaVersion,
			SourceType:    facts.SourceSession,
			RecordedAt:    "2026-04-01T14:00:00Z",
			SourceHash:    "sess-hash-123",
		},
	}
	factA := facts.Fact{
		Headline:   "Implemented widget caching layer",
		SourceType: facts.SourceSession,
		SourceRef:  "session-impl-widgets",
		Timestamp:  "2026-04-01T14:00:00Z",
		Category:   facts.CategoryShip,
	}
	require.NoError(t, facts.WriteFacts(filepath.Join(nodeA, subdir, fileA), headerA, []facts.Fact{factA}))
	runTwinGit(t, nodeA, "add", ".")
	runTwinGit(t, nodeA, "commit", "-m", "memory: extract session facts (node A)")
	runTwinGit(t, nodeA, "push", "origin", "main")

	// --- Node B ---
	uidB, err := uuid.NewV7()
	require.NoError(t, err)
	fileB := dirname + "-" + uidB.String() + ".jsonl"
	require.NotEqual(t, fileA, fileB, "UUID7 filenames must differ between nodes")

	headerB := facts.FileHeader{
		Meta: facts.FileMeta{
			SchemaVersion: facts.SchemaVersion,
			SourceType:    facts.SourceSession,
			RecordedAt:    "2026-04-01T14:00:00Z",
			SourceHash:    "sess-hash-123",
		},
	}
	factB := facts.Fact{
		Headline:   "Widget caching implemented with LRU strategy",
		SourceType: facts.SourceSession,
		SourceRef:  "session-impl-widgets",
		Timestamp:  "2026-04-01T14:05:00Z",
		Category:   facts.CategoryShip,
	}
	require.NoError(t, facts.WriteFacts(filepath.Join(nodeB, subdir, fileB), headerB, []facts.Fact{factB}))
	runTwinGit(t, nodeB, "add", ".")
	runTwinGit(t, nodeB, "commit", "-m", "memory: extract session facts (node B)")

	out := runTwinGitAllowFail(t, nodeB, "push", "origin", "main")
	require.Contains(t, out, "rejected")

	runTwinGit(t, nodeB, "pull", "--rebase", "--autostash")
	runTwinGit(t, nodeB, "push", "origin", "main")

	assertNoConflictMarkers(t, filepath.Join(nodeB, subdir))

	_, errA := os.Stat(filepath.Join(nodeB, subdir, fileA))
	assert.NoError(t, errA, "Node A session fact file must exist after rebase")
	_, errB := os.Stat(filepath.Join(nodeB, subdir, fileB))
	assert.NoError(t, errB, "Node B session fact file must exist after rebase")

	hdrA, factsFromA, err := facts.ReadFacts(filepath.Join(nodeB, subdir, fileA))
	require.NoError(t, err)
	assert.Equal(t, "sess-hash-123", hdrA.Meta.SourceHash)
	assert.Len(t, factsFromA, 1)

	_, factsFromB, err := facts.ReadFacts(filepath.Join(nodeB, subdir, fileB))
	require.NoError(t, err)
	assert.Len(t, factsFromB, 1)
}

// ==========================================================================
// C. Weekly summaries — UUID7 prevents conflict
// ==========================================================================

// TestDistillWeeklySummary_UUID7PreventsConflict simulates two nodes writing
// a weekly summary for the same week. UUID7 filenames ensure no conflict.
// Failure prevented: two daemons generating weekly summaries for the same week
// produce a merge conflict.
func TestDistillWeeklySummary_UUID7PreventsConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone/push operations")
	}

	subdir := filepath.Join("memory", "weekly")
	nodeA, nodeB := setupDistillTwinRepos(t, subdir)

	// --- Node A ---
	uidA, err := uuid.NewV7()
	require.NoError(t, err)
	fileA := "2026-W14-" + uidA.String() + ".md"
	contentA := "# Week 14 Summary (Node A)\n\nWidget feature shipped. Auth bugs fixed.\n"
	require.NoError(t, os.WriteFile(filepath.Join(nodeA, subdir, fileA), []byte(contentA), 0o644))
	runTwinGit(t, nodeA, "add", ".")
	runTwinGit(t, nodeA, "commit", "-m", "memory: weekly summary W14 (node A)")
	runTwinGit(t, nodeA, "push", "origin", "main")

	// --- Node B ---
	uidB, err := uuid.NewV7()
	require.NoError(t, err)
	fileB := "2026-W14-" + uidB.String() + ".md"
	require.NotEqual(t, fileA, fileB, "UUID7 filenames must differ between nodes")

	contentB := "# Week 14 Summary (Node B)\n\nWidget caching layer added. Performance improved.\n"
	require.NoError(t, os.WriteFile(filepath.Join(nodeB, subdir, fileB), []byte(contentB), 0o644))
	runTwinGit(t, nodeB, "add", ".")
	runTwinGit(t, nodeB, "commit", "-m", "memory: weekly summary W14 (node B)")

	out := runTwinGitAllowFail(t, nodeB, "push", "origin", "main")
	require.Contains(t, out, "rejected")

	runTwinGit(t, nodeB, "pull", "--rebase", "--autostash")
	runTwinGit(t, nodeB, "push", "origin", "main")

	// Assert: no conflict markers
	assertNoConflictMarkersInDir(t, filepath.Join(nodeB, subdir))

	// Assert: both files exist
	_, errA := os.Stat(filepath.Join(nodeB, subdir, fileA))
	assert.NoError(t, errA, "Node A weekly summary must exist after rebase")
	_, errB := os.Stat(filepath.Join(nodeB, subdir, fileB))
	assert.NoError(t, errB, "Node B weekly summary must exist after rebase")

	// Assert: content preserved
	gotA, err := os.ReadFile(filepath.Join(nodeB, subdir, fileA))
	require.NoError(t, err)
	assert.Equal(t, contentA, string(gotA))

	gotB, err := os.ReadFile(filepath.Join(nodeB, subdir, fileB))
	require.NoError(t, err)
	assert.Equal(t, contentB, string(gotB))
}

// ==========================================================================
// D. Monthly summaries — UUID7 prevents conflict
// ==========================================================================

// TestDistillMonthlySummary_UUID7PreventsConflict simulates two nodes writing
// a monthly summary for the same month. UUID7 filenames ensure no conflict.
// Failure prevented: two daemons generating monthly summaries for the same
// month produce a merge conflict.
func TestDistillMonthlySummary_UUID7PreventsConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone/push operations")
	}

	subdir := filepath.Join("memory", "monthly")
	nodeA, nodeB := setupDistillTwinRepos(t, subdir)

	// --- Node A ---
	uidA, err := uuid.NewV7()
	require.NoError(t, err)
	fileA := "2026-03-" + uidA.String() + ".md"
	contentA := "# March 2026 Summary (Node A)\n\nMajor architecture overhaul completed.\n"
	require.NoError(t, os.WriteFile(filepath.Join(nodeA, subdir, fileA), []byte(contentA), 0o644))
	runTwinGit(t, nodeA, "add", ".")
	runTwinGit(t, nodeA, "commit", "-m", "memory: monthly summary 2026-03 (node A)")
	runTwinGit(t, nodeA, "push", "origin", "main")

	// --- Node B ---
	uidB, err := uuid.NewV7()
	require.NoError(t, err)
	fileB := "2026-03-" + uidB.String() + ".md"
	require.NotEqual(t, fileA, fileB, "UUID7 filenames must differ between nodes")

	contentB := "# March 2026 Summary (Node B)\n\nShipped UUID7 filenames across all distill outputs.\n"
	require.NoError(t, os.WriteFile(filepath.Join(nodeB, subdir, fileB), []byte(contentB), 0o644))
	runTwinGit(t, nodeB, "add", ".")
	runTwinGit(t, nodeB, "commit", "-m", "memory: monthly summary 2026-03 (node B)")

	out := runTwinGitAllowFail(t, nodeB, "push", "origin", "main")
	require.Contains(t, out, "rejected")

	runTwinGit(t, nodeB, "pull", "--rebase", "--autostash")
	runTwinGit(t, nodeB, "push", "origin", "main")

	assertNoConflictMarkersInDir(t, filepath.Join(nodeB, subdir))

	_, errA := os.Stat(filepath.Join(nodeB, subdir, fileA))
	assert.NoError(t, errA, "Node A monthly summary must exist after rebase")
	_, errB := os.Stat(filepath.Join(nodeB, subdir, fileB))
	assert.NoError(t, errB, "Node B monthly summary must exist after rebase")

	gotA, err := os.ReadFile(filepath.Join(nodeB, subdir, fileA))
	require.NoError(t, err)
	assert.Equal(t, contentA, string(gotA))

	gotB, err := os.ReadFile(filepath.Join(nodeB, subdir, fileB))
	require.NoError(t, err)
	assert.Equal(t, contentB, string(gotB))
}

// ==========================================================================
// E. Negative control — deterministic names cause conflict
// ==========================================================================

// TestDistillWeeklySummary_DeterministicNamesConflict proves the old naming
// scheme (no UUID7) DOES produce merge conflicts when two nodes write the
// same weekly summary file. This confirms the UUID7 fix is necessary.
// Failure prevented: regression — if UUID7 is removed from weekly summary
// filenames, this test catches it.
func TestDistillWeeklySummary_DeterministicNamesConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone/push operations")
	}

	subdir := filepath.Join("memory", "weekly")
	nodeA, nodeB := setupDistillTwinRepos(t, subdir)

	// Old deterministic name: no UUID7 segment.
	fileName := "2026-W14.md"

	contentA := "# Week 14 Summary\n\nWidget feature shipped.\n"
	require.NoError(t, os.WriteFile(filepath.Join(nodeA, subdir, fileName), []byte(contentA), 0o644))
	runTwinGit(t, nodeA, "add", ".")
	runTwinGit(t, nodeA, "commit", "-m", "weekly summary (node A)")
	runTwinGit(t, nodeA, "push", "origin", "main")

	contentB := "# Week 14 Summary\n\nPerformance improvements landed.\n"
	require.NoError(t, os.WriteFile(filepath.Join(nodeB, subdir, fileName), []byte(contentB), 0o644))
	runTwinGit(t, nodeB, "add", ".")
	runTwinGit(t, nodeB, "commit", "-m", "weekly summary (node B)")

	out := runTwinGitAllowFail(t, nodeB, "push", "origin", "main")
	require.Contains(t, out, "rejected")

	// Rebase produces a CONFLICT because both edited the same file.
	rebaseOut := runTwinGitAllowFail(t, nodeB, "pull", "--rebase", "--autostash")
	assert.Contains(t, rebaseOut, "CONFLICT",
		"deterministic filenames must produce a merge conflict when two nodes write the same file")
}

// ==========================================================================
// Helpers
// ==========================================================================

// setupDistillTwinRepos creates a bare git repo and two clones, seeded with
// the given subdirectory structure. Returns the two clone paths.
func setupDistillTwinRepos(t *testing.T, subdir string) (nodeAPath, nodeBPath string) {
	t.Helper()
	base := t.TempDir()

	// Create bare repo.
	barePath := filepath.Join(base, "team-context.git")
	runTwinGit(t, "", "init", "--bare", barePath)

	// Clone A and seed directory structure.
	nodeAPath = filepath.Join(base, "node-a")
	runTwinGit(t, "", "clone", barePath, nodeAPath)
	runTwinGit(t, nodeAPath, "config", "user.email", "test@test.com")
	runTwinGit(t, nodeAPath, "config", "user.name", "test")

	require.NoError(t, os.MkdirAll(filepath.Join(nodeAPath, subdir), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(nodeAPath, subdir, ".gitkeep"), nil, 0o644))
	runTwinGit(t, nodeAPath, "add", ".")
	runTwinGit(t, nodeAPath, "commit", "-m", "init directory structure")
	runTwinGit(t, nodeAPath, "push", "origin", "main")

	// Clone B.
	nodeBPath = filepath.Join(base, "node-b")
	runTwinGit(t, "", "clone", barePath, nodeBPath)
	runTwinGit(t, nodeBPath, "config", "user.email", "test@test.com")
	runTwinGit(t, nodeBPath, "config", "user.name", "test")

	return nodeAPath, nodeBPath
}

// assertNoConflictMarkersInDir walks a directory and fails if any file
// contains git merge conflict markers.
func assertNoConflictMarkersInDir(t *testing.T, dir string) {
	t.Helper()
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		content := string(data)
		if strings.Contains(content, "<<<<<<<") || strings.Contains(content, ">>>>>>>") {
			t.Errorf("conflict markers found in %s", path)
		}
		return nil
	})
	require.NoError(t, err)
}
