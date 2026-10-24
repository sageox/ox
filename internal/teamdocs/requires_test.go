package teamdocs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSkillRequirements_Parsing pins how `requires:` is read. The key decides
// whether a skill may be offered where there is no terminal (chat, a Claude
// organization plugin), so a parse that silently drops a declaration makes a
// shell skill look portable, and one that invents a declaration hides a
// portable skill.
//
// The rules mirror sageox-mono packages/teamskills frontmatter.go: inline list,
// bare scalar, and block list are all declarations; a bare `requires:` with no
// items is not.
func TestSkillRequirements_Parsing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		manifest     string
		wantValues   []string
		wantDeclared bool
	}{
		{name: "absent", manifest: "---\nname: a\n---\nbody\n"},
		{name: "inline list", manifest: "---\nrequires: [shell]\n---\n",
			wantValues: []string{"shell"}, wantDeclared: true},
		{name: "mixed quotes", manifest: "---\nrequires: [\"shell\", 'ox-cli']\n---\n",
			wantValues: []string{"shell", "ox-cli"}, wantDeclared: true},
		{
			// The author's explicit "runs anywhere": declared, with nothing in it.
			// Reading it as undeclared would re-run the body inference the author
			// just opted out of.
			name: "explicit empty list", manifest: "---\nrequires: []\n---\n",
			wantDeclared: true,
		},
		{name: "bare scalar", manifest: "---\nrequires: ox-cli\n---\n",
			wantValues: []string{"ox-cli"}, wantDeclared: true},
		{name: "trailing comment", manifest: "---\nrequires: [shell] # needs bash\n---\n",
			wantValues: []string{"shell"}, wantDeclared: true},
		{name: "hash inside quotes is literal", manifest: "---\nrequires: [\"sh#ell\"]\n---\n",
			wantValues: []string{"sh#ell"}, wantDeclared: true},
		{
			// A value that is only a comment is an empty value, which opens a
			// block list. With no items after it, nothing was declared.
			name: "comment-only value", manifest: "---\nrequires: # todo\nname: a\n---\n",
		},
		{name: "bare key then another key", manifest: "---\nrequires:\nname: a\n---\n"},
		{name: "block list", manifest: "---\nrequires:\n  - shell\n  - \"ox-cli\" # the CLI\n---\n",
			wantValues: []string{"shell", "ox-cli"}, wantDeclared: true},
		{name: "tab-indented block item", manifest: "---\nrequires:\n\t- shell\n---\n",
			wantValues: []string{"shell"}, wantDeclared: true},
		{name: "empty block item declares nothing", manifest: "---\nrequires:\n  -\n---\n"},
		{
			// R3. A zero-indent sequence is what PyYAML and most formatters emit,
			// and it is a declaration. Reading it as "undeclared" was the fail-OPEN
			// side: the author said shell, and a body with no shell fence then
			// classified as portable.
			name: "zero-indent block list", manifest: "---\nrequires:\n- shell\n- \"ox-cli\" # cli\n---\n",
			wantValues: []string{"shell", "ox-cli"}, wantDeclared: true,
		},
		{name: "zero-indent empty item keeps the list open", manifest: "---\nrequires:\n-\n- shell\n---\n",
			wantValues: []string{"shell"}, wantDeclared: true},
		{name: "zero-indent dash glued to a word is not an item", manifest: "---\nrequires:\n-shell\n---\n"},
		{name: "zero-indent list ends at the next key", manifest: "---\nrequires:\n- shell\nname: a\n- stray\n---\n",
			wantValues: []string{"shell"}, wantDeclared: true},
		{name: "fence with trailing space closes the list", manifest: "---\nrequires:\n- shell\n--- \n- body\n",
			wantValues: []string{"shell"}, wantDeclared: true},
		{name: "opening fence with trailing whitespace", manifest: "---\t \nrequires: [shell]\n---\n",
			wantValues: []string{"shell"}, wantDeclared: true},
		{name: "leading whitespace before the fence is not frontmatter", manifest: " ---\nrequires: [shell]\n---\n"},
		{name: "CRLF", manifest: "---\r\nrequires: [shell]\r\n---\r\n",
			wantValues: []string{"shell"}, wantDeclared: true},
		{name: "no frontmatter", manifest: "requires: [shell]\n"},
		{name: "in the body, after the frontmatter", manifest: "---\nname: a\n---\nrequires: [shell]\n"},
		{name: "unknown value is still a declaration", manifest: "---\nrequires: [docker]\n---\n",
			wantValues: []string{"docker"}, wantDeclared: true},
		{
			// R4. requires: is the one key read past the 30-line window: a long
			// folded description must not push a declaration out of view.
			name:         "beyond the 30-line window",
			manifest:     "---\n" + strings.Repeat("x: y\n", 31) + "requires: [shell]\n---\n",
			wantValues:   []string{"shell"},
			wantDeclared: true,
		},
		{
			name: "after a long folded description",
			manifest: "---\nname: a\ndescription: >-\n" + strings.Repeat("  a long line of description\n", 40) +
				"requires:\n- ox-cli\n---\n",
			wantValues:   []string{"ox-cli"},
			wantDeclared: true,
		},
		{
			name:         "block list crossing the window is read whole",
			manifest:     "---\n" + strings.Repeat("x: y\n", 28) + "requires:\n  - shell\n  - ox-cli\n  - more\n---\n",
			wantValues:   []string{"shell", "ox-cli", "more"},
			wantDeclared: true,
		},
		{name: "unterminated frontmatter is read to the end for requires", manifest: "---\n" + strings.Repeat("x: y\n", 40) + "requires: [shell]\n",
			wantValues: []string{"shell"}, wantDeclared: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			values, declared := SkillRequirements([]byte(tt.manifest))
			require.Equal(t, tt.wantDeclared, declared)
			require.Equal(t, tt.wantValues, values)
		})
	}
}

