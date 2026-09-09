package sacred

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// EntityOf is the unit the mass-delete policy counts. Getting it wrong in
// either direction is a data-loss bug: too coarse and a real wipe slips under
// the threshold, too fine and ordinary cleanup is refused.
func TestEntityOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		path string
		want string
	}{
		// A session is a directory of artifacts; every artifact maps to it.
		{"session meta", "sessions/2026-03-03T09-57-ryan-Ox3E53/meta.json", "sessions/2026-03-03T09-57-ryan-Ox3E53"},
		{"session raw", "sessions/2026-03-03T09-57-ryan-Ox3E53/raw.jsonl", "sessions/2026-03-03T09-57-ryan-Ox3E53"},
		{"session nested artifact", "sessions/abc/keyframes/x/y.jpg", "sessions/abc"},
		{"session rej artifact", "sessions/abc/meta.json.rej", "sessions/abc"},
		{"plan file", "data/plans/2026-06-22-viz/plan.md", "data/plans/2026-06-22-viz"},
		{"plan nested", "data/plans/2026-06-22-viz/a/b/c.json", "data/plans/2026-06-22-viz"},

		// The entity directory itself, as `git ls-tree -d` reports it.
		{"session dir itself", "sessions/abc", "sessions/abc"},
		{"plan dir itself", "data/plans/abc", "data/plans/abc"},

		// Not sacred, or not an entity.
		{"empty", "", ""},
		{"outside sacred prefixes", "data/github/issues.json", ""},
		{"prefix with no entity", "sessions/", ""},
		{"plan prefix with no entity", "data/plans/", ""},
		{"similar but different prefix", "sessions-archive/abc/meta.json", ""},
		{"whitespace only", "   ", ""},

		// Raw `git` output lines arrive with surrounding whitespace.
		{"trims surrounding whitespace", "  sessions/abc/meta.json  ", "sessions/abc"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, EntityOf(tc.path))
		})
	}
}

// Entities deduplicates, preserves order, and drops non-sacred noise so
// callers can hand it raw `git log --name-only` output.
func TestEntities(t *testing.T) {
	t.Parallel()

	t.Run("dedupes artifacts of one session to one entity", func(t *testing.T) {
		t.Parallel()
		got := Entities([]string{
			"sessions/abc/meta.json",
			"sessions/abc/raw.jsonl",
			"sessions/abc/summary.md",
		})
		assert.Equal(t, []string{"sessions/abc"}, got,
			"six files from one session must not read as six deletions")
	})

	t.Run("preserves first-seen order across entities", func(t *testing.T) {
		t.Parallel()
		got := Entities([]string{
			"sessions/b/meta.json",
			"data/plans/a/plan.md",
			"sessions/b/raw.jsonl",
			"sessions/c/meta.json",
		})
		assert.Equal(t, []string{"sessions/b", "data/plans/a", "sessions/c"}, got)
	})

	t.Run("drops blanks and non-sacred paths", func(t *testing.T) {
		t.Parallel()
		got := Entities([]string{"", "  ", "data/github/prs.json", "README.md", "sessions/abc/meta.json"})
		assert.Equal(t, []string{"sessions/abc"}, got)
	})

	t.Run("empty input", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, Entities(nil))
	})

	// The threshold is stated in entities, so a single real session — which
	// carries more files than the old file threshold allowed — must sit under
	// it. This is the arithmetic that made ordinary deletes look like wipes.
	t.Run("one real session is under the threshold", func(t *testing.T) {
		t.Parallel()
		files := []string{
			"sessions/abc/meta.json", "sessions/abc/raw.jsonl", "sessions/abc/session.md",
			"sessions/abc/summary.json", "sessions/abc/summary.md", "sessions/abc/context-trace.jsonl",
		}
		assert.Greater(t, len(files), DetectorEntityThreshold, "fixture must exceed the entity threshold in raw file count")
		assert.LessOrEqual(t, len(Entities(files)), DetectorEntityThreshold,
			"but one session is a single entity, which must not trip the detector")
	})
}

func TestHasPrefixAndFilter(t *testing.T) {
	t.Parallel()

	assert.True(t, HasPrefix("sessions/abc/meta.json"))
	assert.True(t, HasPrefix("data/plans/abc/plan.md"))
	assert.False(t, HasPrefix("data/github/issues.json"))
	assert.False(t, HasPrefix(""))

	got := Filter([]string{"sessions/a/x", "", "  ", "data/github/y", "data/plans/b/z"})
	assert.Equal(t, []string{"sessions/a/x", "data/plans/b/z"}, got)
}
