package manifest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateResolveRules_AllowsDataPaths(t *testing.T) {
	t.Parallel()
	rules := []ResolveRule{
		{Mode: ResolveModeAuto, Path: "data/github/"},
		{Mode: ResolveModeAuto, Path: "data/linear/"},
		{Mode: ResolveModeAuto, Path: "data/jira/"},
	}
	result := ValidateResolveRules(rules)
	assert.Equal(t, rules, result)
}

func TestValidateResolveRules_RejectsAutoOnDeniedPaths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		path string
	}{
		{"soul", "SOUL.md"},
		{"agents", "AGENTS.md"},
		{"team", "TEAM.md"},
		{"memory dir", "memory/"},
		{"memory subdir", "memory/shared/"},
		{"sessions dir", "sessions/"},
		{"sessions subdir", "sessions/archive/"},
		{"coworkers", "coworkers/"},
		{"sageox dir", ".sageox/"},
		{"sageox subdir", ".sageox/cache/"},
		{"docs", "docs/"},
		{"discussions", "discussions/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rules := []ResolveRule{{Mode: ResolveModeAuto, Path: tt.path}}
			result := ValidateResolveRules(rules)
			assert.Empty(t, result, "auto on %q should be rejected by hard deny list", tt.path)
		})
	}
}

func TestValidateResolveRules_AllowsNoneOnDeniedPaths(t *testing.T) {
	t.Parallel()
	// resolve none should always be allowed, even on denied paths
	rules := []ResolveRule{
		{Mode: ResolveModeNone, Path: "sessions/"},
		{Mode: ResolveModeNone, Path: "SOUL.md"},
	}
	result := ValidateResolveRules(rules)
	assert.Equal(t, rules, result)
}

func TestValidateResolveRules_MixedAllowedAndDenied(t *testing.T) {
	t.Parallel()
	input := []ResolveRule{
		{Mode: ResolveModeAuto, Path: "data/github/"},
		{Mode: ResolveModeAuto, Path: "sessions/"},
		{Mode: ResolveModeAuto, Path: "data/linear/"},
		{Mode: ResolveModeAuto, Path: "SOUL.md"},
		{Mode: ResolveModeNone, Path: "data/proprietary/"},
	}
	result := ValidateResolveRules(input)
	assert.Equal(t, []ResolveRule{
		{Mode: ResolveModeAuto, Path: "data/github/"},
		{Mode: ResolveModeAuto, Path: "data/linear/"},
		{Mode: ResolveModeNone, Path: "data/proprietary/"},
	}, result)
}

func TestValidateResolveRules_EmptyInput(t *testing.T) {
	t.Parallel()
	assert.Nil(t, ValidateResolveRules(nil))
	assert.Nil(t, ValidateResolveRules([]ResolveRule{}))
}

func TestDefaultResolveRules_CoversDataDir(t *testing.T) {
	t.Parallel()
	paths := AutoResolvePaths(DefaultResolveRules)
	assert.Contains(t, paths, "data/")
}

func TestFallbackConfig_IncludesDefaultResolveRules(t *testing.T) {
	t.Parallel()
	cfg := FallbackConfig()
	assert.Equal(t, DefaultResolveRules, cfg.ResolveRules)
}

