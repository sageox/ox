package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRedactFile_WellFormed(t *testing.T) {
	content := `# Team Redaction Rules

Some documentation here.

` + "```redact" + `
# Internal API keys
regex "ACME-[a-f0-9]{32}" -> [REDACTED_ACME_KEY]

# Internal hostname
literal "api.internal.acme.com" -> [REDACTED_INTERNAL_HOST]
` + "```" + `

## More docs

` + "```redact" + `
literal "Project Falcon" -> [REDACTED_CODENAME]
` + "```" + `
`
	path := filepath.Join(t.TempDir(), "REDACT.md")
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))

	parsed, err := ParseRedactFile(path, RuleSourceTeam)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	assert.Equal(t, path, parsed.Path)
	assert.Equal(t, RuleSourceTeam, parsed.Source)
	assert.Len(t, parsed.Rules, 3)
	assert.Empty(t, parsed.Errors)

	// first rule: regex
	assert.Equal(t, "regex", parsed.Rules[0].Type)
	assert.Equal(t, "ACME-[a-f0-9]{32}", parsed.Rules[0].RawPattern)
	assert.Equal(t, "[REDACTED_ACME_KEY]", parsed.Rules[0].Replacement)

	// second rule: literal
	assert.Equal(t, "literal", parsed.Rules[1].Type)
	assert.Equal(t, "api.internal.acme.com", parsed.Rules[1].RawPattern)
	assert.Equal(t, "[REDACTED_INTERNAL_HOST]", parsed.Rules[1].Replacement)

	// third rule: literal from second block
	assert.Equal(t, "literal", parsed.Rules[2].Type)
	assert.Equal(t, "Project Falcon", parsed.Rules[2].RawPattern)
	assert.Equal(t, "[REDACTED_CODENAME]", parsed.Rules[2].Replacement)
}

func TestParseRedactFile_NonExistent(t *testing.T) {
	parsed, err := ParseRedactFile("/nonexistent/REDACT.md", RuleSourceUser)
	assert.NoError(t, err)
	assert.Nil(t, parsed)
}

func TestParseRedactFile_EmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "REDACT.md")
	require.NoError(t, os.WriteFile(path, []byte(""), 0644))

	parsed, err := ParseRedactFile(path, RuleSourceRepo)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	assert.Empty(t, parsed.Rules)
	assert.Empty(t, parsed.Errors)
}

func TestParseRedactFile_CommentsOnly(t *testing.T) {
	content := "```redact\n# just a comment\n# another comment\n```\n"
	path := filepath.Join(t.TempDir(), "REDACT.md")
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))

	parsed, err := ParseRedactFile(path, RuleSourceRepo)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	assert.Empty(t, parsed.Rules)
	assert.Empty(t, parsed.Errors)
}

func TestParseRedactFile_IgnoresNonRedactBlocks(t *testing.T) {
	content := "```bash\necho hello\n```\n\n```redact\nliteral \"secret\" -> [REDACTED]\n```\n\n```python\nprint('hi')\n```\n"
	path := filepath.Join(t.TempDir(), "REDACT.md")
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))

	parsed, err := ParseRedactFile(path, RuleSourceRepo)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	assert.Len(t, parsed.Rules, 1)
	assert.Equal(t, "secret", parsed.Rules[0].RawPattern)
}

func TestParseRedactFile_MalformedLines(t *testing.T) {
	content := "```redact\nno arrow here\nliteral missing replacement ->\nliteral \"good\" -> [REDACTED_GOOD]\nunknown_type \"foo\" -> [BAR]\n```\n"
	path := filepath.Join(t.TempDir(), "REDACT.md")
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))

	parsed, err := ParseRedactFile(path, RuleSourceRepo)
	require.NoError(t, err)
	require.NotNil(t, parsed)

	// only the good rule should parse
	assert.Len(t, parsed.Rules, 1)
	assert.Equal(t, "good", parsed.Rules[0].RawPattern)

	// errors for the bad lines
	assert.Len(t, parsed.Errors, 3)
	assert.Contains(t, parsed.Errors[0].Message, "missing '->' separator")
	assert.Contains(t, parsed.Errors[1].Message, "missing replacement token")
	assert.Contains(t, parsed.Errors[2].Message, "must start with 'literal' or 'regex'")
}

