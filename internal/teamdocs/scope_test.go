package teamdocs

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestParseRuleScope covers each spelling that used to read as absent and so widened
// a rule, next to the spellings that must keep working.
func TestParseRuleScope(t *testing.T) {
	t.Parallel()
	const padding = "description: >-\n  a long folded description\n  that runs on\n"
	tests := []struct {
		name      string
		manifest  string
		wantKind  ScopeKind
		wantRepos []string
		wantGlobs []string
	}{
		{name: "no frontmatter", manifest: "Just a body.\n", wantKind: ScopeUnscoped},
		{name: "empty frontmatter", manifest: "---\n---\nBody.\n", wantKind: ScopeUnscoped},
		{name: "comment-only frontmatter", manifest: "---\n# nothing yet\n---\nBody.\n", wantKind: ScopeUnscoped},
		{name: "repaired block with an explicit empty repos", manifest: "---\ndescription: Use when: testing\nrepos: []\n---\nBody.\n", wantKind: ScopeUnscoped},
		{name: "repaired block with a null globs", manifest: "---\ndescription: Use when: testing\nglobs: ~\n---\nBody.\n", wantKind: ScopeUnscoped},
		{name: "no scope keys", manifest: "---\nname: a\n---\n", wantKind: ScopeUnscoped},
		{name: "empty list means all", manifest: "---\nrepos: []\nglobs: []\n---\n", wantKind: ScopeUnscoped},
		{name: "null value means all", manifest: "---\nrepos:\nglobs: ~\n---\n", wantKind: ScopeUnscoped},
		{name: "empty string means all", manifest: "---\nglobs: \"\"\n---\n", wantKind: ScopeUnscoped},

		{name: "flow lists", manifest: "---\nrepos: [\"a/b\"]\nglobs: [\"**/*.go\"]\n---\n",
			wantKind: ScopeScoped, wantRepos: []string{"a/b"}, wantGlobs: []string{"**/*.go"}},
		{name: "space before the colon", manifest: "---\nglobs : [\"**/*.go\"]\n---\n",
			wantKind: ScopeScoped, wantGlobs: []string{"**/*.go"}},
		{name: "double-quoted key", manifest: "---\n\"globs\": [\"**/*.go\"]\n---\n",
			wantKind: ScopeScoped, wantGlobs: []string{"**/*.go"}},
		{name: "single-quoted key", manifest: "---\n'repos': [a/b]\n---\n",
			wantKind: ScopeScoped, wantRepos: []string{"a/b"}},
		{name: "folded scalar", manifest: "---\nglobs: >-\n  a/*.go,\n  b/*.go\n---\n",
			wantKind: ScopeScoped, wantGlobs: []string{"a/*.go", "b/*.go"}},
		{name: "literal block", manifest: "---\nrepos: |\n  a/b\n---\n",
			wantKind: ScopeScoped, wantRepos: []string{"a/b"}},
		{name: "byte order mark", manifest: "\ufeff---\nrepos: [a/b]\n---\n",
			wantKind: ScopeScoped, wantRepos: []string{"a/b"}},
		{name: "CRLF", manifest: "---\r\nrepos:\r\n  - a/b\r\n---\r\n",
			wantKind: ScopeScoped, wantRepos: []string{"a/b"}},
		{name: "past line 30", manifest: "---\n" + padding + strings.Repeat("x: y\n", 40) + "globs: [\"**/*.go\"]\n---\n",
			wantKind: ScopeScoped, wantGlobs: []string{"**/*.go"}},
		{name: "anchor and alias", manifest: "---\nshared: &shared [\"a/b\"]\nrepos: *shared\n---\n",
			wantKind: ScopeScoped, wantRepos: []string{"a/b"}},
		{name: "comma inside a brace group stays whole", manifest: "---\nglobs: src/{client,admin}/*.tsx,lib/*.go\n---\n",
			wantKind: ScopeScoped, wantGlobs: []string{"src/{client,admin}/*.tsx", "lib/*.go"}},
		{name: "list entries are never split", manifest: "---\nglobs: [\"a,b\"]\n---\n",
			wantKind: ScopeScoped, wantGlobs: []string{"a,b"}},

		// Not YAML, but written constantly. These carry the author's scope intact.
		{name: "bare glob is an alias to YAML", manifest: "---\nglobs: **/*.go,**/*.mod   # sources\n---\n",
			wantKind: ScopeScoped, wantGlobs: []string{"**/*.go", "**/*.mod"}},
		{name: "bare glob in a block item", manifest: "---\nglobs:\n  - **/*.go\n---\n",
			wantKind: ScopeScoped, wantGlobs: []string{"**/*.go"}},
		{name: "leading brace group", manifest: "---\nglobs: {app,src}/**/*.tsx\n---\n",
			wantKind: ScopeScoped, wantGlobs: []string{"{app,src}/**/*.tsx"}},
		{name: "colon in a description beside a scope", manifest: "---\ndescription: Use when: testing\nrepos: [a/b]\n---\n",
			wantKind: ScopeScoped, wantRepos: []string{"a/b"}},
		{name: "colon in a description with no scope", manifest: "---\ndescription: Use when: testing\n---\n",
			wantKind: ScopeUnscoped},
		{name: "brackets in a hint beside a scope", manifest: "---\nargument-hint: [owner/repo] [--max N]\nrepos: [a/b]\n---\n",
			wantKind: ScopeScoped, wantRepos: []string{"a/b"}},

		// A value that wraps onto a line holding a colon carries no scope problem at all.
		{name: "colon on a wrapped line", manifest: "---\ndescription: This does X\n  and also: Y\nrepos: [a/b]\n---\n",
			wantKind: ScopeScoped, wantRepos: []string{"a/b"}},
		{name: "wrapped line beside no scope", manifest: "---\ndescription: This does X\n  and also: Y\n---\n", wantKind: ScopeUnscoped},
		{name: "wrapped value above a repaired glob", manifest: "---\ndescription: Use when:\n  deploying\nglobs: **/*.go\n---\n",
			wantKind: ScopeScoped, wantGlobs: []string{"**/*.go"}},
		{name: "a block scalar survives a repair elsewhere", manifest: "---\ndescription: a: b\nnotes: |\n  keep: this\nrepos: [a/b]\n---\n",
			wantKind: ScopeScoped, wantRepos: []string{"a/b"}},

		// YAML breaks a line at a bare carriage return, so a key after one is still a key.
		{name: "bare carriage return before a scope key", manifest: "---\nA: \"0\n\rglobs: !\n---\n", wantKind: ScopeInvalid},
		{name: "unicode line break hides a scope in a broken value", manifest: "---\ndescription: a: b\u2028repos: [a/b]\n---\n",
			wantKind: ScopeScoped, wantRepos: []string{"a/b"}},
		{name: "carriage returns only", manifest: "---\rrepos: [a/b]\r---\rBody.\r", wantKind: ScopeInvalid},

		// Folded into a description, each of these would drop a limit the author wrote.
		{name: "wrapped line holds repos", manifest: "---\ndescription: x: y\n  repos: [a/b]\n---\n", wantKind: ScopeInvalid},
		{name: "wrapped line holds globs in a list item", manifest: "---\ndescription: x\n  - globs: a/*.go\n---\n", wantKind: ScopeInvalid},
		{name: "wrapped line holds status", manifest: "---\ndescription: x\n  status: draft\n---\n", wantKind: ScopeInvalid},
		{name: "wrapped line holds a foreign scope key", manifest: "---\ndescription: x\n  applyTo: a/*.go\n---\n", wantKind: ScopeInvalid},
		{name: "wrapped line holds requires", manifest: "---\ndescription: x\n  requires: [shell]\n---\n", wantKind: ScopeInvalid},

		{name: "unclosed flow list", manifest: "---\nglobs: [\"**/*.go\",\n---\n", wantKind: ScopeInvalid},
		{name: "unterminated frontmatter", manifest: "---\nrepos: [a/b]\nBody.\n", wantKind: ScopeInvalid},
		{name: "fence below a blank line hides the block", manifest: "\n---\nrepos: [a/b]\n---\n", wantKind: ScopeInvalid},
		{name: "scalar root", manifest: "---\njust some words\n---\n", wantKind: ScopeInvalid},
		{name: "list root", manifest: "---\n- repos\n---\n", wantKind: ScopeInvalid},
		{name: "mapping as a scope value", manifest: "---\nglobs:\n  include: \"**/*.go\"\n---\n", wantKind: ScopeInvalid},
		{name: "nested list", manifest: "---\nrepos: [[a/b]]\n---\n", wantKind: ScopeInvalid},
		{name: "duplicate scope key", manifest: "---\nglobs: [\"a\"]\nglobs: [\"b\"]\n---\n", wantKind: ScopeInvalid},
		{name: "merge key can hide a scope", manifest: "---\nbase: &base {repos: [a/b]}\n<<: *base\n---\n", wantKind: ScopeInvalid},
		{name: "second document can hide a scope", manifest: "---\nname: a\n--- # more\nrepos: [a/b]\n---\n", wantKind: ScopeInvalid},
		{name: "wrong case", manifest: "---\nGlobs: [\"**/*.go\"]\n---\n", wantKind: ScopeInvalid},
		{name: "stray quote in the key", manifest: "---\nglobs\": a/*.go\n---\n", wantKind: ScopeInvalid},
		{name: "text with no usable value", manifest: "---\nglobs: \",\"\n---\n", wantKind: ScopeInvalid},
		{name: "scope list status cannot be read", manifest: "---\nstatus: [draft]\n---\n", wantKind: ScopeInvalid},
		{name: "bare status is not guessed at", manifest: "---\nstatus: *draft\n---\n", wantKind: ScopeInvalid},
		{name: "paths copied from a Claude rule", manifest: "---\npaths: [\"**/*.go\"]\n---\n", wantKind: ScopeInvalid},
		{name: "applyTo copied from a Copilot rule", manifest: "---\napplyTo: \"**/*.go\"\n---\n", wantKind: ScopeInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ParseRuleScope(tt.manifest)
			require.Equal(t, tt.wantKind, got.Kind, "reason: %q", got.Reason)
			require.Equal(t, tt.wantRepos, got.Repos)
			require.Equal(t, tt.wantGlobs, got.Globs)
			if tt.wantKind == ScopeInvalid {
				require.NotEmpty(t, got.Reason, "an Invalid scope has to say why")
				require.LessOrEqual(t, len([]rune(got.Reason)), maxScopeReasonRunes+1)
			}
		})
	}
}

