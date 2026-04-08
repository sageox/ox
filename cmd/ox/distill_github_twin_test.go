//go:build slow

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/facts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. UUID7 filename conflict avoidance ---

// TestDistillGitHubFacts_UUID7PreventsConflict simulates two nodes extracting
// facts for the same PR and writing to the same team context repo. With
// deterministic filenames (old scheme) this causes a merge conflict; with
// UUID7-based filenames each node writes a unique file and rebase succeeds.
// Failure prevented: two daemons extracting facts concurrently produce a git
// merge conflict that blocks all subsequent team context syncs.
func TestDistillGitHubFacts_UUID7PreventsConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone/push operations")
	}

	barePath, nodeA, nodeB := setupFactsTwinRepos(t)
	_ = barePath

	factsSubdir := filepath.Join("memory", ".github-facts")
	day := "2026-04-01"

	// --- Node A: write a fact file for PR 409 ---
	uidA, err := uuid.NewV7()
	require.NoError(t, err)
	fileA := day + "-" + uidA.String() + "-pr-409.jsonl"
	sourceData := `{"number":409,"title":"Add widget","body":"Implements widget feature"}`
	sourceHash := contentHash(sourceData)

	headerA := facts.FileHeader{
		Meta: facts.FileMeta{
			SchemaVersion: facts.SchemaVersion,
			SourceType:    facts.SourceGitHub,
			RecordedAt:    day + "T00:00:00Z",
			SourceHash:    sourceHash,
		},
	}
	factA := facts.Fact{
		Headline:   "Widget feature added via PR #409",
		SourceType: facts.SourceGitHub,
		SourceRef:  "PR #409",
		Timestamp:  day + "T12:00:00Z",
		Category:   facts.CategoryShip,
	}
	require.NoError(t, facts.WriteFacts(filepath.Join(nodeA, factsSubdir, fileA), headerA, []facts.Fact{factA}))
	runTwinGit(t, nodeA, "add", ".")
	runTwinGit(t, nodeA, "commit", "-m", "memory: extract facts from pr #409 (node A)")
	runTwinGit(t, nodeA, "push", "origin", "main")

	// --- Node B: write a fact file for the same PR 409 (independent extraction) ---
	uidB, err := uuid.NewV7()
	require.NoError(t, err)
	fileB := day + "-" + uidB.String() + "-pr-409.jsonl"

	// Sanity: filenames must differ (UUID7 uniqueness).
	require.NotEqual(t, fileA, fileB, "UUID7 filenames must differ between nodes")

	headerB := facts.FileHeader{
		Meta: facts.FileMeta{
			SchemaVersion: facts.SchemaVersion,
			SourceType:    facts.SourceGitHub,
			RecordedAt:    day + "T00:00:00Z",
			SourceHash:    sourceHash, // same source data
		},
	}
	factB := facts.Fact{
		Headline:   "PR #409 ships the widget feature",
		SourceType: facts.SourceGitHub,
		SourceRef:  "PR #409",
		Timestamp:  day + "T12:05:00Z",
		Category:   facts.CategoryShip,
	}
	require.NoError(t, facts.WriteFacts(filepath.Join(nodeB, factsSubdir, fileB), headerB, []facts.Fact{factB}))
	runTwinGit(t, nodeB, "add", ".")
	runTwinGit(t, nodeB, "commit", "-m", "memory: extract facts from pr #409 (node B)")

	// --- Node B push should fail (non-fast-forward) ---
	out := runTwinGitAllowFail(t, nodeB, "push", "origin", "main")
	require.Contains(t, out, "rejected", "Node B push should be rejected (non-fast-forward)")

	// --- Rebase resolves cleanly (no conflict because files differ) ---
	runTwinGit(t, nodeB, "pull", "--rebase", "--autostash")
	runTwinGit(t, nodeB, "push", "origin", "main")

	// --- Assert: no merge conflict markers anywhere ---
	assertNoConflictMarkers(t, filepath.Join(nodeB, factsSubdir))

	// --- Assert: both fact files exist in Node B working tree ---
	_, errA := os.Stat(filepath.Join(nodeB, factsSubdir, fileA))
	assert.NoError(t, errA, "Node A fact file must exist in Node B after rebase")
	_, errB := os.Stat(filepath.Join(nodeB, factsSubdir, fileB))
	assert.NoError(t, errB, "Node B fact file must exist in Node B after rebase")

	// --- Assert: both files are valid JSONL ---
	hdrA, factsFromA, err := facts.ReadFacts(filepath.Join(nodeB, factsSubdir, fileA))
	require.NoError(t, err, "Node A fact file must be valid JSONL")
	assert.Equal(t, sourceHash, hdrA.Meta.SourceHash)
	assert.Len(t, factsFromA, 1)

	hdrB, factsFromB, err := facts.ReadFacts(filepath.Join(nodeB, factsSubdir, fileB))
	require.NoError(t, err, "Node B fact file must be valid JSONL")
	assert.Equal(t, sourceHash, hdrB.Meta.SourceHash)
	assert.Len(t, factsFromB, 1)

	// --- Assert: findLatestFactFileSourceHash returns hash from latest file ---
	// UUID7s are chronological so uidB (generated after uidA) sorts later.
	latestHash := findLatestFactFileSourceHash(filepath.Join(nodeB, factsSubdir), day+"-*-pr-409.jsonl")
	assert.Equal(t, sourceHash, latestHash,
		"findLatestFactFileSourceHash must return source_hash from the lexicographically last UUID7 file")

	// --- Assert: freshness dedup would skip re-extraction ---
	// If a third node checks freshness for the same PR data, it should see the
	// existing hash and skip the LLM call.
	assert.Equal(t, sourceHash, contentHash(sourceData),
		"contentHash must be deterministic for the same input")
	assert.Equal(t, latestHash, contentHash(sourceData),
		"freshness check: latest file hash must match current source hash (re-extraction would be skipped)")
}

