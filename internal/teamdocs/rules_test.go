package teamdocs

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeRule(t *testing.T, root, relPath, content string) {
	t.Helper()
	abs := filepath.Join(root, relPath)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", abs, err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", abs, err)
	}
}

func TestDiscoverRules_BasicShape(t *testing.T) {
	team := t.TempDir()

	writeRule(t, team, "agents/rules/escalation.md", `---
name: escalation-policy
description: When to page a human.
visibility: always
---
**Why:** Some calls need a human.
**How to apply:** Page on auth/payment/data-loss issues.
`)

	writeRule(t, team, "agents/rules/backend/postgres.md", `---
name: postgres-uses-jsonb
description: Prefer JSONB metadata columns.
visibility: indexed
repos: ["sageox/ox"]
---
Body content here.
`)

	writeRule(t, team, "agents/rules/draft.md", `---
name: not-yet
description: Work in progress.
status: draft
---
draft body
`)

	writeRule(t, team, "agents/rules/old.md", `---
name: superseded-rule
description: Replaced.
status: superseded-by:postgres-uses-jsonb
---
old body
`)

	writeRule(t, team, "agents/rules/human-only.md", `---
name: humans-only
description: Reading material.
audience: human
---
text
`)

	rules, err := DiscoverRules(team, "sageox/ox")
	if err != nil {
		t.Fatalf("DiscoverRules: %v", err)
	}

	gotNames := make([]string, 0, len(rules))
	for _, r := range rules {
		gotNames = append(gotNames, r.Name)
	}

	wantNames := []string{"postgres-uses-jsonb", "escalation-policy"}
	if len(gotNames) != len(wantNames) {
		t.Fatalf("got rules %v, want %v", gotNames, wantNames)
	}

	// rules should be sorted by RelPath: backend/postgres.md before escalation.md
	if rules[0].Name != "postgres-uses-jsonb" {
		t.Errorf("rules[0] = %s, want postgres-uses-jsonb", rules[0].Name)
	}
	if rules[1].Name != "escalation-policy" {
		t.Errorf("rules[1] = %s, want escalation-policy", rules[1].Name)
	}

	// always-tier rule body should be loaded
	if rules[1].Body == "" {
		t.Errorf("escalation-policy.Body should be populated for visibility=always")
	}
	if !strings.Contains(rules[1].Body, "**Why:**") {
		t.Errorf("escalation-policy.Body missing expected content: %q", rules[1].Body)
	}
	if rules[1].EstimatedTokens == 0 {
		t.Errorf("escalation-policy.EstimatedTokens should be > 0 for visibility=always")
	}

	// indexed-tier rule should NOT have body loaded
	if rules[0].Body != "" {
		t.Errorf("postgres-uses-jsonb.Body should be empty for visibility=indexed, got %q", rules[0].Body)
	}

	// subdirectory RelPath is preserved
	if rules[0].RelPath != filepath.Join("backend", "postgres.md") {
		t.Errorf("rules[0].RelPath = %q, want backend/postgres.md", rules[0].RelPath)
	}
}

func TestDiscoverRules_RepoFilter(t *testing.T) {
	team := t.TempDir()

	writeRule(t, team, "agents/rules/all-repos.md", `---
name: all-repos
description: Applies everywhere.
---
body
`)

	writeRule(t, team, "agents/rules/only-ox.md", `---
name: only-ox
description: Only for ox repo.
repos: ["sageox/ox"]
---
body
`)

	writeRule(t, team, "agents/rules/only-cloud.md", `---
name: only-cloud
description: Only for cloud-api.
repos: ["sageox/cloud-api"]
---
body
`)

	rules, err := DiscoverRules(team, "sageox/ox")
	if err != nil {
		t.Fatalf("DiscoverRules: %v", err)
	}

	got := map[string]bool{}
	for _, r := range rules {
		got[r.Name] = true
	}

	if !got["all-repos"] {
		t.Errorf("expected all-repos rule (no filter) to apply to sageox/ox")
	}
	if !got["only-ox"] {
		t.Errorf("expected only-ox rule to apply to sageox/ox")
	}
	if got["only-cloud"] {
		t.Errorf("only-cloud rule should NOT apply to sageox/ox")
	}
}