// TestParseManifest_WrappedDescriptions: these all read to their author as one sentence,
// so the sentence is what has to come out, whole and on one line.
func TestParseManifest_WrappedDescriptions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		block string
		want  string
	}{
		{"colon on the wrapped line", "description: This does X\n  and also: Y\n", "This does X and also: Y"},
		{"value ending in a colon", "description: Use when the user asks:\n  deploy, ship, release\n", "Use when the user asks: deploy, ship, release"},
		{"a list under the sentence", "description: Use when:\n  - deploying\n  - shipping\n", "Use when: - deploying - shipping"},
		{"text below an empty key", "description:\n  Use when the user asks:\n  deploy, ship\n", "Use when the user asks: deploy, ship"},
		{"a blank line inside", "description: Use when: a\n\n  or: b\n", "Use when: a or: b"},
		{"tab indent", "description: Use when: a\n\tor: b\n", "Use when: a or: b"},
		{"apostrophe", "description: It's for: a\n  and b\n", "It's for: a and b"},
		{"text after a quote", "description: \"Use\" this\n  when: x\n", "\"Use\" this when: x"},
		{"crlf", "description: This does X\r\n  and also: Y\r\n", "This does X and also: Y"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fm := parseManifest("---\nname: a\n" + tt.block + "name2: kept\n---\nBody.\n")
			require.Equal(t, ScopeUnscoped, fm.Scope.Kind, "reason: %q", fm.Scope.Reason)
			require.Equal(t, tt.want, fm.Description)
			require.Equal(t, "a", fm.Name, "the key above the wrapped value must survive")
		})
	}
}