func TestResolveModeFor(t *testing.T) {
	t.Parallel()
	rules := []ResolveRule{
		{Mode: ResolveModeAuto, Path: "data/"},
		{Mode: ResolveModeNone, Path: "data/proprietary/"},
	}
	tests := []struct {
		name string
		path string
		want ResolveMode
	}{
		{"auto for data subdir", "data/github/prs.json", ResolveModeAuto},
		{"none for denied subdir", "data/proprietary/secrets.json", ResolveModeNone},
		{"none for unmatched path", "SOUL.md", ResolveModeNone},
		{"auto for data root file", "data/index.json", ResolveModeAuto},
		{"none for no rules", "anything", ResolveModeNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := rules
			if tt.name == "none for no rules" {
				r = nil
			}
			got := ResolveModeFor(tt.path, r)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestResolveModeFor_MostSpecificWins(t *testing.T) {
	t.Parallel()
	rules := []ResolveRule{
		{Mode: ResolveModeAuto, Path: "data/"},
		{Mode: ResolveModeNone, Path: "data/proprietary/"},
		{Mode: ResolveModeAuto, Path: "data/proprietary/public/"},
	}
	assert.Equal(t, ResolveModeAuto, ResolveModeFor("data/github/prs.json", rules))
	assert.Equal(t, ResolveModeNone, ResolveModeFor("data/proprietary/keys.json", rules))
	assert.Equal(t, ResolveModeAuto, ResolveModeFor("data/proprietary/public/readme.md", rules))
}

func TestAutoResolvePaths(t *testing.T) {
	t.Parallel()
	rules := []ResolveRule{
		{Mode: ResolveModeAuto, Path: "data/github/"},
		{Mode: ResolveModeNone, Path: "data/proprietary/"},
		{Mode: ResolveModeAuto, Path: "data/linear/"},
	}
	paths := AutoResolvePaths(rules)
	assert.Equal(t, []string{"data/github/", "data/linear/"}, paths)
}

func TestAutoResolveDenyPaths(t *testing.T) {
	t.Parallel()
	rules := []ResolveRule{
		{Mode: ResolveModeAuto, Path: "data/"},
		{Mode: ResolveModeNone, Path: "data/proprietary/"},
	}
	denies := AutoResolveDenyPaths(rules)
	assert.Equal(t, []string{"data/proprietary/"}, denies)
}

func TestParse_ResolveDirective(t *testing.T) {
	t.Parallel()
	input := `version 1
include .sageox/
resolve auto data/github/
resolve auto data/linear/
resolve none data/proprietary/
`
	cfg, err := Parse(strings.NewReader(input))
	require.NoError(t, err)
	assert.ElementsMatch(t, []ResolveRule{
		{Mode: ResolveModeAuto, Path: "data/github/"},
		{Mode: ResolveModeAuto, Path: "data/linear/"},
		{Mode: ResolveModeNone, Path: "data/proprietary/"},
	}, cfg.ResolveRules)
}

func TestParse_ResolveAutoOnDeniedPathDropped(t *testing.T) {
	t.Parallel()
	input := `version 1
include .sageox/
resolve auto data/github/
resolve auto sessions/
`
	cfg, err := Parse(strings.NewReader(input))
	require.NoError(t, err)
	// sessions/ should be silently dropped (hard deny)
	assert.Equal(t, []ResolveRule{
		{Mode: ResolveModeAuto, Path: "data/github/"},
	}, cfg.ResolveRules)
}

func TestParse_NoResolveDirectiveUsesDefault(t *testing.T) {
	t.Parallel()
	input := `version 1
include .sageox/
include SOUL.md
`
	cfg, err := Parse(strings.NewReader(input))
	require.NoError(t, err)
	assert.Equal(t, DefaultResolveRules, cfg.ResolveRules)
}

func TestParse_ResolvePathTraversalRejected(t *testing.T) {
	t.Parallel()
	input := `version 1
include .sageox/
resolve auto ../data/
`
	cfg, err := Parse(strings.NewReader(input))
	require.NoError(t, err)
	// path traversal rejected, falls back to default
	assert.Equal(t, DefaultResolveRules, cfg.ResolveRules)
}

func TestParse_ResolveUnknownModeSkipped(t *testing.T) {
	t.Parallel()
	input := `version 1
include .sageox/
resolve auto data/github/
resolve semantic memory/
`
	cfg, err := Parse(strings.NewReader(input))
	require.NoError(t, err)
	// "semantic" is not a recognized mode yet — skipped
	assert.Equal(t, []ResolveRule{
		{Mode: ResolveModeAuto, Path: "data/github/"},
	}, cfg.ResolveRules)
}

func TestValidateResolveRules_RejectsEmptyPath(t *testing.T) {
	t.Parallel()
	// empty path would match every file — must be rejected
	rules := []ResolveRule{{Mode: ResolveModeAuto, Path: ""}}
	result := ValidateResolveRules(rules)
	assert.Empty(t, result, "empty path should be rejected")
}

func TestResolveModeFor_ExactFileRule(t *testing.T) {
	t.Parallel()
	rules := []ResolveRule{
		{Mode: ResolveModeNone, Path: "data/"},
		{Mode: ResolveModeAuto, Path: "data/github/prs.json"},
	}
	assert.Equal(t, ResolveModeAuto, ResolveModeFor("data/github/prs.json", rules))
	assert.Equal(t, ResolveModeNone, ResolveModeFor("data/github/issues.json", rules))
}

func TestResolveModeFor_DenyWinsTies(t *testing.T) {
	t.Parallel()
	// same path, conflicting modes — none wins (safer default)
	rules := []ResolveRule{
		{Mode: ResolveModeAuto, Path: "data/proprietary/"},
		{Mode: ResolveModeNone, Path: "data/proprietary/"},
	}
	assert.Equal(t, ResolveModeNone, ResolveModeFor("data/proprietary/file.json", rules))
}

func TestParse_ResolveMissingPathSkipped(t *testing.T) {
	t.Parallel()
	input := `version 1
include .sageox/
resolve auto
`
	cfg, err := Parse(strings.NewReader(input))
	require.NoError(t, err)
	// missing path → skipped, falls back to default
	assert.Equal(t, DefaultResolveRules, cfg.ResolveRules)
}