func TestParseRedactFile_EmptyPattern(t *testing.T) {
	content := "```redact\nliteral \"\" -> [REDACTED]\n```\n"
	path := filepath.Join(t.TempDir(), "REDACT.md")
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))

	parsed, err := ParseRedactFile(path, RuleSourceRepo)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	assert.Empty(t, parsed.Rules)
	assert.Len(t, parsed.Errors, 1)
	assert.Contains(t, parsed.Errors[0].Message, "empty pattern")
}

func TestRulesToPatterns_Literal(t *testing.T) {
	rules := []RedactRule{
		{
			Type:        "literal",
			RawPattern:  "api.internal.acme.com",
			Replacement: "[REDACTED_HOST]",
			Source:      RuleSourceTeam,
			SourcePath:  "/path/to/REDACT.md",
			LineNumber:  5,
		},
	}

	patterns, errors := RulesToPatterns(rules)
	assert.Empty(t, errors)
	require.Len(t, patterns, 1)

	// literal should match exactly, not as regex
	assert.True(t, patterns[0].Pattern.MatchString("api.internal.acme.com"))
	// the dot should be escaped, not match any char
	assert.False(t, patterns[0].Pattern.MatchString("apiXinternalXacmeXcom"))
	assert.Equal(t, "[REDACTED_HOST]", patterns[0].Redact)
	assert.Equal(t, RuleSourceTeam, patterns[0].Source)
}

func TestRulesToPatterns_Regex(t *testing.T) {
	rules := []RedactRule{
		{
			Type:        "regex",
			RawPattern:  "ACME-[a-f0-9]{32}",
			Replacement: "[REDACTED_ACME_KEY]",
			Source:      RuleSourceRepo,
			SourcePath:  "/path/to/REDACT.md",
			LineNumber:  3,
		},
	}

	patterns, errors := RulesToPatterns(rules)
	assert.Empty(t, errors)
	require.Len(t, patterns, 1)

	assert.True(t, patterns[0].Pattern.MatchString("ACME-00112233445566778899aabbccddeeff"))
	assert.False(t, patterns[0].Pattern.MatchString("ACME-short"))
}

func TestRulesToPatterns_BadRegex(t *testing.T) {
	rules := []RedactRule{
		{
			Type:        "regex",
			RawPattern:  "[invalid",
			Replacement: "[REDACTED]",
			Source:      RuleSourceUser,
			SourcePath:  "/path/to/REDACT.md",
			LineNumber:  2,
		},
		{
			Type:        "literal",
			RawPattern:  "good pattern",
			Replacement: "[REDACTED_GOOD]",
			Source:      RuleSourceUser,
			SourcePath:  "/path/to/REDACT.md",
			LineNumber:  3,
		},
	}

	patterns, errors := RulesToPatterns(rules)
	assert.Len(t, errors, 1)
	assert.Contains(t, errors[0].Message, "invalid regex")
	// good rule should still compile
	require.Len(t, patterns, 1)
	assert.Equal(t, "[REDACTED_GOOD]", patterns[0].Redact)
}

func TestRulesToPatterns_CaseInsensitiveRegex(t *testing.T) {
	rules := []RedactRule{
		{
			Type:        "regex",
			RawPattern:  "(?i)project\\s+falcon",
			Replacement: "[REDACTED_CODENAME]",
			Source:      RuleSourceTeam,
			SourcePath:  "/path/to/REDACT.md",
			LineNumber:  1,
		},
	}

	patterns, errors := RulesToPatterns(rules)
	assert.Empty(t, errors)
	require.Len(t, patterns, 1)
	assert.True(t, patterns[0].Pattern.MatchString("Project Falcon"))
	assert.True(t, patterns[0].Pattern.MatchString("PROJECT FALCON"))
	assert.True(t, patterns[0].Pattern.MatchString("project falcon"))
}