// TestScope_ZeroValueIsInvalid: a Scope nobody filled in must not mean "everywhere".
func TestScope_ZeroValueIsInvalid(t *testing.T) {
	t.Parallel()
	require.Equal(t, ScopeInvalid, Scope{}.Kind)
}

// TestParseRuleFrontmatter_UnreadableFileIsInvalid: a read failure used to come back
// as empty frontmatter, which published the rule to everyone.
func TestParseRuleFrontmatter_UnreadableFileIsInvalid(t *testing.T) {
	t.Parallel()
	fm := parseRuleFrontmatter(t.TempDir() + "/missing.md")
	require.Equal(t, ScopeInvalid, fm.Scope.Kind)
}

// TestSkillPaths_IsNotAForeignScope: ox installs a skill's manifest verbatim, so the
// agent reads paths: itself. Only a rule loses it.
func TestSkillPaths_IsNotAForeignScope(t *testing.T) {
	t.Parallel()
	team := t.TempDir()
	writeRule(t, team, "agents/skills/deploy/SKILL.md", "---\nname: deploy\npaths: [\"**/*.go\"]\n---\n\nBody.\n")
	skills, err := DiscoverSkills(team, "a/b")
	require.NoError(t, err)
	require.Len(t, skills, 1)
}

// TestPublishedSkills_InvalidScopeIsRefused: a skill's repos: read as absent would
// install it in every repository, so an unreadable one is refused and reported.
func TestPublishedSkills_InvalidScopeIsRefused(t *testing.T) {
	t.Parallel()
	team := t.TempDir()
	writeRule(t, team, "agents/skills/deploy/SKILL.md", "---\nname: deploy\nrepos: [a/b\nrequires: [shell]\n---\n\nBody.\n")
	installable, rejected, err := DiscoverSkillsWithRejections(team, "a/b")
	require.NoError(t, err)
	require.Empty(t, installable)
	require.Len(t, rejected, 1)
	require.Contains(t, rejected[0].NameError, "unusable frontmatter")
	require.Equal(t, []string{"shell"}, rejected[0].Requires, "a declaration survives a broken line above it")
}

