package recap

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- gatherTeamContext ---

func TestGatherTeamContext_DocsAndDiscussions(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	f.WriteTeamDoc("principles.md", "# The Constitution\n\nClarity beats cleverness.\n")
	f.WriteTeamDoc("glossary.md", "# Glossary\n\nDomain terms defined here.\n")
	f.WriteDiscussion("2026-06-01-alice", "# Roadmap sync\n\nDecided to ship recap this quarter.\n")

	tb := gatherTeamContext(f.Team)

	require.True(t, tb.populated())
	assert.Equal(t, 2, tb.docCount)
	assert.Equal(t, 1, tb.discCount)

	var sawDoc, sawDiscussion bool
	for _, a := range tb.artifacts {
		if a.Kind == "doc" && a.Doc == "principles.md" {
			sawDoc = true
			assert.Equal(t, "The Constitution", a.Title)
		}
		if a.Kind == "discussion" {
			sawDiscussion = true
			assert.Equal(t, "Roadmap sync", a.Title)
		}
	}
	assert.True(t, sawDoc, "docs/ artifact must appear in the team-built list")
	assert.True(t, sawDiscussion, "a recorded discussion must appear in the team-built list")
}

func TestGatherTeamContext_EmptyTeamPath(t *testing.T) {
	t.Parallel()
	tb := gatherTeamContext("")
	assert.False(t, tb.populated())
	assert.Empty(t, tb.artifacts)
}

func TestGatherTeamContext_MemoryOnlyStillPopulated(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.WriteTeamRoot("MEMORY.md", "# Memory\n\nSubstantive team memory line.\n")

	tb := gatherTeamContext(f.Team)
	assert.True(t, tb.populated(), "MEMORY.md alone (no docs/, no discussions/) is still team-built value")
}

func TestGatherTeamContext_CappedAtMaxTeamArtifacts(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	for i := 0; i < maxTeamArtifacts+5; i++ {
		name := string(rune('a'+i)) + ".md"
		f.WriteTeamDoc(name, "# Doc "+name+"\n\nBody.\n")
	}

	tb := gatherTeamContext(f.Team)
	assert.Len(t, tb.artifacts, maxTeamArtifacts)
}

// --- teamBuilt.populated ---

func TestTeamBuiltPopulated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tb   teamBuilt
		want bool
	}{
		{"nothing at all", teamBuilt{}, false},
		{"docs present", teamBuilt{docCount: 1}, true},
		{"discussions present", teamBuilt{discCount: 1}, true},
		{"memory lines present", teamBuilt{memoryLines: 1}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.tb.populated())
		})
	}
}

// --- countDiscussions ---

func TestCountDiscussions(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	assert.Equal(t, 0, countDiscussions(f.Team), "no discussions/ dir yet")

	f.WriteDiscussion("2026-06-01-alice", "content")
	f.WriteDiscussion("2026-06-02-bob", "content")
	assert.Equal(t, 2, countDiscussions(f.Team))
}

// --- recentDiscussions ---

func TestRecentDiscussions_ReverseChronologicalAndLimited(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	f.WriteDiscussion("2026-06-01-alice", "# Oldest\n\nContent.\n")
	f.WriteDiscussion("2026-06-02-bob", "# Middle\n\nContent.\n")
	f.WriteDiscussion("2026-06-03-carol", "# Newest\n\nContent.\n")

	got := recentDiscussions(f.Team, 2)
	require.Len(t, got, 2, "limit must be respected")
	assert.Equal(t, "Newest", got[0].Title, "date-prefixed dirs sort reverse-lexical == reverse-chronological")
	assert.Equal(t, "Middle", got[1].Title)
}

func TestRecentDiscussions_NoDiscussionsDir(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	got := recentDiscussions(f.Team, 4)
	assert.Empty(t, got)
}

// --- discussionTitle ---

func TestDiscussionTitle(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "From Summary", discussionTitle("2026-06-01-alice", "From Summary"))
	assert.Equal(t, "2026-06-01-alice", discussionTitle("2026-06-01-alice", ""), "falls back to dir name when summary has no title")
}

// --- memorySubstance ---

func TestMemorySubstance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    int
	}{
		{
			name:    "counts only substantive lines",
			content: "# Heading\n\n<!-- comment -->\nReal line one.\nReal line two.\n",
			want:    2,
		},
		{
			name:    "all blank, heading, or comment yields zero",
			content: "# Heading\n\n<!-- comment -->\n\n",
			want:    0,
		},
		{
			name:    "empty file",
			content: "",
			want:    0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			path := f.WriteTeamRoot("MEMORY.md", tt.content)
			assert.Equal(t, tt.want, memorySubstance(path))
		})
	}
}

func TestMemorySubstance_MissingFile(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 0, memorySubstance("/nonexistent/MEMORY.md"))
}