// --- B. Deterministic filenames cause conflict (negative control) ---

// TestDistillGitHubFacts_DeterministicNamesConflict proves that the old
// naming scheme (no UUID7) DOES produce merge conflicts when two nodes
// write the same file, confirming the UUID7 fix is necessary.
// Failure prevented: regression — if someone removes UUID7 from filenames,
// this test catches it by showing the conflict would return.
func TestDistillGitHubFacts_DeterministicNamesConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone/push operations")
	}

	_, nodeA, nodeB := setupFactsTwinRepos(t)

	factsSubdir := filepath.Join("memory", ".github-facts")
	// Old deterministic name: no UUID7 segment.
	fileName := "2026-04-01-pr-409.jsonl"

	writeSimpleFactFile(t, filepath.Join(nodeA, factsSubdir, fileName), "hash_a",
		"Widget feature added (node A)")
	runTwinGit(t, nodeA, "add", ".")
	runTwinGit(t, nodeA, "commit", "-m", "node A facts")
	runTwinGit(t, nodeA, "push", "origin", "main")

	writeSimpleFactFile(t, filepath.Join(nodeB, factsSubdir, fileName), "hash_b",
		"Widget feature shipped (node B)")
	runTwinGit(t, nodeB, "add", ".")
	runTwinGit(t, nodeB, "commit", "-m", "node B facts")

	// Push fails (expected).
	out := runTwinGitAllowFail(t, nodeB, "push", "origin", "main")
	require.Contains(t, out, "rejected")

	// Rebase will produce a CONFLICT because both edited the same file.
	rebaseOut := runTwinGitAllowFail(t, nodeB, "pull", "--rebase", "--autostash")
	assert.Contains(t, rebaseOut, "CONFLICT",
		"deterministic filenames must produce a merge conflict when two nodes write the same file")
}

// ==========================================================================
// Helpers
// ==========================================================================

// setupFactsTwinRepos creates a bare git repo and two clones, seeded with
// the memory/.github-facts/ directory structure.
func setupFactsTwinRepos(t *testing.T) (bareURL, nodeAPath, nodeBPath string) {
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

	require.NoError(t, os.MkdirAll(filepath.Join(nodeAPath, "memory", ".github-facts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(nodeAPath, "memory", ".github-facts", ".gitkeep"), nil, 0o644))
	runTwinGit(t, nodeAPath, "add", ".")
	runTwinGit(t, nodeAPath, "commit", "-m", "init team context structure")
	runTwinGit(t, nodeAPath, "push", "origin", "main")

	// Clone B.
	nodeBPath = filepath.Join(base, "node-b")
	runTwinGit(t, "", "clone", barePath, nodeBPath)
	runTwinGit(t, nodeBPath, "config", "user.email", "test@test.com")
	runTwinGit(t, nodeBPath, "config", "user.name", "test")

	return barePath, nodeAPath, nodeBPath
}

// runTwinGit runs a git command and fails the test if it errors.
func runTwinGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	var cmdArgs []string
	if dir != "" {
		cmdArgs = append(cmdArgs, "-C", dir)
	}
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.Command("git", cmdArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %s\n%s", args, err, string(out))
	}
	return strings.TrimSpace(string(out))
}

// runTwinGitAllowFail runs a git command and returns output even on failure.
func runTwinGitAllowFail(t *testing.T, dir string, args ...string) string {
	t.Helper()
	var cmdArgs []string
	if dir != "" {
		cmdArgs = append(cmdArgs, "-C", dir)
	}
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.Command("git", cmdArgs...)
	out, _ := cmd.CombinedOutput()
	return string(out)
}

// writeSimpleFactFile writes a minimal JSONL fact file with the given
// source_hash and a single fact headline.
func writeSimpleFactFile(t *testing.T, path, sourceHash, headline string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))

	header := facts.FileHeader{
		Meta: facts.FileMeta{
			SchemaVersion: facts.SchemaVersion,
			SourceType:    facts.SourceGitHub,
			RecordedAt:    "2026-04-01T00:00:00Z",
			SourceHash:    sourceHash,
		},
	}
	fact := facts.Fact{
		Headline:   headline,
		SourceType: facts.SourceGitHub,
		SourceRef:  "PR #409",
		Timestamp:  "2026-04-01T12:00:00Z",
		Category:   facts.CategoryShip,
	}
	require.NoError(t, facts.WriteFacts(path, header, []facts.Fact{fact}))
}

// assertNoConflictMarkers walks a directory and fails if any file contains
// git merge conflict markers.
func assertNoConflictMarkers(t *testing.T, dir string) {
	t.Helper()
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		content := string(data)
		if strings.Contains(content, "<<<<<<<") || strings.Contains(content, ">>>>>>>") {
			t.Errorf("conflict markers found in %s", path)
		}
		// Also verify it's still valid JSONL if it's a .jsonl file.
		if strings.HasSuffix(path, ".jsonl") {
			lines := strings.Split(strings.TrimSpace(content), "\n")
			for i, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				if !json.Valid([]byte(line)) {
					t.Errorf("invalid JSON on line %d of %s: %s", i+1, path, line)
				}
			}
		}
		return nil
	})
	require.NoError(t, err)
}