func TestNewRedactorWithCustomRules_NoCustomFiles(t *testing.T) {
	// use a temp dir with no REDACT.md files
	tmpDir := t.TempDir()

	redactor, errors := NewRedactorWithCustomRules(tmpDir)
	assert.Empty(t, errors)
	require.NotNil(t, redactor)

	// should still have all built-in patterns
	assert.Equal(t, len(DefaultPatterns()), len(redactor.patterns))
}

func TestNewRedactorWithCustomRules_WithRepoRedact(t *testing.T) {
	tmpDir := t.TempDir()

	// create .sageox/REDACT.md
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	content := "```redact\nliteral \"my-secret-host\" -> [REDACTED_HOST]\n```\n"
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "REDACT.md"), []byte(content), 0644))

	redactor, errors := NewRedactorWithCustomRules(tmpDir)
	assert.Empty(t, errors)
	require.NotNil(t, redactor)

	// should have built-in + 1 custom
	assert.Equal(t, len(DefaultPatterns())+1, len(redactor.patterns))

	// custom rule should work
	output, found := redactor.RedactString("connecting to my-secret-host:5432")
	assert.NotEmpty(t, found)
	assert.Contains(t, output, "[REDACTED_HOST]")
	assert.NotContains(t, output, "my-secret-host")
}

func TestNewRedactorWithCustomRules_BuiltinPatternsStillWork(t *testing.T) {
	tmpDir := t.TempDir()

	// create .sageox/REDACT.md with a custom rule
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	content := "```redact\nliteral \"custom\" -> [REDACTED_CUSTOM]\n```\n"
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "REDACT.md"), []byte(content), 0644))

	redactor, _ := NewRedactorWithCustomRules(tmpDir)

	// built-in AWS pattern should still work
	output, found := redactor.RedactString("key: AKIAIOSFODNN7EXAMPLE")
	assert.NotEmpty(t, found)
	assert.Contains(t, output, "[REDACTED_AWS_KEY]")

	// custom pattern should also work
	output2, found2 := redactor.RedactString("found custom in text")
	assert.NotEmpty(t, found2)
	assert.Contains(t, output2, "[REDACTED_CUSTOM]")
}

func TestStripQuotes(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{`"hello"`, "hello"},
		{`"hello world"`, "hello world"},
		{`hello`, "hello"},
		{`""`, ""},
		{`"`, `"`},
		{`"unclosed`, `"unclosed`},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.expected, stripQuotes(tt.input))
	}
}

func TestParseRedactFile_UnquotedPattern(t *testing.T) {
	// patterns without quotes should still work
	content := "```redact\nliteral my-secret -> [REDACTED]\n```\n"
	path := filepath.Join(t.TempDir(), "REDACT.md")
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))

	parsed, err := ParseRedactFile(path, RuleSourceRepo)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	assert.Len(t, parsed.Rules, 1)
	assert.Equal(t, "my-secret", parsed.Rules[0].RawPattern)
}

func TestDiscoverRedactSources_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	sources := DiscoverRedactSources(tmpDir)

	// should always return 3 entries (team, repo, user)
	assert.Len(t, sources, 3)
	assert.Equal(t, RuleSourceTeam, sources[0].Source)
	assert.Equal(t, RuleSourceRepo, sources[1].Source)
	assert.Equal(t, RuleSourceUser, sources[2].Source)

	// no rules in any source
	for _, src := range sources {
		assert.Empty(t, src.Rules)
	}
}