// TestSkillRequirements_BlockListDoesNotSwallowFollowingKeys: the block reader
// must hand control back on the first non-item line, or a key after the list
// (name:, repos:) is lost — and a lost repos: means "every repository".
func TestSkillRequirements_BlockListDoesNotSwallowFollowingKeys(t *testing.T) {
	t.Parallel()
	team := t.TempDir()
	dir := filepath.Join(team, "agents", "skills", "deploy")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(
		"---\nrequires:\n  - shell\nname: deploy\nrepos: [\"acme/api\"]\ndescription: Ship it.\n---\nbody\n"), 0o644))

	got, err := PublishedSkills(team)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "deploy", got[0].Name)
	require.Equal(t, []string{"acme/api"}, got[0].Repos, "a key after the block list was swallowed")
	require.Equal(t, "Ship it.", got[0].Description)
	require.Equal(t, []string{"shell"}, got[0].Requires)
	require.True(t, got[0].RequiresDeclared)
}

// TestDiscoverSkills_CarriesRequiresIntoTheSkill: discovery on disk and the
// in-memory reader publish uses must agree, or `ox skills publish` warns about
// a skill the planner treats differently.
func TestDiscoverSkills_CarriesRequiresIntoTheSkill(t *testing.T) {
	t.Parallel()
	team := t.TempDir()
	for name, front := range map[string]string{
		"declared":   "requires: [ox-cli]\n",
		"open":       "requires: []\n",
		"undeclared": "",
	} {
		dir := filepath.Join(team, "agents", "skills", name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		manifest := "---\nname: " + name + "\n" + front + "---\nbody\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(manifest), 0o644))

		values, declared := SkillRequirements([]byte(manifest))
		got, err := DiscoverSkills(team, "")
		require.NoError(t, err)
		var found *TeamSkill
		for i := range got {
			if got[i].Name == name {
				found = &got[i]
			}
		}
		require.NotNil(t, found, "%s was not discovered", name)
		require.Equal(t, declared, found.RequiresDeclared, name)
		require.Equal(t, values, found.Requires, name)
	}
}

// TestRequiresDoesNotLeakIntoRules: rules share the frontmatter parser, and a
// requires: key must not change which rules publish or how they read.
func TestRequiresDoesNotLeakIntoRules(t *testing.T) {
	t.Parallel()
	team := t.TempDir()
	dir := filepath.Join(team, "agents", "rules")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "style.md"), []byte(
		"---\nname: style\nrequires:\n  - shell\nvisibility: always\n---\nUse tabs.\n"), 0o644))

	rules, err := DiscoverRules(team, "")
	require.NoError(t, err)
	require.Len(t, rules, 1)
	require.Equal(t, VisibilityAlways, rules[0].Visibility, "the key after a requires: block was lost")
	require.Equal(t, "Use tabs.\n", rules[0].Body)
}

