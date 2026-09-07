//go:build !short

package main

import (
	"testing"

	"github.com/sageox/ox/internal/manifest"
	"github.com/stretchr/testify/assert"
)

// The sparse-checkout helpers decide whether a team context is missing content
// its own manifest promised. Their happy paths are covered by the #862
// regression tests; these pin the edge cases those tests never reach — the
// git-failure paths that must degrade to "nothing to report" rather than to a
// false alarm, and the manifest parsing that decides what "expected" means.

// TestMissingSparseTopLevelDirs_NonRepoReportsNothing pins the degrade path.
// Failure prevented: doctor reporting a team context as missing directories
// when it simply could not read HEAD (unborn branch, or not a repo at all).
// Reporting "content is missing" there would send someone chasing a
// sparse-checkout bug that does not exist.
func TestMissingSparseTopLevelDirs_NonRepoReportsNothing(t *testing.T) {
	notARepo := t.TempDir()
	cfg := manifest.FallbackConfigFor(manifest.RepoKindTeamContext)
	assert.Nil(t, missingSparseTopLevelDirs(notARepo, cfg),
		"a directory that is not a git repo has no HEAD to compare against")
}

// TestTrackedChildren_NonRepoReturnsNil is the same contract one level down.
func TestTrackedChildren_NonRepoReturnsNil(t *testing.T) {
	assert.Nil(t, trackedChildren(t.TempDir(), "agents"),
		"listing tracked children of a non-repo must degrade, not fabricate")
}

// TestExpectedTeamTopLevelDirs_IgnoresEmptyIncludeEntries.
// Failure prevented: a manifest with a stray blank or bare "/" include line
// registering "" as an expected top-level directory, which would then never
// match anything on disk and report the team context permanently broken.
func TestExpectedTeamTopLevelDirs_IgnoresEmptyIncludeEntries(t *testing.T) {
	expected := expectedTeamTopLevelDirs(&manifest.ManifestConfig{
		Includes: []string{"agents/", "", "   ", "/", "memory/rollups"},
	})
	assert.True(t, expected["agents"])
	assert.True(t, expected["memory"], "a nested include registers its top-level directory")
	assert.False(t, expected[""], "blank and bare-slash entries must not register an empty directory")
}

// TestManifestPathDenied covers the deny matching that decides whether a
// tracked child counts as content the manifest actually wanted.
// Failure prevented: treating an explicitly denied path as evidence that a
// directory materialized, which would mask exactly the #862 omission the
// surrounding check exists to catch.
func TestManifestPathDenied(t *testing.T) {
	cfg := &manifest.ManifestConfig{Denies: []string{"data/murmurs", "/secrets/"}}

	assert.True(t, manifestPathDenied("data/murmurs", cfg), "exact deny must match")
	assert.True(t, manifestPathDenied("data/murmurs/today.jsonl", cfg), "a child of a deny is denied")
	assert.True(t, manifestPathDenied("secrets/key", cfg), "denies are compared slash-trimmed")
	assert.False(t, manifestPathDenied("data/murmursible", cfg),
		"prefix matching must respect the path boundary, not raw string prefix")
	assert.False(t, manifestPathDenied("agents/rules/team.md", cfg))
	assert.False(t, manifestPathDenied("anything", nil), "no manifest denies nothing")
}

// TestMissingSparseTopLevelDirs_AllChildrenDeniedIsNotMissing pins a
// deliberate asymmetry that is easy to "fix" into a false alarm.
//
// A directory whose only tracked children are manifest-denied is NOT reported
// missing. Its absence is correct: the manifest explicitly asked for that
// content to stay out, so sparse checkout omitting it is the system working.
// Only a directory with at least one NON-denied tracked child that failed to
// materialize is the #862 shape.
//
// Failure prevented: reporting every deny-listed directory as an unmaterialized
// omission on every doctor run — noise that would train people to ignore the
// one check that catches real content loss.
func TestMissingSparseTopLevelDirs_AllChildrenDeniedIsNotMissing(t *testing.T) {
	repo := seedSparseTeamContext(t)

	// agents/ is absent from this fixture's working tree, and its only tracked
	// child is agents/rules/team.md. Denying that child leaves nothing the
	// manifest actually wanted, so absence is not a finding.
	denied := &manifest.ManifestConfig{
		Includes: []string{".sageox/", "agents/", "memory/"},
		Denies:   []string{"agents/rules"},
	}
	assert.NotContains(t, missingSparseTopLevelDirs(repo, denied), "agents/",
		"a directory whose only tracked children are denied is absent on purpose")

	// Same fixture, same absent directory, no deny: now it IS the #862 shape.
	wanted := &manifest.ManifestConfig{
		Includes: []string{".sageox/", "agents/", "memory/"},
	}
	assert.Contains(t, missingSparseTopLevelDirs(repo, wanted), "agents/",
		"without the deny, the same missing directory must be reported")
}