// TestCustomRules_RealisticSessionContent verifies custom rules work against
// the kind of content that actually appears in Claude Code sessions:
// tool outputs, bash commands, file reads, assistant responses.
func TestCustomRules_RealisticSessionContent(t *testing.T) {
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	// realistic REDACT.md a team might write
	redactContent := `# Redaction Rules

Our internal infrastructure details should not leak into session recordings.

` + "```redact" + `
# internal service hostnames
literal "payments.internal.acmecorp.net" -> [REDACTED_INTERNAL_HOST]
literal "auth-service.k8s.acmecorp.net" -> [REDACTED_INTERNAL_HOST]

# project codename (appears in conversations, code comments, branch names)
regex "(?i)project[\s\-_]+nightingale" -> [REDACTED_CODENAME]

# internal API key format: ACME-<env>-<32hex>
regex "ACME-(prod|staging|dev)-[a-f0-9]{32}" -> [REDACTED_ACME_KEY]

# employee IDs in log output
regex "emp_[0-9]{6}" -> [REDACTED_EMPLOYEE_ID]
` + "```" + `
`
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "REDACT.md"), []byte(redactContent), 0644))

	redactor, errs := NewRedactorWithCustomRules(tmpDir)
	require.Empty(t, errs)

	tests := []struct {
		name     string
		input    string
		contains []string    // must appear in output
		absent   []string    // must NOT appear in output
	}{
		{
			name: "bash tool output with internal hostname",
			input: `$ curl https://payments.internal.acmecorp.net/api/v1/health
{"status":"ok","version":"2.3.1"}`,
			contains: []string{"[REDACTED_INTERNAL_HOST]", `{"status":"ok","version":"2.3.1"}`},
			absent:   []string{"payments.internal.acmecorp.net"},
		},
		{
			name: "assistant mentions codename",
			input: `I see this is part of Project Nightingale. The migration script at db/migrate/003_nightingale.sql needs updating.`,
			contains: []string{"[REDACTED_CODENAME]", "migration script"},
			absent:   []string{"Project Nightingale"},
		},
		{
			name: "config file read with internal API key",
			input: `{
  "payment_gateway": "https://payments.internal.acmecorp.net",
  "api_key": "ACME-prod-deadbeef0123456789abcdef01234567",
  "timeout_ms": 5000
}`,
			contains: []string{"[REDACTED_INTERNAL_HOST]", "[REDACTED_ACME_KEY]", "timeout_ms"},
			absent:   []string{"payments.internal.acmecorp.net", "deadbeef0123456789abcdef01234567"},
		},
		{
			name: "log output with employee ID",
			input: `2026-02-26 10:30:15 INFO user emp_123456 accessed resource /admin/settings
2026-02-26 10:30:16 INFO user emp_789012 updated config`,
			contains: []string{"[REDACTED_EMPLOYEE_ID]", "accessed resource"},
			absent:   []string{"emp_123456", "emp_789012"},
		},
		{
			name:     "codename case insensitive in branch name",
			input:    "git checkout feature/project-nightingale-auth",
			contains: []string{"[REDACTED_CODENAME]"},
			absent:   []string{"nightingale"},
		},
		{
			name: "mixed builtin and custom secrets",
			input: `token=ghp_1234567890abcdefghijklmnopqrstuvwxyz12
curl -H "Authorization: Bearer $TOKEN" https://auth-service.k8s.acmecorp.net/verify`,
			contains: []string{"[REDACTED_GITHUB_TOKEN]", "[REDACTED_INTERNAL_HOST]"},
			absent:   []string{"ghp_1234567890abcdefghijklmnopqrstuvwxyz12", "auth-service.k8s.acmecorp.net"},
		},
		{
			name:     "no false positive on similar but non-matching text",
			input:    "The ACME company provides payment services at payments.example.com",
			contains: []string{"ACME company", "payments.example.com"},
			absent:   []string{"[REDACTED"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, _ := redactor.RedactString(tt.input)
			for _, s := range tt.contains {
				assert.Contains(t, output, s)
			}
			for _, s := range tt.absent {
				assert.NotContains(t, output, s)
			}
		})
	}
}