// TestManifestBody_MatchesReadRuleBody: the in-memory body and the on-disk body
// are one function, so a publish-time scan and a planner-time scan read the
// same bytes.
func TestManifestBody_MatchesReadRuleBody(t *testing.T) {
	t.Parallel()
	for _, content := range []string{
		"---\nname: a\n---\nbody\n",
		"---\r\nname: a\r\n---\r\nbody\r\n",
		"no frontmatter\n",
		"---\nunterminated: yes\n```bash\nls\n```\n",
		"---\nname: a\n---something\n---\nbody\n",
	} {
		path := filepath.Join(t.TempDir(), "SKILL.md")
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
		fromDisk, err := ReadRuleBody(path)
		require.NoError(t, err)
		require.Equal(t, fromDisk, ManifestBody(content), "%q", content)
	}
	require.Contains(t, ManifestBody("---\nunterminated: yes\n```bash\nls\n```\n"), "```bash",
		"an unterminated frontmatter hid the body from the scan")
}

// TestFrontmatter_ByteOrderMarkIsReadThrough: a SKILL.md or rule saved with a
// leading UTF-8 byte-order mark is read WITH its frontmatter by every editor
// and coding agent. Reading it as "no frontmatter" drops the keys that narrow
// where content lands — repos:, audience:, status:, requires: — so a draft
// publishes and a repo-scoped skill reaches every repository.
func TestFrontmatter_ByteOrderMarkIsReadThrough(t *testing.T) {
	t.Parallel()
	const bom = "\ufeff"
	team := t.TempDir()
	write := func(name, manifest string) {
		dir := filepath.Join(team, "agents", "skills", name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(manifest), 0o644))
	}
	write("scoped", bom+"---\nname: scoped\ndescription: \"Only for api.\"\nrepos: [\"acme/api\"]\nrequires: [shell]\n---\nbody\n")
	write("crlf", bom+"---\r\nname: crlf\r\ndescription: Windows line endings.\r\n---\r\nbody\r\n")
	write("draft", bom+"---\nname: draft\nstatus: draft\n---\nbody\n")
	write("people", bom+"---\nname: people\naudience: human\n---\nbody\n")

	published, err := PublishedSkills(team)
	require.NoError(t, err)
	byName := map[string]TeamSkill{}
	for _, s := range published {
		byName[s.Name] = s
	}
	require.NotContains(t, byName, "draft", "a BOM hid status: draft and the draft was published")
	require.NotContains(t, byName, "people", "a BOM hid audience: human and it was published to AI coworkers")

	scoped := byName["scoped"]
	require.Equal(t, "Only for api.", scoped.Description)
	require.Equal(t, []string{"acme/api"}, scoped.Repos, "a BOM widened a repo-scoped skill to every repository")
	require.Equal(t, []string{"shell"}, scoped.Requires)
	require.True(t, scoped.RequiresDeclared)
	require.Equal(t, "Windows line endings.", byName["crlf"].Description)

	applies, err := DiscoverSkills(team, "acme/web")
	require.NoError(t, err)
	for _, s := range applies {
		require.NotEqual(t, "scoped", s.Name, "repos: was ignored behind a BOM")
	}

	values, declared := SkillRequirements([]byte(bom + "---\nrequires:\n  - ox-cli\n---\n"))
	require.True(t, declared)
	require.Equal(t, []string{"ox-cli"}, values)

	require.Equal(t, "body\n", ManifestBody(bom+"---\nname: x\n---\nbody\n"),
		"the frontmatter of a BOM-prefixed manifest leaked into its body")

	// Exactly one BOM, as the consumers strip: after it, a second is text.
	values, declared = SkillRequirements([]byte(bom + bom + "---\nrequires: [shell]\n---\n"))
	require.False(t, declared)
	require.Nil(t, values)
}

