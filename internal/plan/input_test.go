package plan

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		wantHeadings []string
		// wantFiles maps a section heading to the file refs expected in it.
		wantFiles map[string][]string
	}{
		{
			name:         "empty input yields no sections",
			raw:          "",
			wantHeadings: nil,
		},
		{
			name:         "whitespace-only input yields no sections",
			raw:          "   \n\t\n  ",
			wantHeadings: nil,
		},
		{
			name:         "single H2 section",
			raw:          "## Overview\nSome body text here.",
			wantHeadings: []string{"Overview"},
			wantFiles:    map[string][]string{"Overview": nil},
		},
		{
			name:         "multiple H2 sections split correctly",
			raw:          "## First\nbody one\n\n## Second\nbody two\n\n## Third\nbody three",
			wantHeadings: []string{"First", "Second", "Third"},
		},
		{
			name:         "preamble before first H2 becomes empty-heading section",
			raw:          "Intro paragraph before any heading.\n\n## Real Section\nbody",
			wantHeadings: []string{"", "Real Section"},
		},
		{
			name:         "backtick-quoted file paths extracted",
			raw:          "## Changes\nEdit `internal/plan/input.go` and `cmd/ox/plan.go`.",
			wantHeadings: []string{"Changes"},
			wantFiles: map[string][]string{
				"Changes": {"cmd/ox/plan.go", "internal/plan/input.go"},
			},
		},
		{
			name:         "path:line refs extracted",
			raw:          "## Bug\nThe issue is at internal/plan/enrich.go:42 in the loop.",
			wantHeadings: []string{"Bug"},
			wantFiles: map[string][]string{
				"Bug": {"internal/plan/enrich.go:42"},
			},
		},
		{
			name:         "non-path backticks are ignored",
			raw:          "## Notes\nRun `make build` and check `the result`.",
			wantHeadings: []string{"Notes"},
			wantFiles:    map[string][]string{"Notes": nil},
		},
		{
			name:         "duplicate file refs deduped and sorted",
			raw:          "## Dup\nTouch `a/b.go`, then `a/b.go` again, plus `a/a.go`.",
			wantHeadings: []string{"Dup"},
			wantFiles: map[string][]string{
				"Dup": {"a/a.go", "a/b.go"},
			},
		},
		{
			name:         "bare extension file without directory extracted",
			raw:          "## Top\nUpdate `Makefile.go` here.",
			wantHeadings: []string{"Top"},
			wantFiles: map[string][]string{
				"Top": {"Makefile.go"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := Parse(tt.raw)

			if in.Raw != tt.raw {
				t.Errorf("Raw mismatch: got %q want %q", in.Raw, tt.raw)
			}

			var gotHeadings []string
			for _, s := range in.Sections {
				gotHeadings = append(gotHeadings, s.Heading)
			}
			if !reflect.DeepEqual(gotHeadings, tt.wantHeadings) {
				t.Errorf("headings: got %v want %v", gotHeadings, tt.wantHeadings)
			}

			for heading, wantFiles := range tt.wantFiles {
				sec := findSection(in.Sections, heading)
				if sec == nil {
					t.Fatalf("section %q not found", heading)
				}
				if !reflect.DeepEqual(sec.Files, wantFiles) {
					t.Errorf("section %q files: got %v want %v", heading, sec.Files, wantFiles)
				}
			}
		})
	}
}

func TestResolveEmptyStdin(t *testing.T) {
	// point auto-discovery at an empty dir so the test never reads the real
	// ~/.claude/plans and an empty stdin yields an empty Input.
	withPlanModeDir(t, t.TempDir())
	in, err := Resolve("", strings.NewReader(""))
	if err != nil {
		t.Fatalf("Resolve empty stdin: unexpected error %v", err)
	}
	if len(in.Sections) != 0 {
		t.Errorf("expected no sections for empty input, got %d", len(in.Sections))
	}
}