// TestCustomRules_RedactMapAndSlice verifies custom rules work through
// RedactMap, which is the path used for raw JSON session entries.
func TestCustomRules_RedactMapAndSlice(t *testing.T) {
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	content := "```redact\nliteral \"internal.acme.io\" -> [REDACTED_HOST]\n```\n"
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "REDACT.md"), []byte(content), 0644))

	redactor, _ := NewRedactorWithCustomRules(tmpDir)

	// simulate a raw session entry as parsed JSON
	data := map[string]any{
		"type":    "tool",
		"content": "Connected to internal.acme.io:5432",
		"tool_input": map[string]any{
			"command": "psql -h internal.acme.io -U admin",
		},
		"tool_output": "key: AKIAIOSFODNN7EXAMPLE",
		"metadata": []any{
			"host=internal.acme.io",
			map[string]any{"url": "https://internal.acme.io/api"},
		},
	}

	redacted := redactor.RedactMap(data)
	assert.True(t, redacted)

	// custom rule applied to string values at all nesting levels
	assert.Contains(t, data["content"], "[REDACTED_HOST]")
	assert.NotContains(t, data["content"], "internal.acme.io")

	toolInput := data["tool_input"].(map[string]any)
	assert.Contains(t, toolInput["command"], "[REDACTED_HOST]")

	// built-in pattern also applied
	assert.Contains(t, data["tool_output"], "[REDACTED_AWS_KEY]")

	// nested slice and map
	metadata := data["metadata"].([]any)
	assert.Contains(t, metadata[0], "[REDACTED_HOST]")
	nestedMap := metadata[1].(map[string]any)
	assert.Contains(t, nestedMap["url"], "[REDACTED_HOST]")
}

// TestCustomRules_RedactEntries verifies custom rules work through
// the full RedactEntries path (Content + ToolInput + ToolOutput).
func TestCustomRules_RedactEntries(t *testing.T) {
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	content := "```redact\nliteral \"Project Phoenix\" -> [REDACTED_PROJECT]\nregex \"EMP-[0-9]{4}\" -> [REDACTED_EMP]\n```\n"
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "REDACT.md"), []byte(content), 0644))

	redactor, _ := NewRedactorWithCustomRules(tmpDir)

	entries := []Entry{
		{
			Type:    EntryTypeUser,
			Content: "Working on Project Phoenix, assigned to EMP-4521",
		},
		{
			Type:      EntryTypeTool,
			Content:   "bash output",
			ToolName:  "bash",
			ToolInput: "grep -r 'Project Phoenix' src/",
			ToolOutput: `src/config.yaml:  project: Project Phoenix
src/main.go:  // EMP-4521 authored this`,
		},
		{
			Type:    EntryTypeAssistant,
			Content: "No secrets here, just normal code review.",
		},
	}

	count := redactor.RedactEntries(entries)
	assert.Equal(t, 2, count) // entries 0 and 1 had secrets

	// entry 0: content redacted
	assert.Contains(t, entries[0].Content, "[REDACTED_PROJECT]")
	assert.Contains(t, entries[0].Content, "[REDACTED_EMP]")
	assert.NotContains(t, entries[0].Content, "Project Phoenix")

	// entry 1: tool input and output redacted
	assert.Contains(t, entries[1].ToolInput, "[REDACTED_PROJECT]")
	assert.Contains(t, entries[1].ToolOutput, "[REDACTED_PROJECT]")
	assert.Contains(t, entries[1].ToolOutput, "[REDACTED_EMP]")

	// entry 2: untouched
	assert.Equal(t, "No secrets here, just normal code review.", entries[2].Content)
}

// TestCustomRules_MultiSourceMerge verifies that rules from multiple
// REDACT.md files (repo + user) all apply to the same input.
func TestCustomRules_MultiSourceMerge(t *testing.T) {
	tmpDir := t.TempDir()

	// repo-level REDACT.md
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	repoRedact := "```redact\nliteral \"repo-secret-host\" -> [REDACTED_REPO_HOST]\n```\n"
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "REDACT.md"), []byte(repoRedact), 0644))

	// user-level REDACT.md (use a temp path since we can't write to real user config dir in tests)
	// instead, test via LoadCustomPatterns which already handles file discovery
	// for a true multi-source test, parse both files and merge manually
	userPath := filepath.Join(t.TempDir(), "user-REDACT.md")
	userRedact := "```redact\nliteral \"my-embarrassing-var-name\" -> [REDACTED_PERSONAL]\n```\n"
	require.NoError(t, os.WriteFile(userPath, []byte(userRedact), 0644))

	// parse both
	repoParsed, _ := ParseRedactFile(filepath.Join(sageoxDir, "REDACT.md"), RuleSourceRepo)
	userParsed, _ := ParseRedactFile(userPath, RuleSourceUser)

	var allRules []RedactRule
	allRules = append(allRules, repoParsed.Rules...)
	allRules = append(allRules, userParsed.Rules...)

	customPatterns, errs := RulesToPatterns(allRules)
	assert.Empty(t, errs)

	// build redactor: builtin + both custom sources
	patterns := DefaultPatterns()
	patterns = append(patterns, customPatterns...)
	redactor := NewRedactorWithPatterns(patterns)

	// input that contains secrets from all three sources
	input := "connecting to repo-secret-host with AKIAIOSFODNN7EXAMPLE, found my-embarrassing-var-name in diff"
	output, found := redactor.RedactString(input)

	assert.Contains(t, output, "[REDACTED_REPO_HOST]")
	assert.Contains(t, output, "[REDACTED_AWS_KEY]")
	assert.Contains(t, output, "[REDACTED_PERSONAL]")
	assert.NotContains(t, output, "repo-secret-host")
	assert.NotContains(t, output, "AKIAIOSFODNN7EXAMPLE")
	assert.NotContains(t, output, "my-embarrassing-var-name")
	assert.GreaterOrEqual(t, len(found), 3)
}