// TestRequires_SurvivesAnUnreadableScopeKey: `ox skills publish` asks about requires:
// directly, so a broken key above it must not read as "declares nothing".
func TestRequires_SurvivesAnUnreadableScopeKey(t *testing.T) {
	t.Parallel()
	for _, manifest := range []string{
		"---\nstatus: [draft]\nrequires: [shell]\n---\n",
		"---\nglobs:\n  include: a\nrequires: [shell]\n---\n",
		"---\nrepos: [a]\nrepos: [b]\nrequires: [shell]\n---\n",
	} {
		values, declared := SkillRequirements([]byte(manifest))
		require.True(t, declared, manifest)
		require.Equal(t, []string{"shell"}, values, manifest)
		require.Equal(t, ScopeInvalid, ParseRuleScope(manifest).Kind, manifest)
	}
}

// oracleEntries reads a decoded scope value the way any other YAML consumer would.
// strings holds the list's text entries, and opaque counts anything else that holds
// a value.
func oracleEntries(value any) (entries []string, opaque int) {
	switch v := value.(type) {
	case nil:
	case string:
		if strings.TrimSpace(v) != "" {
			opaque++ // one scalar; how it splits on commas is ox's reading, not YAML's
		}
	case []any:
		for _, item := range v {
			switch text := item.(type) {
			case nil:
			case string:
				if trimmed := strings.TrimSpace(text); trimmed != "" {
					entries = append(entries, trimmed)
				}
			default:
				opaque++
			}
		}
	default:
		opaque++
	}
	return entries, opaque
}

