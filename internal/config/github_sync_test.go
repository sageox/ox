package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsValidGitHubSyncMode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		mode string
		want bool
	}{
		{GitHubSyncEnabled, true},
		{GitHubSyncDisabled, true},
		{"", true},
		{"invalid", false},
		{"on", false},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsValidGitHubSyncMode(tt.mode))
		})
	}
}

func TestNormalizeGitHubSync(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{GitHubSyncEnabled, GitHubSyncEnabled},
		{GitHubSyncDisabled, GitHubSyncDisabled},
		{"", GitHubSyncEnabled},
		{"invalid", GitHubSyncEnabled},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, NormalizeGitHubSync(tt.input))
		})
	}
}

func TestResolveGitHubSync_Default(t *testing.T) {
	// no t.Parallel(): reads os.Getenv that sibling tests mutate via t.Setenv
	// no env var, no project config → default enabled
	got := ResolveGitHubSync("")
	assert.Equal(t, GitHubSyncEnabled, got)
}

func TestResolveGitHubSync_EnvOverride(t *testing.T) {
	t.Setenv("OX_GITHUB_SYNC", "disabled")
	got := ResolveGitHubSync("")
	assert.Equal(t, GitHubSyncDisabled, got)
}

func TestResolveGitHubSync_InvalidEnv(t *testing.T) {
	t.Setenv("OX_GITHUB_SYNC", "invalid")
	got := ResolveGitHubSync("")
	assert.Equal(t, GitHubSyncEnabled, got) // normalized to enabled
}

func TestResolveGitHubSyncPRs_MasterDisabled(t *testing.T) {
	t.Setenv("OX_GITHUB_SYNC", "disabled")
	got := ResolveGitHubSyncPRs("")
	assert.Equal(t, GitHubSyncDisabled, got)
}

func TestResolveGitHubSyncPRs_Default(t *testing.T) {
	got := ResolveGitHubSyncPRs("")
	assert.Equal(t, GitHubSyncEnabled, got)
}

func TestResolveGitHubSyncPRs_EnvOverride(t *testing.T) {
	t.Setenv("OX_GITHUB_SYNC_PRS", "disabled")
	got := ResolveGitHubSyncPRs("")
	assert.Equal(t, GitHubSyncDisabled, got)
}

func TestResolveGitHubSyncIssues_MasterDisabled(t *testing.T) {
	t.Setenv("OX_GITHUB_SYNC", "disabled")
	got := ResolveGitHubSyncIssues("")
	assert.Equal(t, GitHubSyncDisabled, got)
}

func TestResolveGitHubSyncIssues_Default(t *testing.T) {
	got := ResolveGitHubSyncIssues("")
	assert.Equal(t, GitHubSyncEnabled, got)
}

func TestResolveGitHubSyncIssues_EnvOverride(t *testing.T) {
	t.Setenv("OX_GITHUB_SYNC_ISSUES", "disabled")
	got := ResolveGitHubSyncIssues("")
	assert.Equal(t, GitHubSyncDisabled, got)
}

func TestResolveGitHubSyncPRs_WithTempProject(t *testing.T) {
	// temp dir with no config — defaults to enabled
	dir := t.TempDir()
	got := ResolveGitHubSyncPRs(dir)
	assert.Equal(t, GitHubSyncEnabled, got)
}

func TestResolveGitHubSyncIssues_WithTempProject(t *testing.T) {
	dir := t.TempDir()
	got := ResolveGitHubSyncIssues(dir)
	assert.Equal(t, GitHubSyncEnabled, got)
}