// TestCustomRules_OverlappingRules verifies that when multiple rules match
// the same text, the first rule in pattern order wins (builtin before custom,
// earlier custom sources before later ones).
func TestCustomRules_OverlappingRules(t *testing.T) {
	t.Run("builtin wins over custom for same text", func(t *testing.T) {
		tmpDir := t.TempDir()
		sageoxDir := filepath.Join(tmpDir, ".sageox")
		require.NoError(t, os.MkdirAll(sageoxDir, 0755))

		// custom rule that also targets GitHub tokens with a different replacement
		content := "```redact\nregex \"ghp_[A-Za-z0-9_]{36,}\" -> [CUSTOM_GH_TOKEN]\n```\n"
		require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "REDACT.md"), []byte(content), 0644))

		redactor, errs := NewRedactorWithCustomRules(tmpDir)
		require.Empty(t, errs)

		input := "token=ghp_1234567890abcdefghijklmnopqrstuvwxyz12"
		output, _ := redactor.RedactString(input)

		// builtin github_token pattern runs first and wins
		assert.Contains(t, output, "[REDACTED_GITHUB_TOKEN]")
		assert.NotContains(t, output, "[CUSTOM_GH_TOKEN]")
		assert.NotContains(t, output, "ghp_")
	})

	t.Run("repo rule wins over user rule for same literal", func(t *testing.T) {
		// parse two files manually, merge repo before user (matching real merge order)
		repoPath := filepath.Join(t.TempDir(), "repo-REDACT.md")
		userPath := filepath.Join(t.TempDir(), "user-REDACT.md")

		require.NoError(t, os.WriteFile(repoPath,
			[]byte("```redact\nliteral \"shared-secret\" -> [REDACTED_REPO]\n```\n"), 0644))
		require.NoError(t, os.WriteFile(userPath,
			[]byte("```redact\nliteral \"shared-secret\" -> [REDACTED_USER]\n```\n"), 0644))

		repoParsed, _ := ParseRedactFile(repoPath, RuleSourceRepo)
		userParsed, _ := ParseRedactFile(userPath, RuleSourceUser)

		var allRules []RedactRule
		allRules = append(allRules, repoParsed.Rules...)
		allRules = append(allRules, userParsed.Rules...)

		customPatterns, _ := RulesToPatterns(allRules)
		redactor := NewRedactorWithPatterns(customPatterns)

		output, _ := redactor.RedactString("found shared-secret in config")

		// repo rule runs first since it's earlier in the merged slice
		assert.Contains(t, output, "[REDACTED_REPO]")
		assert.NotContains(t, output, "[REDACTED_USER]")
		assert.NotContains(t, output, "shared-secret")
	})
}

