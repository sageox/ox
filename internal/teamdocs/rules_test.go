package teamdocs

import (
	"os"
	"path/filepath"
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