func TestDiscoverRules_LegacyCoworkersFallback(t *testing.T) {
	team := t.TempDir()

	writeRule(t, team, "coworkers/rules/legacy.md", `---
name: legacy-rule
description: Lived in old location.
---
body
`)

	rules, err := DiscoverRules(team, "")
	if err != nil {
		t.Fatalf("DiscoverRules: %v", err)
	}

	if len(rules) != 1 || rules[0].Name != "legacy-rule" {
		t.Errorf("expected legacy-rule from coworkers/rules/, got %+v", rules)
	}
}

func TestDiscoverRules_PreferAgentsOverCoworkers(t *testing.T) {
	team := t.TempDir()

	// same Name in both locations — agents/ should win
	writeRule(t, team, "agents/rules/dup.md", `---
name: same-name
description: New location.
---
new body
`)
	writeRule(t, team, "coworkers/rules/dup.md", `---
name: same-name
description: Old location.
---
old body
`)

	rules, err := DiscoverRules(team, "")
	if err != nil {
		t.Fatalf("DiscoverRules: %v", err)
	}

	if len(rules) != 1 {
		t.Fatalf("expected 1 deduped rule, got %d", len(rules))
	}
	if !strings.Contains(rules[0].AbsPath, filepath.Join("agents", "rules")) {
		t.Errorf("expected agents/rules to win on dedupe, got AbsPath %q", rules[0].AbsPath)
	}
}

func TestDiscoverRules_MissingDir(t *testing.T) {
	team := t.TempDir()
	rules, err := DiscoverRules(team, "")
	if err != nil {
		t.Fatalf("DiscoverRules on empty team should not error: %v", err)
	}
	if len(rules) != 0 {
		t.Errorf("expected no rules in empty team dir, got %d", len(rules))
	}
}

