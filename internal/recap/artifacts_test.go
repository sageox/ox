package recap

import (
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- resolveArtifacts ---

func TestResolveArtifacts_DocsAndKnownRootMerge(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	f.WriteTeamDoc("principles.md", "# The Constitution\n\nDo the simple thing.\n")
	f.WriteTeamRoot("MEMORY.md", "# Memory\n\nTeam remembers this.\n")
	f.WriteTeamRoot("AGENTS.md", "# Agents\n\nHow agents should behave.\n")

	files := resolveArtifacts(f.Team)
	assert.Contains(t, files, "principles.md")
	assert.Contains(t, files, "memory.md")
	assert.Contains(t, files, "agents.md")
	assert.Equal(t, "The Constitution", files["principles.md"].Title)
}

func TestResolveArtifacts_EmptyTeamPath(t *testing.T) {
	t.Parallel()
	files := resolveArtifacts("")
	assert.Empty(t, files)
}

func TestResolveArtifacts_DocsCatalogTakesPrecedenceOverKnownRoot(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	// If a team ever put a same-named file under docs/, that catalog entry
	// (with its resolved title) should win over the root fallback.
	f.WriteTeamDoc("agents.md", "# Docs Agents\n\nFrom the catalog.\n")
	f.WriteTeamRoot("AGENTS.md", "# Root Agents\n\nFrom the root.\n")

	files := resolveArtifacts(f.Team)
	require.Contains(t, files, "agents.md")
	assert.Contains(t, files["agents.md"].Path, "docs")
}

// --- buildArtifactReaches ---

func TestBuildArtifactReaches_RealTitleAndSnippetFromDisk(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	f.WriteTeamDoc("principles.md", "# The SageOx Constitution\n\nClarity beats cleverness in every review.\n")

	scan := traceScan{docs: map[string]*docReach{}}
	scan.record("principles.md", string(contexttrace.SourceTeamDocs), "My Session", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))

	reaches := buildArtifactReaches(scan, f.Team)
	require.Len(t, reaches, 1)
	assert.Equal(t, "principles.md", reaches[0].Doc)
	assert.Equal(t, "The SageOx Constitution", reaches[0].Title)
	assert.Contains(t, reaches[0].Snippet, "Clarity beats cleverness")
	assert.Equal(t, 1, reaches[0].Sessions)
	assert.Equal(t, []string{"My Session"}, reaches[0].SampleWork)
	assert.NotEmpty(t, reaches[0].Receipt)
}

func TestBuildArtifactReaches_MissingFileStillReportedByName(t *testing.T) {
	t.Parallel()
	f := newFixture(t) // no docs written at all

	scan := traceScan{docs: map[string]*docReach{}}
	scan.record("vanished.md", string(contexttrace.SourceTeamDocs), "A Session", time.Time{})

	reaches := buildArtifactReaches(scan, f.Team)
	require.Len(t, reaches, 1, "a doc that reached a session but no longer resolves on disk must still be reported by name — honest, not hidden")
	assert.Equal(t, "vanished.md", reaches[0].Doc)
	assert.Empty(t, reaches[0].Receipt)
	assert.Empty(t, reaches[0].Title)
}

func TestBuildArtifactReaches_CappedAtMaxArtifacts(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	scan := traceScan{docs: map[string]*docReach{}}
	for i := 0; i < maxArtifacts+5; i++ {
		name := string(rune('a'+i)) + ".md"
		scan.record(name, string(contexttrace.SourceTeamDocs), "s", time.Time{})
	}

	reaches := buildArtifactReaches(scan, f.Team)
	assert.Len(t, reaches, maxArtifacts)
}

// --- readArtifactContent ---

func TestReadArtifactContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		content     string
		wantTitle   string
		wantSnippet string
	}{
		{
			name:        "frontmatter title wins over H1",
			content:     "---\ntitle: From Frontmatter\n---\n# From Heading\n\nBody text here.\n",
			wantTitle:   "From Frontmatter",
			wantSnippet: "Body text here.",
		},
		{
			name:        "falls back to first H1 when no frontmatter",
			content:     "# The Heading\n\nFirst paragraph line.\n",
			wantTitle:   "The Heading",
			wantSnippet: "First paragraph line.",
		},
		{
			name:        "HTML comments skipped as noise",
			content:     "# Title\n\n<!-- pipeline marker -->\nReal content line.\n",
			wantTitle:   "Title",
			wantSnippet: "Real content line.",
		},
		{
			name:        "empty file",
			content:     "",
			wantTitle:   "",
			wantSnippet: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			path := f.WriteTeamDoc("doc.md", tt.content)

			title, snippet := readArtifactContent(path)
			assert.Equal(t, tt.wantTitle, title)
			assert.Equal(t, tt.wantSnippet, snippet)
		})
	}
}

func TestReadArtifactContent_MissingFile(t *testing.T) {
	t.Parallel()
	title, snippet := readArtifactContent("/nonexistent/path/doc.md")
	assert.Empty(t, title)
	assert.Empty(t, snippet)
}

func TestReadArtifactContent_SnippetTruncatedAtMax(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	long := strings.Repeat("word ", snippetMax) // way over snippetMax chars
	path := f.WriteTeamDoc("doc.md", "# Title\n\n"+long+"\n")

	_, snippet := readArtifactContent(path)
	assert.LessOrEqual(t, len([]rune(snippet)), snippetMax)
}

// --- stripFrontmatter ---

func TestStripFrontmatter(t *testing.T) {
	t.Parallel()

	t.Run("well-formed frontmatter is removed and title captured", func(t *testing.T) {
		t.Parallel()
		var title string
		body := stripFrontmatter("---\ntitle: Hello\n---\nBody\n", &title)
		assert.Equal(t, "Hello", title)
		assert.Equal(t, "Body\n", body)
	})

	t.Run("no frontmatter returns content unchanged", func(t *testing.T) {
		t.Parallel()
		var title string
		body := stripFrontmatter("Just body content\n", &title)
		assert.Equal(t, "Just body content\n", body)
		assert.Empty(t, title)
	})

	t.Run("unterminated frontmatter treated as body", func(t *testing.T) {
		t.Parallel()
		var title string
		content := "---\ntitle: Hello\nno closing delimiter\n"
		body := stripFrontmatter(content, &title)
		assert.Equal(t, content, body)
	})

	t.Run("nil titleOut does not panic", func(t *testing.T) {
		t.Parallel()
		assert.NotPanics(t, func() {
			stripFrontmatter("---\ntitle: Hello\n---\nBody\n", nil)
		})
	})
}

// --- frontmatterValue ---

func TestFrontmatterValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		line    string
		key     string
		wantVal string
		wantOK  bool
	}{
		{"simple value", "title: Hello World", "title", "Hello World", true},
		{"quoted value", `title: "Hello World"`, "title", "Hello World", true},
		{"wrong key", "description: x", "title", "", false},
		{"multi-line scalar marker skipped", "title: >-", "title", "", false},
		{"empty value skipped", "title:", "title", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			val, ok := frontmatterValue(tt.line, tt.key)
			assert.Equal(t, tt.wantVal, val)
			assert.Equal(t, tt.wantOK, ok)
		})
	}
}

// --- truncate ---

func TestTruncate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		s    string
		n    int
		want string
	}{
		{"shorter than limit unchanged", "hello", 10, "hello"},
		{"exactly at limit unchanged", "hello", 5, "hello"},
		{"cut and ellipsized", "hello world", 8, "hello w…"},
		{"n of 1 returns single rune, no ellipsis room", "hello", 1, "h"},
		{"multi-byte runes not split", "héllo wörld", 6, "héllo…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := truncate(tt.s, tt.n)
			assert.Equal(t, tt.want, got)
			assert.LessOrEqual(t, len([]rune(got)), tt.n)
		})
	}
}
