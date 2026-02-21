package teamdocs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverDocs(t *testing.T) {
	tests := []struct {
		name     string
		files    map[string]string // filename -> content
		wantDocs []string          // expected doc names in result
		wantNot  []string          // names that should NOT appear
	}{
		{
			name: "full frontmatter",
			files: map[string]string{
				"principles.md": "---\ntitle: The Constitution\ndescription: Team values\nvisibility: indexed\nwhen: architectural decisions\n---\n# The Constitution\nContent here.",
			},
			wantDocs: []string{"principles.md"},
		},
		{
			name: "no frontmatter falls back to H1 and first paragraph",
			files: map[string]string{
				"guide.md": "# Deployment Guide\n\nThis explains how to deploy.\n",
			},
			wantDocs: []string{"guide.md"},
		},
		{
			name: "hidden doc excluded",
			files: map[string]string{
				"draft.md":   "---\nvisibility: hidden\n---\n# Draft\nWIP.",
				"visible.md": "# Visible\nHello.",
			},
			wantDocs: []string{"visible.md"},
			wantNot:  []string{"draft.md"},
		},
		{
			name: "README.md always excluded",
			files: map[string]string{
				"README.md": "# Team Docs\nInstructions here.",
				"guide.md":  "# Guide\nContent.",
			},
			wantDocs: []string{"guide.md"},
			wantNot:  []string{"README.md"},
		},
		{
			name: "non-md files ignored",
			files: map[string]string{
				"guide.md":   "# Guide\nContent.",
				"image.png":  "binary data",
				"data.json":  `{"key": "value"}`,
				"notes.txt":  "some notes",
			},
			wantDocs: []string{"guide.md"},
			wantNot:  []string{"image.png", "data.json", "notes.txt"},
		},
		{
			name:     "empty docs directory",
			files:    map[string]string{},
			wantDocs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			teamDir := t.TempDir()
			docsDir := filepath.Join(teamDir, "docs")
			if err := os.MkdirAll(docsDir, 0o755); err != nil {
				t.Fatal(err)
			}

			for name, content := range tt.files {
				if err := os.WriteFile(filepath.Join(docsDir, name), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			docs, err := DiscoverDocs(teamDir)
			if err != nil {
				t.Fatalf("DiscoverDocs() error: %v", err)
			}

			gotNames := make(map[string]bool)
			for _, d := range docs {
				gotNames[d.Name] = true
			}

			for _, want := range tt.wantDocs {
				if !gotNames[want] {
					t.Errorf("expected doc %q in results, got %v", want, docNames(docs))
				}
			}

			for _, notWant := range tt.wantNot {
				if gotNames[notWant] {
					t.Errorf("did not expect doc %q in results, got %v", notWant, docNames(docs))
				}
			}

			if tt.wantDocs == nil && len(docs) != 0 {
				t.Errorf("expected no docs, got %v", docNames(docs))
			}
		})
	}
}

func TestDiscoverDocs_MissingDocsDir(t *testing.T) {
	teamDir := t.TempDir()
	// no docs/ subdirectory created

	docs, err := DiscoverDocs(teamDir)
	if err != nil {
		t.Fatalf("expected no error for missing docs dir, got: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("expected empty result, got %d docs", len(docs))
	}
}

func TestParseFrontmatter(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    docFrontmatter
	}{
		{
			name:    "full frontmatter",
			content: "---\ntitle: My Title\ndescription: My description\nvisibility: indexed\nwhen: making changes\n---\n# Content",
			want: docFrontmatter{
				Title:       "My Title",
				Description: "My description",
				Visibility:  "indexed",
				When:        "making changes",
			},
		},
		{
			name:    "multi-line when with >-",
			content: "---\ntitle: Guide\nwhen: >-\n  architectural decisions,\n  product direction changes\n---\n",
			want: docFrontmatter{
				Title: "Guide",
				When:  "architectural decisions, product direction changes",
			},
		},
		{
			name:    "quoted values",
			content: "---\ntitle: \"Quoted Title\"\ndescription: 'Single quoted'\n---\n",
			want: docFrontmatter{
				Title:       "Quoted Title",
				Description: "Single quoted",
			},
		},
		{
			name:    "no frontmatter",
			content: "# Just a heading\nNo frontmatter here.",
			want:    docFrontmatter{},
		},
		{
			name:    "empty file",
			content: "",
			want:    docFrontmatter{},
		},
		{
			name:    "visibility hidden",
			content: "---\nvisibility: hidden\n---\n# Hidden Doc",
			want: docFrontmatter{
				Visibility: "hidden",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "test.md")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}

			got := parseFrontmatter(path)

			if got.Title != tt.want.Title {
				t.Errorf("title: got %q, want %q", got.Title, tt.want.Title)
			}
			if got.Description != tt.want.Description {
				t.Errorf("description: got %q, want %q", got.Description, tt.want.Description)
			}
			if got.Visibility != tt.want.Visibility {
				t.Errorf("visibility: got %q, want %q", got.Visibility, tt.want.Visibility)
			}
			if got.When != tt.want.When {
				t.Errorf("when: got %q, want %q", got.When, tt.want.When)
			}
		})
	}
}

func TestExtractTitleFromContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "simple H1",
			content: "# My Title\nContent.",
			want:    "My Title",
		},
		{
			name:    "H1 after frontmatter",
			content: "---\ntitle: FM Title\n---\n# Content Title\nBody.",
			want:    "Content Title",
		},
		{
			name:    "no heading",
			content: "Just text, no heading.",
			want:    "",
		},
		{
			name:    "H2 not H1",
			content: "## Subtitle\nContent.",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "test.md")
			os.WriteFile(path, []byte(tt.content), 0o644)
			got := extractTitleFromContent(path)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractDescriptionFromContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "paragraph after heading",
			content: "# Title\n\nThis is the description paragraph.\n",
			want:    "This is the description paragraph.",
		},
		{
			name:    "skips headings and blank lines",
			content: "# Title\n\n## Subtitle\n\nActual content here.",
			want:    "Actual content here.",
		},
		{
			name:    "after frontmatter",
			content: "---\ntitle: T\n---\n# Title\n\nDescription here.",
			want:    "Description here.",
		},
		{
			name:    "long paragraph truncated",
			content: "# Title\n\n" + strings.Repeat("A", 200),
			want:    strings.Repeat("A", 157) + "...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "test.md")
			os.WriteFile(path, []byte(tt.content), 0o644)
			got := extractDescriptionFromContent(path)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTitleFromFilename(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"principles.md", "Principles"},
		{"api-conventions.md", "Api Conventions"},
		{"my_guide.md", "My Guide"},
		{"README.md", "README"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := titleFromFilename(tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildDoc_VisibilityAlwaysTreatedAsIndexed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "glossary.md")
	content := "---\ntitle: Glossary\nvisibility: always\n---\n# Glossary\nTerms here."
	os.WriteFile(path, []byte(content), 0o644)

	doc := buildDoc("glossary.md", path)

	// "always" is accepted but treated as "indexed" until auto-inlining ships
	if doc.Visibility != VisibilityIndexed {
		t.Errorf("expected visibility %q, got %q", VisibilityIndexed, doc.Visibility)
	}
}

func TestDiscoverDocs_SortedByName(t *testing.T) {
	teamDir := t.TempDir()
	docsDir := filepath.Join(teamDir, "docs")
	os.MkdirAll(docsDir, 0o755)

	files := []string{"zebra.md", "alpha.md", "middle.md"}
	for _, f := range files {
		os.WriteFile(filepath.Join(docsDir, f), []byte("# "+f+"\nContent."), 0o644)
	}

	docs, err := DiscoverDocs(teamDir)
	if err != nil {
		t.Fatal(err)
	}

	if len(docs) != 3 {
		t.Fatalf("expected 3 docs, got %d", len(docs))
	}
	if docs[0].Name != "alpha.md" || docs[1].Name != "middle.md" || docs[2].Name != "zebra.md" {
		t.Errorf("expected sorted order, got %v", docNames(docs))
	}
}

// docNames returns a slice of doc names for test error messages.
func docNames(docs []TeamDoc) []string {
	names := make([]string, len(docs))
	for i, d := range docs {
		names[i] = d.Name
	}
	return names
}