// TestCustomRules_CascadingReplacement verifies behavior when a replacement
// token from one rule could be matched by a subsequent rule.
func TestCustomRules_CascadingReplacement(t *testing.T) {
	t.Run("replacement token matched by later pattern", func(t *testing.T) {
		// rule A replaces text, rule B's regex matches part of rule A's replacement
		rules := []RedactRule{
			{
				Type:        "literal",
				RawPattern:  "internal-api-key-abc123",
				Replacement: "[REDACTED_INTERNAL_KEY]",
				Source:      RuleSourceRepo,
				SourcePath:  "test",
				LineNumber:  1,
			},
			{
				Type:        "regex",
				RawPattern:  "INTERNAL",
				Replacement: "[MATCHED_INTERNAL]",
				Source:      RuleSourceUser,
				SourcePath:  "test",
				LineNumber:  2,
			},
		}

		patterns, errs := RulesToPatterns(rules)
		require.Empty(t, errs)
		redactor := NewRedactorWithPatterns(patterns)

		input := "key: internal-api-key-abc123"
		output, _ := redactor.RedactString(input)

		// rule A fires first, producing "[REDACTED_INTERNAL_KEY]"
		// rule B then matches "INTERNAL" inside that replacement token
		// this is the actual sequential-mutation behavior
		assert.NotContains(t, output, "internal-api-key-abc123")
		assert.Contains(t, output, "[MATCHED_INTERNAL]",
			"later pattern matches text inside earlier replacement — this is expected sequential behavior")
	})

	t.Run("safe replacement tokens do not cascade", func(t *testing.T) {
		// when replacement tokens use [REDACTED_...] format and no other
		// pattern matches that specific string, no cascading occurs
		rules := []RedactRule{
			{
				Type:        "literal",
				RawPattern:  "my-db-host.internal.net",
				Replacement: "[REDACTED_DB_HOST]",
				Source:      RuleSourceRepo,
				SourcePath:  "test",
				LineNumber:  1,
			},
			{
				Type:        "regex",
				RawPattern:  "emp_[0-9]{6}",
				Replacement: "[REDACTED_EMPLOYEE_ID]",
				Source:      RuleSourceRepo,
				SourcePath:  "test",
				LineNumber:  2,
			},
		}

		patterns, errs := RulesToPatterns(rules)
		require.Empty(t, errs)
		redactor := NewRedactorWithPatterns(patterns)

		input := "connecting to my-db-host.internal.net as emp_123456"
		output, _ := redactor.RedactString(input)

		// both replacements are clean, no cascading
		assert.Equal(t, "connecting to [REDACTED_DB_HOST] as [REDACTED_EMPLOYEE_ID]", output)
	})
}

// TestCustomRules_TeamLevelDiscovery verifies that REDACT.md files in the
// team context directory are discovered and applied via the full
// NewRedactorWithCustomRules path (config.json -> config.local.toml -> team context).
func TestCustomRules_TeamLevelDiscovery(t *testing.T) {
	// set up project root with .sageox/config.json
	projectRoot := t.TempDir()
	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	projectCfg := &config.ProjectConfig{
		TeamID:   "team_redact_test",
		TeamName: "Redact Test Team",
		Endpoint: "https://test.sageox.ai",
	}
	cfgJSON, err := json.Marshal(projectCfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), cfgJSON, 0644))

	// set up team context directory with docs/REDACT.md
	teamContextDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(teamContextDir, "docs"), 0755))
	teamRedact := "```redact\nliteral \"team-infra.internal.io\" -> [REDACTED_TEAM_HOST]\nregex \"TEAM-KEY-[a-f0-9]{16}\" -> [REDACTED_TEAM_KEY]\n```\n"
	require.NoError(t, os.WriteFile(
		filepath.Join(teamContextDir, "docs", "REDACT.md"),
		[]byte(teamRedact), 0644,
	))

	// write config.local.toml pointing to team context
	localCfg := &config.LocalConfig{
		TeamContexts: []config.TeamContext{
			{
				TeamID:   "team_redact_test",
				TeamName: "Redact Test Team",
				Path:     teamContextDir,
			},
		},
	}
	require.NoError(t, config.SaveLocalConfig(projectRoot, localCfg))

	// create redactor through the full discovery path
	redactor, errs := NewRedactorWithCustomRules(projectRoot)
	require.Empty(t, errs)

	// verify team rules are active
	output, found := redactor.RedactString("connecting to team-infra.internal.io with TEAM-KEY-0123456789abcdef")
	assert.NotEmpty(t, found)
	assert.Contains(t, output, "[REDACTED_TEAM_HOST]")
	assert.Contains(t, output, "[REDACTED_TEAM_KEY]")
	assert.NotContains(t, output, "team-infra.internal.io")
	assert.NotContains(t, output, "TEAM-KEY-0123456789abcdef")

	// verify builtin patterns still work alongside team rules
	output2, found2 := redactor.RedactString("key=AKIAIOSFODNN7EXAMPLE")
	assert.NotEmpty(t, found2)
	assert.Contains(t, output2, "[REDACTED_AWS_KEY]")
}