func TestResolveNilStdin(t *testing.T) {
	withPlanModeDir(t, t.TempDir())
	in, err := Resolve("", nil)
	if err != nil {
		t.Fatalf("Resolve nil stdin: unexpected error %v", err)
	}
	if len(in.Sections) != 0 {
		t.Errorf("expected no sections for nil input, got %d", len(in.Sections))
	}
}

// withPlanModeDir points auto-discovery at a temp dir for the duration of a
// test, restoring the default afterward. NEVER touches the real ~/.claude/plans.
func withPlanModeDir(t *testing.T, dir string) {
	t.Helper()
	prev := planModeDirOverride
	planModeDirOverride = dir
	t.Cleanup(func() { planModeDirOverride = prev })
}

// writePlanFile writes a markdown plan with a controlled mod time so the
// newest-file selection is deterministic regardless of filesystem timestamp
// granularity.
func writePlanFile(t *testing.T, dir, name, body string, mod time.Time) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestResolve_AutoDiscoversNewestPlanFile verifies that with no --file and no
// piped stdin, Resolve picks the NEWEST *.md under the plan-mode dir.
// Failure prevented: the human 'ox plan' path silently enriching nothing (or
// an arbitrary/oldest file) when plan-mode already wrote the active plan.
func TestResolve_AutoDiscoversNewestPlanFile(t *testing.T) {
	dir := t.TempDir()
	withPlanModeDir(t, dir)

	base := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	writePlanFile(t, dir, "old-plan.md", "## Old\nstale plan", base.Add(-2*time.Hour))
	newest := writePlanFile(t, dir, "active-plan.md", "## Active\nthe current plan", base)
	// a non-markdown file in the dir must be ignored.
	writePlanFile(t, dir, "notes.txt", "not a plan", base.Add(time.Hour))

	in, err := Resolve("", nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if in.Path != newest {
		t.Errorf("auto-discovered path = %q, want %q (newest .md)", in.Path, newest)
	}
	if !strings.Contains(in.Raw, "the current plan") {
		t.Errorf("expected newest plan content, got %q", in.Raw)
	}
}

// TestResolve_FilePrecedenceOverDiscovery verifies --file wins over a present
// auto-discoverable plan-mode file. Agents/explicit callers must not be
// overridden by whatever happens to sit in ~/.claude/plans.
func TestResolve_FilePrecedenceOverDiscovery(t *testing.T) {
	planModeDir := t.TempDir()
	withPlanModeDir(t, planModeDir)
	writePlanFile(t, planModeDir, "discovered.md", "## Discovered\nshould be ignored", time.Now())

	explicitDir := t.TempDir()
	explicit := writePlanFile(t, explicitDir, "explicit.md", "## Explicit\nuse me", time.Now())

	in, err := Resolve(explicit, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if in.Path != explicit {
		t.Errorf("path = %q, want %q (--file precedence)", in.Path, explicit)
	}
	if !strings.Contains(in.Raw, "use me") {
		t.Errorf("expected --file content, got %q", in.Raw)
	}
}

// TestResolve_StdinPrecedenceOverDiscovery verifies a piped stdin wins over
// auto-discovery — agents pipe the plan explicitly and must not be shadowed.
func TestResolve_StdinPrecedenceOverDiscovery(t *testing.T) {
	planModeDir := t.TempDir()
	withPlanModeDir(t, planModeDir)
	writePlanFile(t, planModeDir, "discovered.md", "## Discovered\nignore me", time.Now())

	in, err := Resolve("", strings.NewReader("## Piped\nfrom stdin"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if in.Path != "" {
		t.Errorf("stdin source should have empty Path, got %q", in.Path)
	}
	if !strings.Contains(in.Raw, "from stdin") {
		t.Errorf("expected stdin content, got %q", in.Raw)
	}
}

// TestResolve_EmptyWhenNoDiscoverablePlan verifies discovery degrades to an
// empty Input when the plan-mode dir is empty or missing — the fail-open
// contract the caller relies on to print a clear "no plan found" message.
func TestResolve_EmptyWhenNoDiscoverablePlan(t *testing.T) {
	t.Run("empty dir", func(t *testing.T) {
		withPlanModeDir(t, t.TempDir())
		in, err := Resolve("", nil)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if strings.TrimSpace(in.Raw) != "" {
			t.Errorf("expected empty Raw, got %q", in.Raw)
		}
	})

	t.Run("missing dir", func(t *testing.T) {
		withPlanModeDir(t, filepath.Join(t.TempDir(), "does-not-exist"))
		in, err := Resolve("", nil)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if strings.TrimSpace(in.Raw) != "" {
			t.Errorf("expected empty Raw, got %q", in.Raw)
		}
	})

	t.Run("empty piped stdin falls through to discovery", func(t *testing.T) {
		dir := t.TempDir()
		withPlanModeDir(t, dir)
		discovered := writePlanFile(t, dir, "p.md", "## Found\nvia discovery", time.Now())
		// an empty (but non-nil) pipe must not suppress auto-discovery.
		in, err := Resolve("", strings.NewReader("   \n"))
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if in.Path != discovered {
			t.Errorf("path = %q, want %q (empty stdin should fall through)", in.Path, discovered)
		}
	})
}

func findSection(sections []Section, heading string) *Section {
	for i := range sections {
		if sections[i].Heading == heading {
			return &sections[i]
		}
	}
	return nil
}

// TestResolveInput mirrors decision.TestResolveInput's structure: topic beats
// file beats stdin/auto-discovery, and the full-document delegation is exact.
func TestResolveInput(t *testing.T) {
	t.Run("topic wins over file and stdin", func(t *testing.T) {
		explicit := writePlanFile(t, t.TempDir(), "explicit.md", "## Explicit\nignored when topic is set", time.Now())

		in, err := ResolveInput("my topic", nil, explicit, strings.NewReader("ignored stdin"))
		if err != nil {
			t.Fatalf("ResolveInput: %v", err)
		}
		if in.Topic != "my topic" {
			t.Errorf("Topic = %q, want %q", in.Topic, "my topic")
		}
		if in.Raw != "" {
			t.Errorf("Raw must stay empty in topic mode, got %q", in.Raw)
		}
		if in.Path != "" {
			t.Errorf("Path must stay empty in topic mode, got %q", in.Path)
		}
	})

	t.Run("topic with files populates Files and a synthetic preamble section", func(t *testing.T) {
		in, err := ResolveInput("auth refresh flow", []string{"internal/auth/token.go", " internal/auth/refresh.go ", "internal/auth/token.go"}, "", nil)
		if err != nil {
			t.Fatalf("ResolveInput: %v", err)
		}
		want := []string{"internal/auth/refresh.go", "internal/auth/token.go"}
		if !reflect.DeepEqual(in.Files, want) {
			t.Errorf("Files = %v, want %v (trimmed, deduped, sorted)", in.Files, want)
		}
		if len(in.Sections) != 1 {
			t.Fatalf("expected exactly one synthetic section, got %d: %+v", len(in.Sections), in.Sections)
		}
		sec := in.Sections[0]
		if sec.Heading != "" {
			t.Errorf("synthetic section Heading must stay empty (preamble semantics), got %q", sec.Heading)
		}
		if sec.Body != "auth refresh flow" {
			t.Errorf("synthetic section Body = %q, want the topic text", sec.Body)
		}
		if !reflect.DeepEqual(sec.Files, want) {
			t.Errorf("synthetic section Files = %v, want %v", sec.Files, want)
		}
	})

	t.Run("empty topic delegates to Resolve: file mode", func(t *testing.T) {
		withPlanModeDir(t, t.TempDir())
		explicit := writePlanFile(t, t.TempDir(), "explicit.md", "## Explicit\nfull doc path", time.Now())

		in, err := ResolveInput("", nil, explicit, nil)
		if err != nil {
			t.Fatalf("ResolveInput: %v", err)
		}
		if in.Topic != "" {
			t.Errorf("Topic must stay empty, got %q", in.Topic)
		}
		if in.Path != explicit {
			t.Errorf("path = %q, want %q (delegated to Resolve)", in.Path, explicit)
		}
		if !strings.Contains(in.Raw, "full doc path") {
			t.Errorf("expected file content via Resolve, got %q", in.Raw)
		}
	})

	t.Run("empty topic delegates to Resolve: stdin", func(t *testing.T) {
		withPlanModeDir(t, t.TempDir())
		in, err := ResolveInput("", nil, "", strings.NewReader("## Piped\nfrom stdin"))
		if err != nil {
			t.Fatalf("ResolveInput: %v", err)
		}
		if in.Topic != "" {
			t.Errorf("Topic must stay empty, got %q", in.Topic)
		}
		if !strings.Contains(in.Raw, "from stdin") {
			t.Errorf("expected stdin content via Resolve, got %q", in.Raw)
		}
	})

	t.Run("whitespace-only topic is not a topic", func(t *testing.T) {
		withPlanModeDir(t, t.TempDir())
		in, err := ResolveInput("   ", nil, "", strings.NewReader("## Piped\nstdin wins when topic is blank"))
		if err != nil {
			t.Fatalf("ResolveInput: %v", err)
		}
		if in.Topic != "" {
			t.Errorf("whitespace-only topic must not count as a topic, got %q", in.Topic)
		}
		if !strings.Contains(in.Raw, "stdin wins") {
			t.Errorf("expected fallthrough to stdin, got %q", in.Raw)
		}
	})
}

func TestNormalizeExplicitFiles(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil", nil, nil},
		{"empty and whitespace-only entries dropped", []string{"", "   ", "a.go"}, []string{"a.go"}},
		{"duplicates collapsed", []string{"a.go", "a.go"}, []string{"a.go"}},
		{"sorted", []string{"b.go", "a.go"}, []string{"a.go", "b.go"}},
		{"trimmed", []string{"  a.go  "}, []string{"a.go"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeExplicitFiles(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("normalizeExplicitFiles(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestLooksLikeHTML_NeverMisroutesPageOrMarkdown: without it a .html page with no
// doctype saves as markdown (ox#1070), or a markdown plan that embeds html is read as a page.
func TestLooksLikeHTML_NeverMisroutesPageOrMarkdown(t *testing.T) {
	const fragment = `<title>My Plan</title><style>body{color:#111}</style><h1>Real title</h1><p>Body.</p>`
	tests := []struct {
		name string
		path string
		raw  string
		want bool
	}{
		{"fragment opening with title", "plan.html", fragment, true},
		{"fragment opening with a div", "plan.html", `<div class="app"><h1>Plan</h1></div>`, true},
		{"document behind a byte order mark", "plan.html", "\ufeff<!DOCTYPE html><html><body></body></html>", true},
		{"document behind a leading comment", "plan.html", "<!-- generated -->\n<!doctype html><html><body></body></html>", true},
		{"xhtml behind an xml prolog", "plan.htm", "<?xml version=\"1.0\"?>\n<!DOCTYPE html>\n<html><body></body></html>", true},
		{"upper-case extension", "PLAN.HTML", fragment, true},
		{"piped full document", "", "\n  <!DOCTYPE html>\n<html><body></body></html>", true},
		{"piped markdown opening with a tag", "", "<p align=\"center\">Logo</p>\n\n# Plan\n", false},
		{"markdown file opening with a tag", "plan.md", "<div align=\"center\"><img src=\"logo.png\"></div>\n\n# Plan\n", false},
		{"markdown file embedding an html fence", "plan.md", "# Plan\n\n```html-interactive\n<!doctype html><html><body>demo</body></html>\n```\n", false},
		// Empty input stays on the markdown path, which owns the empty-plan handling.
		{"whitespace-only page", "plan.html", " \n\t", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LooksLikeHTML(tt.path, tt.raw); got != tt.want {
				t.Errorf("LooksLikeHTML(%q, %.40q) = %v, want %v", tt.path, tt.raw, got, tt.want)
			}
		})
	}
}