// oracleScopeKeyLine is deliberately not the production pattern: an oracle sharing
// the code it checks can only agree with it. The value excludes YAML's empty
// spellings, which limit nothing, so a read of "everywhere" is right for those.
var oracleScopeKeyLine = regexp.MustCompile(`(?i)^\s*["']?(repos|globs)["']?\s*:\s*(.*\S)\s*$`)

func blockHasScopeKeyLine(block string) bool {
	for line := range strings.SplitSeq(block, "\n") {
		match := oracleScopeKeyLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		switch value := match[2]; {
		case value[0] == '#', value == "~", strings.EqualFold(value, "null"),
			value == `""`, value == "''", value == "[]":
		default:
			return true
		}
	}
	return false
}

// oracleDocument decodes the block into plain Go values. That decoder refuses a
// repeated key anywhere, so for one that only repeats some unrelated key it reads the
// two scope keys on their own, keeping the last value as a map would.
func oracleDocument(block string, parsed *yaml.Node) map[string]any {
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(block), &doc); err == nil {
		return doc
	}
	doc = map[string]any{}
	if len(parsed.Content) == 0 || parsed.Content[0].Kind != yaml.MappingNode {
		return doc
	}
	root := parsed.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		if key := root.Content[i].Value; key == "repos" || key == "globs" {
			var value any
			if err := root.Content[i+1].Decode(&value); err == nil {
				doc[key] = value
			}
		}
	}
	return doc
}

// scopeSeeds renders one scope in every style a formatter, an author or an attacker
// can produce. They run on every plain `go test`, and seed the fuzzer.
func scopeSeeds() []string {
	const list = `["**/*.go", "src/{client,admin}/**/*.{tsx,css}"]`
	values := []string{
		list,
		"\n  - \"**/*.go\"\n  - src/{client,admin}/**/*.{tsx,css}",
		"\n- \"**/*.go\"",
		"\n  [\n    \"**/*.go\",\n    \"a/*.ts\",\n  ]",
		"[\n\"**/*.go\",\nsrc:main/**\n]",
		"**/*.go,**/*.mod",
		"\"**/*.go,**/*.mod\"",
		"'**/*.go'",
		">-\n  **/*.go,\n  **/*.mod",
		"|\n  **/*.go",
		"[\"**/*.go\" # note\n, \"b\"]",
		"!!str a/*.go",
		"&a [\"x\"]\nrepos: *a",
		"{a: b}",
		"[[a]]",
		"[\"unclosed\",",
		"~",
		"\"\"",
		",",
	}
	var padding strings.Builder
	for i := range 40 {
		fmt.Fprintf(&padding, "x%d: y\n", i)
	}
	longUniqueKeys := padding.String()
	keys := []string{"globs: ", "globs : ", "\"globs\": ", "'repos': ", "repos: ", "GLOBS: ", "paths: ", "\tglobs: "}
	wrappers := []func(string) string{
		func(fm string) string { return "---\n" + fm + "\n---\nBody.\n" },
		func(fm string) string { return "\ufeff---\n" + fm + "\n---\n" },
		func(fm string) string { return strings.ReplaceAll("---\n"+fm+"\n---\n", "\n", "\r\n") },
		func(fm string) string { return "---\nname: r\n# comment\n" + fm + "\ndescription: Below it.\n---\n" },
		func(fm string) string { return "---\n" + longUniqueKeys + fm + "\n---\n" },
		func(fm string) string { return "---\ndescription: Use when: testing\n" + fm + "\n---\n" },
		func(fm string) string { return "---\ndescription: Wraps\n  onto: a second line\n" + fm + "\n---\n" },
		func(fm string) string { return "---\ndescription: Wraps onto\n  " + fm + "\n---\n" },
		func(fm string) string { return "---\n" + fm + "\nglobs: [\"dup\"]\n---\n" },
		func(fm string) string { return "---\n" + fm + "\n" },
		func(fm string) string { return "\n---\n" + fm + "\n---\n" },
		func(fm string) string { return "---\nname: a\n--- # sep\n" + fm + "\n---\n" },
	}
	seeds := make([]string, 0, len(values)*len(keys)*len(wrappers))
	for _, value := range values {
		for _, key := range keys {
			for _, wrap := range wrappers {
				seeds = append(seeds, wrap(key+value))
			}
		}
	}
	return seeds
}