func TestDiscoverRules_DefaultsApplied(t *testing.T) {
	team := t.TempDir()

	writeRule(t, team, "agents/rules/no-frontmatter-fields.md", `---
name: minimal
description: Has only required fields.
---
body
`)

	rules, err := DiscoverRules(team, "")
	if err != nil {
		t.Fatalf("DiscoverRules: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	r := rules[0]
	if r.Visibility != VisibilityIndexed {
		t.Errorf("default visibility should be indexed, got %q", r.Visibility)
	}
	if r.Audience != RuleAudienceAI {
		t.Errorf("default audience should be ai, got %q", r.Audience)
	}
	if r.Status != RuleStatusActive {
		t.Errorf("default status should be active, got %q", r.Status)
	}
}

func TestParseInlineList(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`["a", "b"]`, []string{"a", "b"}},
		{`[a, b, c]`, []string{"a", "b", "c"}},
		{`[]`, nil},
		{``, nil},
		{`["sageox/ox"]`, []string{"sageox/ox"}},
		{`sageox/ox`, []string{"sageox/ox"}},
	}
	for _, c := range cases {
		got := parseInlineList(c.in)
		if len(got) != len(c.want) {
			t.Errorf("parseInlineList(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("parseInlineList(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestReadRuleBody_StripsFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rule.md")
	if err := os.WriteFile(path, []byte(`---
name: test
description: foo
---
This is the body.

Second paragraph.
`), 0o644); err != nil {
		t.Fatal(err)
	}

	body, err := readRuleBody(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "name: test") {
		t.Errorf("body should not contain frontmatter: %q", body)
	}
	if !strings.HasPrefix(body, "This is the body.") {
		t.Errorf("body should start with content: %q", body)
	}
}

func TestReadRuleBody_NoFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rule.md")
	if err := os.WriteFile(path, []byte("Just a body, no frontmatter.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, err := readRuleBody(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(body, "Just a body") {
		t.Errorf("body without frontmatter should pass through: %q", body)
	}
}

// TestDiscoverRules_Globs covers the scope field that lets a rule be
// team-general AND path-scoped at the same time.
//
// That quadrant is real and had no expression before: Go error-wrapping idioms,
// Terraform conventions, and SQL migration rules all apply to every repo on the
// team but only to some files in them. Without globs: an author's only options
// were to load the rule in every session or to duplicate it into each repo's
// local rules — the copies-that-rot problem team rules exist to solve.
func TestDiscoverRules_Globs(t *testing.T) {
	tests := []struct {
		name      string
		globsLine string
		want      []string
	}{
		{"absent", "", nil},
		{"inline list, matching repos: style", `globs: ["**/*.go", "**/*.mod"]`, []string{"**/*.go", "**/*.mod"}},
		// An author copying a rule out of .cursor/rules writes the bare comma form.
		// Rejecting it would leave the rule unscoped — loading everywhere, which is
		// the exact outcome globs: exists to prevent.
		{"bare comma form, as Cursor and Copilot write it", "globs: **/*.go,**/*.mod", []string{"**/*.go", "**/*.mod"}},
		{"single bare glob", "globs: migrations/**", []string{"migrations/**"}},
		{"quoted single", `globs: "**/*.tf"`, []string{"**/*.tf"}},
		// End to end: the literal hash must survive into the parsed glob, not just
		// into the un-stripped value.
		{"quoted entry containing a hash", `globs: ["**/*.go # generated sources"]`, []string{"**/*.go # generated sources"}},
		{"empty value is not a scope", "globs:", nil},
		{"empty list is not a scope", "globs: []", nil},
		// The guide's own examples carried trailing comments. Parsed literally they
		// produced glob entries named "Cursor" and "Copilot" — a rule scoped to
		// files that cannot exist, so it silently never applies. Anyone copying the
		// documentation got a rule that looked scoped and was not.
		{"trailing comment on the bare form", "globs: **/*.go,**/*.mod   # matches Cursor, Copilot, Cline", []string{"**/*.go", "**/*.mod"}},
		{"trailing comment on the inline form", `globs: ["**/*.go", "**/*.mod"]   # matches the repos: style`, []string{"**/*.go", "**/*.mod"}},
		{"comment-only value is no scope", "globs: # TODO decide", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			body := "---\nname: scoped\ndescription: A scoped rule.\n"
			if tt.globsLine != "" {
				body += tt.globsLine + "\n"
			}
			body += "---\n\nUse errors.Is.\n"
			writeRule(t, root, "agents/rules/scoped.md", body)

			rules, err := DiscoverRules(root, "acme/api")
			if err != nil {
				t.Fatalf("DiscoverRules: %v", err)
			}
			if len(rules) != 1 {
				t.Fatalf("got %d rules, want 1", len(rules))
			}
			if !slices.Equal(rules[0].Globs, tt.want) {
				t.Errorf("Globs = %#v, want %#v", rules[0].Globs, tt.want)
			}
		})
	}
}

// TestDiscoverRules_TrailingCommentsDoNotLeakIntoValues covers the whole
// frontmatter surface, not just globs: extractValue did not strip comments
// either, so `name: foo # note` carried the comment into the identifier that
// cross-references and superseded-by resolve against.
//
// A value that OPENS with a quote keeps its `#`, because there it is literal
// YAML and truncating would corrupt a description that legitimately contains one.
func TestDiscoverRules_TrailingCommentsDoNotLeakIntoValues(t *testing.T) {
	root := t.TempDir()
	writeRule(t, root, "agents/rules/commented.md",
		"---\nname: commented   # the identifier\ndescription: Wrap errors.   # why\nrepos: [\"acme/api\"]   # only the API\n---\n\nBody.\n")
	writeRule(t, root, "agents/rules/quoted.md",
		"---\nname: quoted\ndescription: \"Use #tags in commit messages\"\n---\n\nBody.\n")

	rules, err := DiscoverRules(root, "acme/api")
	if err != nil {
		t.Fatalf("DiscoverRules: %v", err)
	}
	byName := map[string]TeamRule{}
	for _, r := range rules {
		byName[r.Name] = r
	}

	if _, ok := byName["commented"]; !ok {
		t.Fatalf("the name carried its trailing comment; got %v", keysOf(byName))
	}
	if got := byName["commented"].Description; got != "Wrap errors." {
		t.Errorf("Description = %q, want the comment stripped", got)
	}
	if got := byName["commented"].Repos; !slices.Equal(got, []string{"acme/api"}) {
		t.Errorf("Repos = %#v, want the comment stripped", got)
	}
	if got := byName["quoted"].Description; got != "Use #tags in commit messages" {
		t.Errorf("Description = %q — a quoted # is literal YAML and must survive", got)
	}
}

// TestStripYAMLComment_QuoteEscapes: both escape forms must survive, or a value
// is silently truncated at its first inner quote and the reader cannot tell.
func TestStripYAMLComment_QuoteEscapes(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"plain double", `"hello"`, `"hello"`},
		{"plain single", `'hello'`, `'hello'`},
		{"double then comment", `"hello"   # note`, `"hello"`},
		{"single then comment", `'hello'   # note`, `'hello'`},
		// YAML doubles a single quote to escape it. Scanning for the first quote
		// truncated 'It''s fine' to It, and apostrophes in descriptions are common.
		{"doubled single quote", `'It''s fine'`, `'It''s fine'`},
		{"doubled single quote then comment", `'It''s fine'  # yes`, `'It''s fine'`},
		{"backslash-escaped double quote", `"say \"hi\" now"`, `"say \"hi\" now"`},
		// A # inside quotes is literal YAML, not a comment.
		{"hash inside quotes", `"Use #tags here"`, `"Use #tags here"`},
		{"unterminated stays whole", `"no closing quote`, `"no closing quote`},
		{"unquoted with comment", `bare value  # note`, `bare value`},
		{"comment only", `# nothing`, ``},
		// A # inside a quoted entry of a flow sequence is literal. Cutting at the
		// first " #" produced the unparseable `["**/*.go`, which told the agent a
		// scope that matches nothing the author meant.
		{"hash inside a quoted sequence entry", `["**/*.go # generated sources"]`, `["**/*.go # generated sources"]`},
		{"sequence then comment", `["**/*.go", "**/*.mod"]   # note`, `["**/*.go", "**/*.mod"]`},
		{"sequence with apostrophe entry", `['it''s', "b"]  # note`, `['it''s', "b"]`},
		{"unterminated sequence stays whole", `["**/*.go"`, `["**/*.go"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripYAMLComment(tt.in); got != tt.want {
				t.Errorf("stripYAMLComment(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func keysOf(m map[string]TeamRule) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// TestDiscoverRules_GlobsDoNotFilterDiscovery: globs describe WHERE a rule
// applies inside a repo, not WHETHER the repo gets it. That is repos:.
//
// Conflating them would silently drop scoped rules from discovery, and the
// symptom — a rule that exists in the team repo but never reaches anyone — is
// the same one an unmaterialized checkout produces, so it would be diagnosed
// as a sync problem rather than a filter bug.
func TestDiscoverRules_GlobsDoNotFilterDiscovery(t *testing.T) {
	root := t.TempDir()
	writeRule(t, root, "agents/rules/go-idioms.md",
		"---\nname: go-idioms\ndescription: Wrap errors.\nglobs: [\"**/*.go\"]\n---\n\nUse %w.\n")

	rules, err := DiscoverRules(root, "acme/web-frontend")
	if err != nil {
		t.Fatalf("DiscoverRules: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("a globbed rule was filtered out of a repo with no matching files yet: got %d rules", len(rules))
	}
	if !slices.Equal(rules[0].Globs, []string{"**/*.go"}) {
		t.Errorf("Globs = %#v", rules[0].Globs)
	}
}