// TestCustomRules_SessionStopPath simulates the redaction path used by
// processSession/processAgentSession: NewRedactorWithCustomRules -> RedactEntries + RedactMap.
// Verifies both builtin and custom rules apply through both code paths.
func TestCustomRules_SessionStopPath(t *testing.T) {
	// set up project with repo-level REDACT.md
	projectRoot := t.TempDir()
	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	redactContent := "```redact\nliteral \"payments.internal.acme.net\" -> [REDACTED_INTERNAL]\nregex \"ACME-KEY-[a-f0-9]{32}\" -> [REDACTED_ACME_KEY]\n```\n"
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "REDACT.md"), []byte(redactContent), 0644))

	redactor, errs := NewRedactorWithCustomRules(projectRoot)
	require.Empty(t, errs)

	// --- path 1: RedactEntries (structured session entries) ---
	entries := []Entry{
		{
			Type:    EntryTypeUser,
			Content: "Deploy to payments.internal.acme.net using ACME-KEY-deadbeef0123456789abcdef01234567",
		},
		{
			Type:       EntryTypeTool,
			Content:    "bash output",
			ToolName:   "bash",
			ToolInput:  "curl https://payments.internal.acme.net/health",
			ToolOutput: "Connected. Token: ghp_1234567890abcdefghijklmnopqrstuvwxyz12",
		},
		{
			Type:    EntryTypeAssistant,
			Content: "The API key ACME-KEY-aabbccdd00112233445566778899eeff is in the config at payments.internal.acme.net",
		},
	}

	entryCount := redactor.RedactEntries(entries)
	assert.Equal(t, 3, entryCount, "all 3 entries should have secrets redacted")

	// entry 0: custom rules in Content
	assert.Contains(t, entries[0].Content, "[REDACTED_INTERNAL]")
	assert.Contains(t, entries[0].Content, "[REDACTED_ACME_KEY]")
	assert.NotContains(t, entries[0].Content, "payments.internal.acme.net")

	// entry 1: custom rule in ToolInput, builtin rule in ToolOutput
	assert.Contains(t, entries[1].ToolInput, "[REDACTED_INTERNAL]")
	assert.Contains(t, entries[1].ToolOutput, "[REDACTED_GITHUB_TOKEN]")
	assert.NotContains(t, entries[1].ToolOutput, "ghp_")

	// entry 2: both custom rules in Content
	assert.Contains(t, entries[2].Content, "[REDACTED_ACME_KEY]")
	assert.Contains(t, entries[2].Content, "[REDACTED_INTERNAL]")

	// --- path 2: RedactMap (raw JSON entries) ---
	rawData := map[string]any{
		"content":    "connecting to payments.internal.acme.net",
		"tool_input": "ACME-KEY-deadbeef0123456789abcdef01234567",
		"tool_output": map[string]any{
			"result": "token ghp_1234567890abcdefghijklmnopqrstuvwxyz12 accepted",
		},
		"metadata": []any{
			"host=payments.internal.acme.net",
		},
	}

	redacted := redactor.RedactMap(rawData)
	assert.True(t, redacted)

	assert.Contains(t, rawData["content"], "[REDACTED_INTERNAL]")
	assert.NotContains(t, rawData["content"], "payments.internal.acme.net")
	assert.Contains(t, rawData["tool_input"], "[REDACTED_ACME_KEY]")

	toolOutput := rawData["tool_output"].(map[string]any)
	assert.Contains(t, toolOutput["result"], "[REDACTED_GITHUB_TOKEN]")

	metadata := rawData["metadata"].([]any)
	assert.Contains(t, metadata[0], "[REDACTED_INTERNAL]")
}
