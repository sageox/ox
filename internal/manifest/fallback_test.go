package manifest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A knowledge bubble whose manifest doesn't parse must fall back to the kb
// include set, not the team-context one. Regression for ox-182q: bubbles
// inherited the team-context fallback, so knowledge/ was never materialized
// and the bug was invisible except for a slog.Warn.
func TestFallbackConfigFor_KB(t *testing.T) {
	t.Parallel()
	cfg := FallbackConfigFor(RepoKindKB)

	assert.ElementsMatch(t, []string{".sageox/", "AGENTS.md", "knowledge/"}, cfg.Includes,
		"kb fallback must be exactly control-plane + AGENTS.md + knowledge/")

	for _, teamOnly := range []string{"SOUL.md", "TEAM.md", "MEMORY.md", "memory/", "docs/", "agents/", "coworkers/", "discussions/", "agent-context/"} {
		assert.NotContains(t, cfg.Includes, teamOnly,
			"kb fallback must not inherit team-context path %q", teamOnly)
	}

	assert.Equal(t, SupportedVersion, cfg.Version)
	assert.Equal(t, DefaultResolveRules, cfg.ResolveRules)
}

// The team-context fallback must not drift into the kb shape either.
func TestFallbackConfigFor_TeamContext(t *testing.T) {
	t.Parallel()
	cfg := FallbackConfigFor(RepoKindTeamContext)

	assert.Contains(t, cfg.Includes, "SOUL.md")
	assert.Contains(t, cfg.Includes, "discussions/")
	assert.Contains(t, cfg.Includes, "agents/", "team rules must survive manifest fallback")
	assert.NotContains(t, cfg.Includes, "knowledge/",
		"team-context fallback must not include the bubble knowledge/ tree")
}

// A RepoKind with no case in fallbackIncludesFor must panic, not silently
// degrade to some default set. Silent degradation is the exact mechanism of
// the bug this file fixes — the next kind added without a case would inherit
// the wrong tree and, as before, announce it only in a log line.
func TestFallbackConfigFor_UnknownKindPanics(t *testing.T) {
	t.Parallel()
	for _, kind := range []RepoKind{"", "ledger", "TEAM_CONTEXT"} {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			assert.Panics(t, func() { FallbackConfigFor(kind) },
				"unrecognized kind %q must panic rather than fall back to a guess", kind)
		})
	}
}

func TestFallbackConfigFor_ReturnsFreshCopy(t *testing.T) {
	t.Parallel()
	for _, kind := range []RepoKind{RepoKindKB, RepoKindTeamContext} {
		cfg := FallbackConfigFor(kind)
		cfg.Includes[0] = "mutated"
		if FallbackConfigFor(kind).Includes[0] == "mutated" {
			t.Errorf("FallbackConfigFor(%q) leaked the package-level slice", kind)
		}
	}
}

// The end-to-end path that broke in production: a bubble seeded with a
// comment-only sync.manifest (no version, no includes) must still produce a
// sparse set that materializes knowledge/.
func TestParseFile_CommentOnlyManifest_KBStillGetsKnowledge(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sync.manifest")
	// verbatim content the server seeds into new bubbles
	require.NoError(t, os.WriteFile(path, []byte("# managed by SageOx KB watchman\n"), 0o644))

	sparse := ComputeSparseSet(ParseFile(path, RepoKindKB))

	assert.Contains(t, sparse, "/knowledge/")
	assert.Contains(t, sparse, "/.sageox/")
	assert.NotContains(t, sparse, "/discussions/", "must not fall back to the team-context set")
}

// Bare filename patterns match at any depth under gitignore semantics, which
// is what let "AGENTS.md" leak knowledge/agents.md out of an otherwise
// fully-excluded directory (case-insensitively, on macOS).
func TestComputeSparseSet_AnchorsPatternsToRoot(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		include string
		want    string
	}{
		{"bare file", "AGENTS.md", "/AGENTS.md"},
		{"top-level dir", "knowledge/", "/knowledge/"},
		{"dotted dir", ".sageox/", "/.sageox/"},
		{"nested path already root-relative", "memory/daily/", "/memory/daily/"},
		{"already anchored is left alone", "/AGENTS.md", "/AGENTS.md"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := ComputeSparseSet(&ManifestConfig{Includes: []string{tt.include}})
			assert.Contains(t, result, tt.want)
		})
	}
}

func TestEnsureSageoxInclude(t *testing.T) {
	t.Parallel()
	t.Run("appends when absent", func(t *testing.T) {
		t.Parallel()
		got := EnsureSageoxInclude([]string{"/*", "!/*/", "/knowledge/"})
		assert.Equal(t, []string{"/*", "!/*/", "/knowledge/", "/.sageox/"}, got,
			".sageox/ must come after !/*/ so it is re-included, not re-excluded")
	})

	t.Run("no-op when already present in any form", func(t *testing.T) {
		t.Parallel()
		for _, form := range []string{".sageox", ".sageox/", "/.sageox", "/.sageox/"} {
			in := []string{"/*", "!/*/", form}
			assert.Equal(t, in, EnsureSageoxInclude(in), "should not duplicate %q", form)
		}
	})
}