// TestDocFrontmatter_ByteOrderMarkIsReadThrough: team docs share the rule.
func TestDocFrontmatter_ByteOrderMarkIsReadThrough(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "guide.md")
	require.NoError(t, os.WriteFile(path, []byte("\ufeff---\ntitle: Deploy guide\nvisibility: hidden\n---\n# Heading\n\nFirst paragraph.\n"), 0o644))
	fm := parseFrontmatter(path)
	require.Equal(t, "Deploy guide", fm.Title)
	require.Equal(t, "hidden", fm.Visibility, "a BOM hid visibility: hidden")
	require.Equal(t, "Heading", extractTitleFromContent(path), "the frontmatter was scanned as body")
	require.Equal(t, "First paragraph.", extractDescriptionFromContent(path))
}

// TestRuleFrontmatter_OtherKeysKeepTheWindow: R4 widens ONLY requires:. A
// repos: or status: past line 30 keeps its old meaning (not read), so no
// published skill or rule changes scope because of this contract.
func TestRuleFrontmatter_OtherKeysKeepTheWindow(t *testing.T) {
	t.Parallel()
	manifest := "---\nname: a\n" + strings.Repeat("x: y\n", 35) + "repos: [\"acme/api\"]\nstatus: draft\nrequires: [shell]\n---\n"
	fm := parseRuleFrontmatterFrom(strings.NewReader(manifest))
	require.Equal(t, "a", fm.Name)
	require.Nil(t, fm.Repos, "a key past the window was read")
	require.Empty(t, fm.Status, "a key past the window was read")
	require.Equal(t, []string{"shell"}, fm.Requires)
}

// TestFrontmatter_R2_FencesWithTrailingWhitespace: every metadata reader uses
// the same fence rule as the trust check, so they agree on where frontmatter
// is — and a fence written "--- " no longer drops repos:, status: or the body.
func TestFrontmatter_R2_FencesWithTrailingWhitespace(t *testing.T) {
	t.Parallel()
	team := t.TempDir()
	write := func(name, manifest string) {
		dir := filepath.Join(team, "agents", "skills", name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(manifest), 0o644))
	}
	write("scoped", "--- \nname: scoped\nrepos: [\"acme/api\"]\n---\t\nbody\n")
	write("draft", "---  \nname: draft\nstatus: draft\n---\nbody\n")

	published, err := PublishedSkills(team)
	require.NoError(t, err)
	require.Len(t, published, 1, "a draft behind a '--- ' fence was published")
	require.Equal(t, "scoped", published[0].Name)
	require.Equal(t, []string{"acme/api"}, published[0].Repos)

	require.Equal(t, "body\n", ManifestBody("--- \nname: x\n---  \nbody\n"))
	require.Equal(t, "after\n", ManifestBody("---\nname: x\n---foo\n---\nafter\n"),
		"a '---foo' line closed the frontmatter")
	require.Equal(t, " ---\nname: x\n---\nbody\n", ManifestBody(" ---\nname: x\n---\nbody\n"),
		"leading whitespace was accepted as a fence")

	doc := filepath.Join(t.TempDir(), "guide.md")
	require.NoError(t, os.WriteFile(doc, []byte("--- \ntitle: Guide\n--- \n# Heading\n\nFirst.\n"), 0o644))
	require.Equal(t, "Guide", parseFrontmatter(doc).Title)
	require.Equal(t, "Heading", extractTitleFromContent(doc))
	require.Equal(t, "First.", extractDescriptionFromContent(doc))
}