// FuzzParseScope: no input widens a rule's scope compared with what YAML says.
//
// The oracle decodes the same block into plain Go values through yaml.v3's ordinary
// path, which is what any other tool reading this frontmatter does.
func FuzzParseScope(f *testing.F) {
	for _, seed := range scopeSeeds() {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, manifest string) {
		got := ParseRuleScope(manifest)

		switch got.Kind {
		case ScopeInvalid:
			if got.Reason == "" || got.Repos != nil || got.Globs != nil {
				t.Fatalf("an Invalid scope must carry a reason and no values: %+v", got)
			}
		case ScopeUnscoped:
			if got.Repos != nil || got.Globs != nil {
				t.Fatalf("an Unscoped scope holds values: %+v", got)
			}
		case ScopeScoped:
			if len(got.Repos) == 0 && len(got.Globs) == 0 {
				t.Fatalf("a Scoped scope with nothing in it means everywhere: %+v", got)
			}
		default:
			t.Fatalf("unknown scope kind %v", got.Kind)
		}

		block, state := splitFrontmatter(manifest)
		if state != frontmatterClosed {
			return
		}

		var parsed yaml.Node
		if err := yaml.Unmarshal([]byte(block), &parsed); err != nil {
			if got.Kind == ScopeUnscoped && blockHasScopeKeyLine(block) {
				t.Fatalf("YAML rejected a block holding a scope key and it read as everywhere: %q", block)
			}
			return
		}
		doc := oracleDocument(block, &parsed)

		repos, reposOpaque := oracleEntries(doc["repos"])
		globs, globsOpaque := oracleEntries(doc["globs"])
		declared := len(repos)+reposOpaque+len(globs)+globsOpaque > 0
		if declared && got.Kind == ScopeUnscoped {
			t.Fatalf("YAML says repos=%v globs=%v and the parse read everywhere: %q", doc["repos"], doc["globs"], block)
		}
		if got.Kind != ScopeScoped {
			return
		}
		if !declared {
			t.Fatalf("Scoped with no scope in YAML: %+v from %q", got, block)
		}
		if reposOpaque == 0 && !slices.Equal(append([]string{}, got.Repos...), append([]string{}, repos...)) {
			t.Fatalf("repos %q differ from YAML's %q in %q", got.Repos, repos, block)
		}
		if globsOpaque == 0 && !slices.Equal(append([]string{}, got.Globs...), append([]string{}, globs...)) {
			t.Fatalf("globs %q differ from YAML's %q in %q", got.Globs, globs, block)
		}
	})
}

// TestScopeSeeds_CoverEveryKind keeps the corpus honest: seeds that all landed on one
// kind would exercise one branch and prove nothing about the others.
func TestScopeSeeds_CoverEveryKind(t *testing.T) {
	t.Parallel()
	counts := map[ScopeKind]int{}
	for _, seed := range scopeSeeds() {
		counts[ParseRuleScope(seed).Kind]++
	}
	for _, kind := range []ScopeKind{ScopeInvalid, ScopeUnscoped, ScopeScoped} {
		require.Positive(t, counts[kind], fmt.Sprintf("no seed reached kind %d", kind))
	}
}
