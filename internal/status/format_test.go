package status

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestInferSemantic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		label    string
		value    string
		expected string
	}{
		// success indicators
		{"logged in", "Status", "logged in", "success"},
		{"yes value", "Enabled", "yes", "success"},
		{"initialized", "Project", "initialized", "success"},
		{"enabled", "Feature", "enabled", "success"},
		{"true value", "Flag", "true", "success"},
		{"case insensitive success", "Status", "Logged In", "success"},

		// error indicators
		{"not logged in", "Status", "not logged in", "error"},
		{"no value", "Configured", "no", "error"},
		{"not initialized", "Project", "not initialized", "error"},
		{"none value", "Teams", "none", "error"},
		{"disabled", "Feature", "disabled", "error"},
		{"false value", "Flag", "false", "error"},
		{"case insensitive error", "Status", "NOT LOGGED IN", "error"},

		// highlight indicators
		{"user label", "User", "Person A", "highlight"},
		{"email label", "Email", "user@example.com", "highlight"},
		{"case insensitive highlight", "USER", "Person B", "highlight"},

		// muted indicators
		{"id label", "Agent ID", "abc-123", "muted"},
		{"path label", "Config path", "/some/path", "muted"},
		{"directory label", "User directory", "/home/user", "muted"},
		{"file label", "Auth file", "/path/to/file", "muted"},
		{"expires label", "Token expires", "2025-01-01", "muted"},

		// default
		{"unknown label and value", "Foo", "bar", "default"},
		{"empty strings", "", "", "default"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := InferSemantic(tt.label, tt.value)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestFormatTimeAgo(t *testing.T) {
	t.Parallel()

	now := time.Now()

	tests := []struct {
		name     string
		t        time.Time
		expected string
	}{
		{"just now", now.Add(-10 * time.Second), "just now"},
		{"1 minute ago", now.Add(-1 * time.Minute), "1 minute ago"},
		{"5 minutes ago", now.Add(-5 * time.Minute), "5 minutes ago"},
		{"1 hour ago", now.Add(-1 * time.Hour), "1 hour ago"},
		{"3 hours ago", now.Add(-3 * time.Hour), "3 hours ago"},
		{"1 day ago", now.Add(-24 * time.Hour), "1 day ago"},
		{"4 days ago", now.Add(-4 * 24 * time.Hour), "4 days ago"},
		{"1 week ago", now.Add(-7 * 24 * time.Hour), "1 week ago"},
		{"3 weeks ago", now.Add(-21 * 24 * time.Hour), "3 weeks ago"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := FormatTimeAgo(tt.t)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestFormatEndpointDisplay(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		url      string
		expected string
	}{
		{"empty returns default", "", "(default)"},
		{"strips https", "https://sageox.ai", "sageox.ai"},
		{"strips http", "http://sageox.ai", "sageox.ai"},
		{"no protocol unchanged", "sageox.ai", "sageox.ai"},
		{"with path", "https://sageox.ai/v1", "sageox.ai/v1"},
		{"with subdomain", "https://api.test.sageox.ai", "api.test.sageox.ai"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := FormatEndpointDisplay(tt.url)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestExtractGitHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		url      string
		expected string
	}{
		{"empty string", "", ""},
		{"https url", "https://git.example.com/user/repo.git", "git.example.com"},
		{"http url", "http://git.example.com/user/repo.git", "git.example.com"},
		{"ssh url", "git@git.example.com:user/repo.git", "git.example.com"},
		{"https with credentials", "https://oauth2:token123@git.example.com/user/repo.git", "git.example.com"},
		{"host only no path", "git.example.com", "git.example.com"},
		{"malformed no host part", "@:", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ExtractGitHost(tt.url)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestFormatTokenCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		tokens   int
		expected string
	}{
		{"zero", 0, "0"},
		{"small", 500, "500"},
		{"exactly 1000", 1000, "1.0K"},
		{"thousands", 3100, "3.1K"},
		{"millions", 1200000, "1.2M"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := FormatTokenCount(tt.tokens)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestFormatDurationShort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		duration time.Duration
		expected string
	}{
		{"milliseconds", 500 * time.Millisecond, "500ms"},
		{"seconds", 5 * time.Second, "5.0s"},
		{"minutes", 3 * time.Minute, "3.0m"},
		{"hours", 2 * time.Hour, "2.0h"},
		{"sub-second", 50 * time.Millisecond, "50ms"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := FormatDurationShort(tt.duration)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestFormatGitRepoStatus(t *testing.T) {
	t.Parallel()

	now := time.Now()

	tests := []struct {
		name             string
		status           GitRepoStatus
		expectedText     string
		expectedSemantic string
	}{
		{
			"not found",
			GitRepoStatus{Exists: false},
			"not found", "error",
		},
		{
			"error",
			GitRepoStatus{Exists: true, Error: "not a git repo"},
			"not a git repo", "error",
		},
		{
			"synced no last sync",
			GitRepoStatus{Exists: true, UncommittedCount: 0},
			"synced", "success",
		},
		{
			"synced with last sync",
			GitRepoStatus{Exists: true, UncommittedCount: 0, HasLastSync: true, LastSync: now.Add(-5 * time.Minute)},
			"synced (5 minutes ago)", "success",
		},
		{
			"uncommitted changes",
			GitRepoStatus{Exists: true, UncommittedCount: 3},
			"3 uncommitted", "warning",
		},
		{
			"uncommitted with last sync",
			GitRepoStatus{Exists: true, UncommittedCount: 2, HasLastSync: true, LastSync: now.Add(-1 * time.Hour)},
			"2 uncommitted (1 hour ago)", "warning",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			text, semantic := FormatGitRepoStatus(tt.status)
			assert.Equal(t, tt.expectedText, text)
			assert.Equal(t, tt.expectedSemantic, semantic)
		})
	}
}

func TestEstimateTokens(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 0, EstimateTokens(0))
	assert.Equal(t, 250, EstimateTokens(1000))
	assert.Equal(t, 1, EstimateTokens(4))
}
