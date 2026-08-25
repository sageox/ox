package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/status"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildStatusJSON_WithRepoDetail(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	localCfg := &config.LocalConfig{
		Ledger: &config.LedgerConfig{Path: tmpDir},
	}

	repoDetail := &api.RepoDetailResponse{
		Visibility:  "private",
		AccessLevel: "member",
	}

	output := buildStatusJSON(
		false, nil, nil, "test.sageox.ai", "/tmp/auth.json", false,
		"/tmp/config", "/tmp/cwd", "/tmp/cwd/.sageox", false,
		localCfg, "", repoDetail, nil,
		nil, nil,
		statusBubblesSummary{},
	)

	require.NotNil(t, output.Ledger, "ledger section should be populated when localCfg.Ledger has a path")
	assert.Equal(t, "private", output.Ledger.Visibility)
	assert.Equal(t, "member", output.Ledger.AccessLevel)
	assert.True(t, output.Ledger.Configured)
	assert.Equal(t, tmpDir, output.Ledger.Path)
}

func TestBuildStatusJSON_WithoutRepoDetail(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	localCfg := &config.LocalConfig{
		Ledger: &config.LedgerConfig{Path: tmpDir},
	}

	output := buildStatusJSON(
		false, nil, nil, "test.sageox.ai", "/tmp/auth.json", false,
		"/tmp/config", "/tmp/cwd", "/tmp/cwd/.sageox", false,
		localCfg, "", nil, nil,
		nil, nil,
		statusBubblesSummary{},
	)

	require.NotNil(t, output.Ledger, "ledger section should be populated even without repoDetail")
	assert.Empty(t, output.Ledger.Visibility, "visibility should be empty when repoDetail is nil")
	assert.Empty(t, output.Ledger.AccessLevel, "access_level should be empty when repoDetail is nil")
	assert.True(t, output.Ledger.Configured)
}

func TestBuildStatusJSON_ViewerAccess(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	localCfg := &config.LocalConfig{
		Ledger: &config.LedgerConfig{Path: tmpDir},
	}

	repoDetail := &api.RepoDetailResponse{
		Visibility:  "public",
		AccessLevel: "viewer",
	}

	output := buildStatusJSON(
		false, nil, nil, "test.sageox.ai", "/tmp/auth.json", false,
		"/tmp/config", "/tmp/cwd", "/tmp/cwd/.sageox", false,
		localCfg, "", repoDetail, nil,
		nil, nil,
		statusBubblesSummary{},
	)

	require.NotNil(t, output.Ledger)
	assert.Equal(t, "public", output.Ledger.Visibility)
	assert.Equal(t, "viewer", output.Ledger.AccessLevel)
}

func TestBuildStatusJSON_NoLedgerConfig(t *testing.T) {
	t.Parallel()

	// no ledger configured means repoDetail fields have no place to land
	localCfg := &config.LocalConfig{}

	repoDetail := &api.RepoDetailResponse{
		Visibility:  "private",
		AccessLevel: "member",
	}

	output := buildStatusJSON(
		false, nil, nil, "test.sageox.ai", "/tmp/auth.json", false,
		"/tmp/config", "/tmp/cwd", "/tmp/cwd/.sageox", false,
		localCfg, "", repoDetail, nil,
		nil, nil,
		statusBubblesSummary{},
	)

	require.NotNil(t, output.Ledger, "ledger section should always be present")
	assert.False(t, output.Ledger.Configured, "ledger should not be configured")
}

func TestBuildStatusJSON_NilLocalConfig(t *testing.T) {
	t.Parallel()

	output := buildStatusJSON(
		false, nil, nil, "test.sageox.ai", "/tmp/auth.json", false,
		"/tmp/config", "/tmp/cwd", "/tmp/cwd/.sageox", false,
		nil, "", nil, nil,
		nil, nil,
		statusBubblesSummary{},
	)

	require.NotNil(t, output.Ledger, "ledger section should always be present")
	assert.False(t, output.Ledger.Configured, "ledger should not be configured")
	assert.Nil(t, output.TeamContexts, "team_contexts should be nil when localCfg is nil")
}

func TestBuildStatusJSON_LedgerPathNotExists(t *testing.T) {
	t.Parallel()

	// path that doesn't exist triggers "not found" error in getGitRepoStatus
	localCfg := &config.LocalConfig{
		Ledger: &config.LedgerConfig{Path: "/nonexistent/path/ledger"},
	}

	repoDetail := &api.RepoDetailResponse{
		Visibility:  "private",
		AccessLevel: "viewer",
	}

	output := buildStatusJSON(
		false, nil, nil, "test.sageox.ai", "/tmp/auth.json", false,
		"/tmp/config", "/tmp/cwd", "/tmp/cwd/.sageox", false,
		localCfg, "", repoDetail, nil,
		nil, nil,
		statusBubblesSummary{},
	)

	require.NotNil(t, output.Ledger)
	assert.False(t, output.Ledger.Exists, "ledger should not exist for nonexistent path")
	assert.Equal(t, "not found", output.Ledger.Error)
	// visibility/access are still populated from repoDetail regardless of local state
	assert.Equal(t, "private", output.Ledger.Visibility)
	assert.Equal(t, "viewer", output.Ledger.AccessLevel)
}

func TestBuildStatusJSON_AuthenticatedWithToken(t *testing.T) {
	t.Parallel()

	expires := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	token := &auth.StoredToken{
		AccessToken: "test-token",
		ExpiresAt:   expires,
		UserInfo: auth.UserInfo{
			UserID: "user_123",
			Email:  "person@example.com",
			Name:   "Person A",
		},
	}

	output := buildStatusJSON(
		true, nil, token, "test.sageox.ai", "/tmp/auth.json", true,
		"/tmp/config", "/tmp/cwd", "/tmp/cwd/.sageox", true,
		nil, "", nil, nil,
		nil, nil,
		statusBubblesSummary{},
	)

	require.NotNil(t, output.Auth)
	assert.True(t, output.Auth.Authenticated)
	assert.Equal(t, "Person A", output.Auth.User)
	assert.Equal(t, "person@example.com", output.Auth.Email)
	assert.Equal(t, &expires, output.Auth.ExpiresAt)
}

func TestBuildStatusJSON_ProjectInitialized(t *testing.T) {
	t.Parallel()

	output := buildStatusJSON(
		false, nil, nil, "test.sageox.ai", "/tmp/auth.json", false,
		"/tmp/config", "/tmp/cwd", "/tmp/cwd/.sageox", true,
		nil, "", nil, nil,
		nil, nil,
		statusBubblesSummary{},
	)

	require.NotNil(t, output.Project)
	assert.True(t, output.Project.Initialized)
	assert.Equal(t, "/tmp/cwd/.sageox", output.Project.ConfigPath)
}

func TestBuildStatusJSON_ProjectNotInitialized(t *testing.T) {
	t.Parallel()

	output := buildStatusJSON(
		false, nil, nil, "test.sageox.ai", "/tmp/auth.json", false,
		"/tmp/config", "/tmp/cwd", "/tmp/cwd/.sageox", false,
		nil, "", nil, nil,
		nil, nil,
		statusBubblesSummary{},
	)

	require.NotNil(t, output.Project)
	assert.False(t, output.Project.Initialized)
	assert.Empty(t, output.Project.ConfigPath, "config path should be empty when not initialized")
}

func TestBuildStatusJSON_VisibilityAccessLevelCombinations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		visibility  string
		accessLevel string
	}{
		{
			name:        "private repo with member access",
			visibility:  "private",
			accessLevel: "member",
		},
		{
			name:        "private repo with viewer access",
			visibility:  "private",
			accessLevel: "viewer",
		},
		{
			name:        "public repo with member access",
			visibility:  "public",
			accessLevel: "member",
		},
		{
			name:        "public repo with viewer access",
			visibility:  "public",
			accessLevel: "viewer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()
			localCfg := &config.LocalConfig{
				Ledger: &config.LedgerConfig{Path: tmpDir},
			}
			repoDetail := &api.RepoDetailResponse{
				Visibility:  tt.visibility,
				AccessLevel: tt.accessLevel,
			}

			output := buildStatusJSON(
				false, nil, nil, "test.sageox.ai", "/tmp/auth.json", false,
				"/tmp/config", "/tmp/cwd", "/tmp/cwd/.sageox", false,
				localCfg, "", repoDetail, nil,
				nil, nil,
				statusBubblesSummary{},
			)

			require.NotNil(t, output.Ledger)
			assert.Equal(t, tt.visibility, output.Ledger.Visibility)
			assert.Equal(t, tt.accessLevel, output.Ledger.AccessLevel)
		})
	}
}

func TestShortenPathViaSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require Developer Mode on Windows")
	}
	t.Parallel()

	target := filepath.Join(t.TempDir(), "data", "ledger")
	require.NoError(t, os.MkdirAll(target, 0755))

	projectRoot := filepath.Join(t.TempDir(), "project")
	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	require.NoError(t, os.Symlink(target, filepath.Join(sageoxDir, "ledger")))

	tests := []struct {
		name       string
		root       string
		fullPath   string
		candidates []string
		want       string
	}{
		{
			name:       "symlink matches",
			root:       projectRoot,
			fullPath:   target,
			candidates: []string{".sageox/ledger"},
			want:       ".sageox/ledger",
		},
		{
			name:       "no match returns full path",
			root:       projectRoot,
			fullPath:   "/some/other/path",
			candidates: []string{".sageox/ledger"},
			want:       "/some/other/path",
		},
		{
			name:       "empty root returns full path",
			root:       "",
			fullPath:   target,
			candidates: []string{".sageox/ledger"},
			want:       target,
		},
		{
			name:       "empty fullPath returns empty",
			root:       projectRoot,
			fullPath:   "",
			candidates: []string{".sageox/ledger"},
			want:       "",
		},
		{
			name:       "nonexistent symlink returns full path",
			root:       projectRoot,
			fullPath:   target,
			candidates: []string{".sageox/teams/primary"},
			want:       target,
		},
		{
			name:       "first matching candidate wins",
			root:       projectRoot,
			fullPath:   target,
			candidates: []string{".sageox/teams/primary", ".sageox/ledger"},
			want:       ".sageox/ledger",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shortenPathViaSymlink(tt.root, tt.fullPath, tt.candidates...)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestStatusJSONOutput_OmitsEmptyFields(t *testing.T) {
	t.Parallel()

	// minimal output — optional fields should be omitted
	output := statusJSONOutput{
		Auth:    &statusAuthJSON{Authenticated: false, Endpoint: "test.sageox.ai"},
		Config:  &statusConfigJSON{UserConfigDir: "/tmp"},
		Project: &statusProjectJSON{Initialized: false},
		Ledger:  &statusLedgerJSON{Configured: false},
	}

	data, err := json.Marshal(output)
	require.NoError(t, err)

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &raw))

	// omitempty fields should not appear
	_, hasTeamContexts := raw["team_contexts"]
	assert.False(t, hasTeamContexts, "empty team_contexts should be omitted")
	_, hasAICoworkers := raw["ai_coworkers"]
	assert.False(t, hasAICoworkers, "empty ai_coworkers should be omitted")
	_, hasDaemon := raw["daemon"]
	assert.False(t, hasDaemon, "nil daemon should be omitted")
	_, hasVersion := raw["version"]
	assert.False(t, hasVersion, "nil version should be omitted")

	// required fields should always appear
	_, hasAuth := raw["auth"]
	assert.True(t, hasAuth, "auth should always be present")
	_, hasConfig := raw["config"]
	assert.True(t, hasConfig, "config should always be present")
	_, hasProject := raw["project"]
	assert.True(t, hasProject, "project should always be present")
	_, hasLedger := raw["ledger"]
	assert.True(t, hasLedger, "ledger should always be present")
}

func TestStatusJSONOutput_RoundTrip(t *testing.T) {
	t.Parallel()

	expires := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	output := statusJSONOutput{
		Auth: &statusAuthJSON{
			Authenticated: true,
			Endpoint:      "sageox.ai",
			User:          "Person A",
			Email:         "person@example.com",
			ExpiresAt:     &expires,
		},
		Config:  &statusConfigJSON{UserConfigDir: "/home/user/.config/sageox", AuthFile: "/home/user/.config/sageox/auth.json", AuthFileExists: true},
		Project: &statusProjectJSON{Initialized: true, Directory: "/project", ConfigPath: "/project/.sageox"},
		Ledger:  &statusLedgerJSON{Configured: true, Path: "/data/ledger", Exists: true, Branch: "main", Visibility: "private", AccessLevel: "member"},
		Daemon:  &statusDaemonJSON{Running: true},
		Version: &statusVersionJSON{Current: "0.17.0", Latest: "0.18.0", UpdateAvailable: true},
	}

	data, err := json.MarshalIndent(output, "", "  ")
	require.NoError(t, err)

	var decoded statusJSONOutput
	require.NoError(t, json.Unmarshal(data, &decoded))

	assert.Equal(t, output.Auth.Authenticated, decoded.Auth.Authenticated)
	assert.Equal(t, output.Auth.User, decoded.Auth.User)
	assert.Equal(t, output.Ledger.Visibility, decoded.Ledger.Visibility)
	assert.Equal(t, output.Version.UpdateAvailable, decoded.Version.UpdateAvailable)
	assert.Equal(t, output.Version.Latest, decoded.Version.Latest)
}

func TestInferSemantic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		label string
		value string
		want  string
	}{
		// success indicators
		{"logged in", "Auth", "Logged in", "success"},
		{"yes value", "Enabled", "Yes", "success"},
		{"initialized", "Project", "Initialized", "success"},
		{"enabled", "Feature", "Enabled", "success"},
		{"true value", "Flag", "True", "success"},
		{"case insensitive success", "Auth", "LOGGED IN", "success"},

		// error indicators
		{"not logged in", "Auth", "Not logged in", "error"},
		{"no value", "Enabled", "No", "error"},
		{"not initialized", "Project", "Not initialized", "error"},
		{"none value", "Items", "None", "error"},
		{"disabled", "Feature", "Disabled", "error"},
		{"false value", "Flag", "False", "error"},

		// highlight (user identity)
		{"user label", "User", "Person A", "highlight"},
		{"email label", "Email", "person@example.com", "highlight"},
		{"user label case insensitive", "user", "someone", "highlight"},

		// muted (technical details)
		{"path label", "Config Path", "/some/path", "muted"},
		{"directory label", "Directory", "/tmp", "muted"},
		{"file label", "Auth File", "auth.json", "muted"},
		{"id label", "Team ID", "abc-123", "muted"},
		{"expires label", "Expires", "2026-01-01", "muted"},

		// default (nothing matched)
		{"generic value", "Status", "running", "default"},
		{"generic label and value", "Count", "42", "default"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := status.InferSemantic(tt.label, tt.value)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFormatValue_PrefixesBySemanticType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		semantic   string
		wantPrefix string
	}{
		{"success", "✓ "},
		{"error", "✗ "},
		{"warning", "⚠ "},
	}

	for _, tt := range tests {
		t.Run(tt.semantic, func(t *testing.T) {
			t.Parallel()
			got := formatValue("test", tt.semantic)
			assert.Contains(t, got, tt.wantPrefix+"test")
		})
	}
}

func TestFormatValue_ContainsOriginalValue(t *testing.T) {
	t.Parallel()

	semantics := []string{"success", "error", "warning", "highlight", "muted", "default", "unknown"}
	for _, sem := range semantics {
		t.Run(sem, func(t *testing.T) {
			t.Parallel()
			got := formatValue("myvalue", sem)
			assert.Contains(t, got, "myvalue")
		})
	}
}

func TestRenderVisibility(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		visibility string
		wantText   string
	}{
		{"public", "public", "public"},
		{"private", "private", "private"},
		{"Public uppercase", "Public", "Public"},
		{"unknown falls through", "internal", "internal"},
		{"empty string", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := renderVisibility(tt.visibility)
			assert.Contains(t, got, tt.wantText)
		})
	}
}

func TestRenderVisibilityWithAccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		visibility  string
		accessLevel string
		wantParts   []string
	}{
		{
			"viewer shows read-only",
			"private", "viewer",
			[]string{"private", "read-only"},
		},
		{
			"member shows checkmark",
			"private", "member",
			[]string{"private", "✓ member"},
		},
		{
			"owner shows checkmark",
			"public", "owner",
			[]string{"public", "✓ owner"},
		},
		{
			"empty access level no suffix",
			"private", "",
			[]string{"private"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := renderVisibilityWithAccess(tt.visibility, tt.accessLevel)
			for _, part := range tt.wantParts {
				assert.Contains(t, got, part)
			}
		})
	}

	// viewer should NOT contain the checkmark
	t.Run("viewer excludes checkmark", func(t *testing.T) {
		t.Parallel()
		got := renderVisibilityWithAccess("private", "viewer")
		assert.NotContains(t, got, "✓")
	})
}

func TestRenderTable(t *testing.T) {
	t.Parallel()

	t.Run("contains header and underline", func(t *testing.T) {
		t.Parallel()
		got := renderTable("Auth", [][]string{{"Status", "Logged in"}})
		assert.Contains(t, got, "Auth")
		assert.Contains(t, got, "────")
	})

	t.Run("contains row values", func(t *testing.T) {
		t.Parallel()
		got := renderTable("Config", [][]string{
			{"Path", "/tmp/config"},
			{"File", "auth.json"},
		})
		assert.Contains(t, got, "/tmp/config")
		assert.Contains(t, got, "auth.json")
	})

	t.Run("explicit semantic overrides auto-detect", func(t *testing.T) {
		t.Parallel()
		// "Yes" would auto-detect as success, but explicit "warning" overrides
		got := renderTable("Test", [][]string{{"Status", "Yes", "warning"}})
		assert.Contains(t, got, "⚠")
		assert.Contains(t, got, "Yes")
	})

	t.Run("auto-detects semantic from value", func(t *testing.T) {
		t.Parallel()
		got := renderTable("Test", [][]string{{"Status", "Not logged in"}})
		assert.Contains(t, got, "✗")
	})
}

func TestFormatGitRepoStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		status       gitRepoStatus
		wantText     string
		wantSemantic string
	}{
		{
			"not found",
			gitRepoStatus{Exists: false},
			"not found", "error",
		},
		{
			"error present",
			gitRepoStatus{Exists: true, Error: "not a git repo"},
			"not a git repo", "error",
		},
		{
			"synced no last sync",
			gitRepoStatus{Exists: true, UncommittedCount: 0},
			"synced", "success",
		},
		{
			"synced with last sync time",
			gitRepoStatus{
				Exists:           true,
				UncommittedCount: 0,
				HasLastSync:      true,
			},
			"synced (just now)", "success",
		},
		{
			"uncommitted changes",
			gitRepoStatus{Exists: true, UncommittedCount: 3},
			"3 uncommitted", "warning",
		},
		{
			"single uncommitted",
			gitRepoStatus{Exists: true, UncommittedCount: 1},
			"1 uncommitted", "warning",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := tt.status
			if input.HasLastSync {
				input.LastSync = time.Now().Add(-30 * time.Second)
			}
			text, semantic := status.FormatGitRepoStatus(input)
			assert.Contains(t, text, tt.wantText)
			assert.Equal(t, tt.wantSemantic, semantic)
		})
	}
}

func TestFormatTimeAgo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		offset time.Duration
		want   string
	}{
		{"just now", -5 * time.Second, "just now"},
		{"1 minute ago", -1 * time.Minute, "1 minute ago"},
		{"multiple minutes", -15 * time.Minute, "15 minutes ago"},
		{"1 hour ago", -1 * time.Hour, "1 hour ago"},
		{"multiple hours", -5 * time.Hour, "5 hours ago"},
		{"1 day ago", -25 * time.Hour, "1 day ago"},
		{"multiple days", -72 * time.Hour, "3 days ago"},
		{"1 week ago", -8 * 24 * time.Hour, "1 week ago"},
		{"multiple weeks", -21 * 24 * time.Hour, "3 weeks ago"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := status.FormatTimeAgo(time.Now().Add(tt.offset))
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFormatEndpointDisplay(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
		want string
	}{
		{"empty returns default", "", "(default)"},
		{"strips https", "https://sageox.ai", "sageox.ai"},
		{"strips http", "http://sageox.ai", "sageox.ai"},
		{"preserves bare host", "sageox.ai", "sageox.ai"},
		{"strips https with subdomain", "https://api.test.sageox.ai", "api.test.sageox.ai"},
		{"preserves path after strip", "https://sageox.ai/v1", "sageox.ai/v1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := status.FormatEndpointDisplay(tt.url)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestExtractGitHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
		want string
	}{
		{"empty", "", ""},
		{"https url", "https://git.example.com/user/repo.git", "git.example.com"},
		{"http url", "http://git.example.com/user/repo.git", "git.example.com"},
		{"ssh url", "git@git.example.com:user/repo.git", "git.example.com"},
		{"https with credentials", "https://oauth2:token@git.example.com/user/repo.git", "git.example.com"},
		{"bare host", "git.example.com/user/repo.git", "git.example.com"},
		{"host only no path", "git.example.com", "git.example.com"},
		{"https host no path", "https://git.example.com", "git.example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := status.ExtractGitHost(tt.url)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFormatDurationShort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		duration time.Duration
		want     string
	}{
		{"milliseconds", 500 * time.Millisecond, "500ms"},
		{"sub-millisecond", 100 * time.Microsecond, "0ms"},
		{"one second", 1 * time.Second, "1.0s"},
		{"seconds", 45500 * time.Millisecond, "45.5s"},
		{"one minute", 60 * time.Second, "1.0m"},
		{"minutes", 150 * time.Second, "2.5m"},
		{"one hour", 60 * time.Minute, "1.0h"},
		{"hours", 90 * time.Minute, "1.5h"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := status.FormatDurationShort(tt.duration)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- renderAuthStatus: principal-kind-aware identity ---
//
// These use t.Setenv, which is incompatible with t.Parallel — auth-env
// resolution is process-global (SAGEOX_TOKEN, XDG_CONFIG_HOME), matching the
// serial pattern internal/auth's own env-token tests use.

// Pinned token vectors. These are real, correctly-checksummed values for the
// "valid" pair (so they clear the local format check and reach the mock
// introspection server) and single-character corruptions for the "malformed"
// pair (so they fail it). See internal/auth/env_token_test.go for the vectors'
// own coverage.
const (
	validTeamToken       = "oxt_test_1ljPfr"
	validPersonalToken   = "oxp_test_4bDZfN"
	malformedTeamToken   = "oxt_test_1ljPfX"
	malformedPersonalPAT = "oxp_test_4bDZfX"
)

// setupAuthRenderEnv isolates renderAuthStatus from every process-global input
// it reads: the endpoint, SAGEOX_TOKEN, and the auth.json location.
//
// OX_GIT_CREDENTIALS_FILE is the load-bearing one: without it the Git PAT
// section reaches gitserver.LoadCredentialsForEndpoint, which probes the
// developer's real OS keychain. That makes a pure render test depend on the
// host's login keychain state (and, on a locked machine, on a UI prompt).
func setupAuthRenderEnv(t *testing.T, endpointURL, envToken string) {
	t.Helper()
	t.Setenv("SAGEOX_ENDPOINT", endpointURL)
	t.Setenv("SAGEOX_TOKEN", envToken)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("OX_GIT_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "creds.json"))
}

// writeDiskLogin plants a live auth.json login for ep, with a distinctive
// identity, so a test can prove whether that identity leaks into output.
func writeDiskLogin(t *testing.T, ep, name, email string) {
	t.Helper()
	authPath, err := auth.GetAuthFilePath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(authPath), 0o700))

	store := auth.AuthStore{Tokens: map[string]*auth.StoredToken{
		endpoint.NormalizeEndpoint(ep): {
			AccessToken:  "disk-access-token",
			RefreshToken: "disk-refresh-token",
			ExpiresAt:    time.Now().Add(24 * time.Hour),
			TokenType:    "Bearer",
			UserInfo:     auth.UserInfo{UserID: "u_disk", Name: name, Email: email},
		},
	}}
	blob, err := json.Marshal(store)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(authPath, blob, 0o600))
}

// countEndpointBlocks counts rendered "Endpoint" rows. Asserting on it keeps a
// test-isolation leak (a stray auth.json or a shared endpoint) from silently
// satisfying a Contains() assertion via somebody else's block.
func countEndpointBlocks(out string) int {
	return strings.Count(out, "Endpoint")
}

// TestRenderAuthStatus_MalformedEnvTokenIsReported: with SAGEOX_TOKEN set to a
// value that fails the local format check and no auth.json behind it, the
// endpoint holds no usable credential and so vanishes from
// auth.GetLoggedInEndpoints() entirely. Status then rendered a bare
// "✗ not logged in" and advised `ox login` — the one diagnosis that never
// mentions the credential the operator actually supplied, given to the CI
// operator who is the entire audience for SAGEOX_TOKEN and cannot run a
// browser login anyway.
func TestRenderAuthStatus_MalformedEnvTokenIsReported(t *testing.T) {
	setupAuthRenderEnv(t, "http://127.0.0.1:1", malformedTeamToken)

	out := renderAuthStatus("/tmp/auth.json")

	if !strings.Contains(out, auth.EnvVarToken) {
		t.Errorf("output must name %s so the operator knows which credential was refused, got:\n%s", auth.EnvVarToken, out)
	}
	if !strings.Contains(out, "local format check") {
		t.Errorf("output must say the value failed a local format check, got:\n%s", out)
	}
	if strings.Contains(out, "not logged in") {
		t.Errorf("a refused env token must not render as a bare \"not logged in\", got:\n%s", out)
	}
	// Family-aware: a truncated team token and a truncated PAT want different
	// next steps, and `ox login` cannot mint the former at all.
	if !strings.Contains(out, "team token") {
		t.Errorf("expected team-family guidance for an oxt_ value, got:\n%s", out)
	}
	if got := countEndpointBlocks(out); got != 1 {
		t.Errorf("expected exactly 1 Endpoint block, got %d:\n%s", got, out)
	}
}

// TestRenderAuthStatus_MalformedEnvTokenMustNotSilentlyFallBackToDiskIdentity
// is the security test. With a malformed SAGEOX_TOKEN AND a live disk login for
// the same endpoint, status used to render "(✓ logged in)" and the human's
// name — while the operator believed CI was acting as a service account. Every
// API call, ledger write, and murmur would be attributed to that human.
func TestRenderAuthStatus_MalformedEnvTokenMustNotSilentlyFallBackToDiskIdentity(t *testing.T) {
	const ep = "http://127.0.0.1:1"
	const diskName = "Disk Login Human"
	const diskEmail = "disk-login-human@example.test"

	setupAuthRenderEnv(t, ep, malformedTeamToken)
	writeDiskLogin(t, ep, diskName, diskEmail)

	out := renderAuthStatus("/tmp/auth.json")

	mentionsEnvRefusal := strings.Contains(out, auth.EnvVarToken) &&
		(strings.Contains(out, "NOT being used") || strings.Contains(out, "local format check"))

	if strings.Contains(out, diskName) || strings.Contains(out, diskEmail) {
		if !mentionsEnvRefusal {
			t.Fatalf("SECURITY: status presented the stored disk identity %q <%s> as the active credential while %s was set and unusable — "+
				"every API call, ledger write, and murmur would be attributed to that person instead of the principal the operator named. Output:\n%s",
				diskName, diskEmail, auth.EnvVarToken, out)
		}
	}
	if !mentionsEnvRefusal {
		t.Fatalf("output must say %s was set and is not being used, got:\n%s", auth.EnvVarToken, out)
	}
	if strings.Contains(out, "✓ logged in") {
		t.Fatalf("a refused env token must not produce a positive endpoint verdict, got:\n%s", out)
	}
	if got := countEndpointBlocks(out); got != 1 {
		t.Fatalf("expected exactly 1 Endpoint block, got %d:\n%s", got, out)
	}
}

// TestRenderAuthStatus_UnreachableServerIsNotReportedAsRejected: a developer
// offline (or behind a VPN that is down) with a perfectly good token was told
// "(✗ not authenticated)", "SAGEOX_TOKEN (env) rejected", and to "mint a fresh
// PAT". The server never answered, so "rejected" is a false claim about a
// credential nobody judged, and the advice costs a needless rotation.
func TestRenderAuthStatus_UnreachableServerIsNotReportedAsRejected(t *testing.T) {
	// Bind a real port, then close it, so the address is well-formed and
	// nothing is listening — a connection refusal, not a DNS failure.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close()

	setupAuthRenderEnv(t, url, validTeamToken)

	out := renderAuthStatus("/tmp/auth.json")

	if strings.Contains(out, "rejected") {
		t.Errorf("an unreachable endpoint must not be reported as a rejection, got:\n%s", out)
	}
	if strings.Contains(out, "mint a fresh PAT") {
		t.Errorf("an unreachable endpoint must not advise a credential rotation, got:\n%s", out)
	}
	if strings.Contains(out, "✗ not authenticated") {
		t.Errorf("an unreachable endpoint must not render a negative auth verdict, got:\n%s", out)
	}
	if strings.Contains(out, "✓ logged in") {
		t.Errorf("an unverified credential must not render a positive auth verdict either, got:\n%s", out)
	}
	if !strings.Contains(out, "could not verify") {
		t.Errorf("expected a \"could not verify\" state, got:\n%s", out)
	}
	if got := countEndpointBlocks(out); got != 1 {
		t.Errorf("expected exactly 1 Endpoint block, got %d:\n%s", got, out)
	}
}

// TestRenderAuthStatus_EnvUserTokenShowsIntrospectedIdentity: an env-sourced
// token carries a zero UserInfo (there was no login to learn a name from), so
// a principal_kind:"user" response carrying a real name and email still
// rendered the generic "identity resolved server-side per request". Knowing WHO
// a credential authenticates as is the stated reason status calls the
// introspection endpoint rather than a bare liveness check.
func TestRenderAuthStatus_EnvUserTokenShowsIntrospectedIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != auth.IntrospectEndpoint {
			t.Errorf("unexpected path %q, want %q", r.URL.Path, auth.IntrospectEndpoint)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"active": true,
			"principal_kind": "user",
			"scope": "full-access",
			"token_type": "Bearer",
			"expires_at": null,
			"user": {"id": "u_1", "email": "introspected@example.test", "name": "Introspected Person", "tier": "pro"},
			"team": null,
			"token": null
		}`))
	}))
	defer srv.Close()

	setupAuthRenderEnv(t, srv.URL, validPersonalToken)

	out := renderAuthStatus("/tmp/auth.json")

	if !strings.Contains(out, "Introspected Person") {
		t.Errorf("expected the introspected name in output, got:\n%s", out)
	}
	if !strings.Contains(out, "introspected@example.test") {
		t.Errorf("expected the introspected email in output, got:\n%s", out)
	}
	if strings.Contains(out, "identity resolved server-side per request") {
		t.Errorf("a user-principal response with a real identity must not fall into the generic blank-identity branch:\n%s", out)
	}
	if got := countEndpointBlocks(out); got != 1 {
		t.Errorf("expected exactly 1 Endpoint block, got %d:\n%s", got, out)
	}
}

// TestRenderAuthStatus_TeamServiceWithoutTeamID: a team-service response whose
// team object is null must still render a stable, non-empty "Acting as" line
// and must still name the token, because the token name is the only handle an
// operator has for finding this credential in a secret store.
func TestRenderAuthStatus_TeamServiceWithoutTeamID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"active": true,
			"principal_kind": "team-service",
			"token_type": "Bearer",
			"expires_at": null,
			"user": null,
			"team": null,
			"token": {"prefix": "oxt_ab12", "name": "nightly-indexer"}
		}`))
	}))
	defer srv.Close()

	setupAuthRenderEnv(t, srv.URL, validTeamToken)

	out := renderAuthStatus("/tmp/auth.json")

	if !strings.Contains(out, "(unknown team)") {
		t.Errorf("expected the stable (unknown team) fallback, got:\n%s", out)
	}
	if !strings.Contains(out, "nightly-indexer") {
		t.Errorf("expected the token name so the operator can identify the credential, got:\n%s", out)
	}
	if strings.Contains(out, "identity resolved server-side per request") {
		t.Errorf("a team-service response must not fall into the generic blank-identity branch:\n%s", out)
	}
}

// TestRenderAuthStatus_UnknownPrincipalKindKeepsTeamID: the server contract can
// grow a principal kind this binary predates, and an older ox must degrade
// honestly. Two failure modes to avoid: inventing a user identity we were never
// given, and silently dropping a team_id the server DID send — which would make
// status tell the operator strictly less than the response contained.
func TestRenderAuthStatus_UnknownPrincipalKindKeepsTeamID(t *testing.T) {
	for _, kind := range []string{"team_service", "", "service"} {
		t.Run("kind="+kind, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{
					"active": true,
					"principal_kind": "` + kind + `",
					"token_type": "Bearer",
					"expires_at": null,
					"user": null,
					"team": {"team_id": "team_future42"},
					"token": {"prefix": "oxt_ab12", "name": "future-kind"}
				}`))
			}))
			defer srv.Close()

			setupAuthRenderEnv(t, srv.URL, validTeamToken)

			out := renderAuthStatus("/tmp/auth.json")

			if !strings.Contains(out, "team_future42") {
				t.Errorf("a present team_id must never be silently dropped, got:\n%s", out)
			}
			if strings.Contains(out, "User") {
				t.Errorf("an unrecognized principal kind must not claim a user identity, got:\n%s", out)
			}
		})
	}
}

// TestRenderAuthStatus_TeamTokenActingAs is the red-first proof for the
// "acting as team <team_id>" rendering: before this change, an env-sourced
// team token fell into the generic "SAGEOX_TOKEN (env) — identity resolved
// server-side per request" branch (same as any personal PAT), which never
// told the user WHICH team the CLI was acting as. Reverting the switch in
// renderAuthStatus back to the old if/else on epToken.UserInfo reproduces
// that failure — this test would then fail on the "team_abc123" assertion.
func TestRenderAuthStatus_TeamTokenActingAs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != auth.IntrospectEndpoint {
			t.Errorf("unexpected path %q, want %q", r.URL.Path, auth.IntrospectEndpoint)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"active": true,
			"principal_kind": "team-service",
			"scope": "read-only",
			"token_type": "Bearer",
			"expires_at": null,
			"user": null,
			"team": {"team_id": "team_abc123"},
			"token": {"prefix": "oxt_ab12", "name": "ci-deploy"}
		}`))
	}))
	defer srv.Close()

	// validTeamToken is a real, correctly-checksummed value, so it clears the
	// local format check and reaches the mock introspection server above.
	setupAuthRenderEnv(t, srv.URL, validTeamToken)

	out := renderAuthStatus("/tmp/auth.json")

	if !strings.Contains(out, "Acting as") {
		t.Errorf("expected an \"Acting as\" line, got:\n%s", out)
	}
	if !strings.Contains(out, "team_abc123") {
		t.Errorf("expected the introspected team_id in output, got:\n%s", out)
	}
	if !strings.Contains(out, "ci-deploy") {
		t.Errorf("expected the introspected token name in output, got:\n%s", out)
	}
	if strings.Contains(out, "identity resolved server-side per request") {
		t.Errorf("team token fell into the generic blank-identity branch instead of the team-specific one:\n%s", out)
	}
	// The verdict line is what a human and the BDD status parser read to
	// decide whether the CLI is usable; without this the test passes under a
	// mutation that flips it.
	if !strings.Contains(out, "✓ logged in") {
		t.Errorf("an accepted token must render a positive endpoint verdict, got:\n%s", out)
	}
	if strings.Contains(out, "✗ not authenticated") {
		t.Errorf("an accepted token must not render a negative endpoint verdict, got:\n%s", out)
	}
	if got := countEndpointBlocks(out); got != 1 {
		t.Errorf("expected exactly 1 Endpoint block, got %d:\n%s", got, out)
	}
}

// TestRenderAuthStatus_RejectedTokenGetsFamilyAwareHint proves the rejected-
// credential hint is chosen by token FAMILY, in both directions. The single-row
// version of this test passed under an inverted HasPrefix condition, because
// only the oxt_ arm was ever exercised — an inversion just swapped which of the
// two arms was wrong, and no assertion looked at the other one.
//
// The two remedies are not interchangeable: `ox login` only ever mints a
// personal oxp_ token, and the PAT settings page cannot issue a team credential
// either, so pointing a team-token holder at them ends nowhere.
func TestRenderAuthStatus_RejectedTokenGetsFamilyAwareHint(t *testing.T) {
	tests := []struct {
		name       string
		envToken   string
		wantPhrase string
		denyPhrase string
	}{
		{
			name:       "team token gets CI-secret-store guidance",
			envToken:   validTeamToken,
			wantPhrase: "this looks like a team token",
			denyPhrase: "mint a fresh PAT",
		},
		{
			name:       "personal token gets PAT guidance",
			envToken:   validPersonalToken,
			wantPhrase: "mint a fresh PAT",
			denyPhrase: "this looks like a team token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer srv.Close()

			setupAuthRenderEnv(t, srv.URL, tt.envToken)

			out := renderAuthStatus("/tmp/auth.json")

			if !strings.Contains(out, tt.wantPhrase) {
				t.Errorf("expected %q in the hint, got:\n%s", tt.wantPhrase, out)
			}
			if strings.Contains(out, tt.denyPhrase) {
				t.Errorf("must not get the other family's hint (%q), got:\n%s", tt.denyPhrase, out)
			}
			if !strings.Contains(out, "ox login") {
				t.Errorf("expected the hint to mention `ox login`, got:\n%s", out)
			}
			// The verdict line, asserted in both directions: a mutation
			// flipping it passed this test before.
			if !strings.Contains(out, "✗ not authenticated") {
				t.Errorf("a rejected token must render a negative endpoint verdict, got:\n%s", out)
			}
			if strings.Contains(out, "✓ logged in") {
				t.Errorf("a rejected token must not render a positive endpoint verdict, got:\n%s", out)
			}
			if got := countEndpointBlocks(out); got != 1 {
				t.Errorf("expected exactly 1 Endpoint block, got %d:\n%s", got, out)
			}
		})
	}
}

// TestRenderAuthStatus_RejectedTeamTokenMustNotRecommendSettingTheVarAgain: we
// are on this line BECAUSE SAGEOX_TOKEN is set, so "set SAGEOX_TOKEN for CI/API
// use" is advice the operator already followed. A refused team token is rotated
// in the CI secret store, which is the only place it can be replaced.
func TestRenderAuthStatus_RejectedTeamTokenMustNotRecommendSettingTheVarAgain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	setupAuthRenderEnv(t, srv.URL, validTeamToken)

	out := renderAuthStatus("/tmp/auth.json")

	if strings.Contains(out, "set SAGEOX_TOKEN for CI/API use") {
		t.Errorf("hint tells the operator to do the thing they already did, got:\n%s", out)
	}
	if !strings.Contains(out, "secret store") {
		t.Errorf("expected the hint to point at the CI secret store, got:\n%s", out)
	}
}

// --- renderIdentity: the identity matrix, without a server ---
//
// renderIdentity is pure, so every cell of (env-sourced) × (introspect result)
// × (principal kind) × (team present) × (UserInfo populated) is reachable here
// for the cost of a struct literal. The integration-shaped tests above stay as
// proof that the wiring into renderAuthStatus is real.
func TestRenderIdentity(t *testing.T) {
	t.Parallel()

	human := &auth.StoredToken{UserInfo: auth.UserInfo{Name: "Ada Lovelace", Email: "ada@example.test"}}
	blank := &auth.StoredToken{}

	tests := []struct {
		name       string
		envSourced bool
		tok        *auth.StoredToken
		ir         *auth.IntrospectResult
		want       []string
		deny       []string
	}{
		{
			name: "disk login renders the stored identity",
			tok:  human,
			want: []string{"User", "Ada Lovelace", "ada@example.test"},
		},
		{
			name:       "env token with no introspection falls back to the credential line",
			envSourced: true,
			tok:        blank,
			want:       []string{"SAGEOX_TOKEN (env)", "identity resolved server-side per request"},
		},
		{
			name:       "team-service renders the team and the token name",
			envSourced: true,
			tok:        blank,
			ir: &auth.IntrospectResult{
				PrincipalKind: auth.PrincipalKindTeamService,
				Team:          &auth.IntrospectTeam{TeamID: "team_x"},
				Token:         &auth.IntrospectTokenInfo{Name: "ci-deploy"},
			},
			want: []string{"Acting as", "team team_x", "ci-deploy"},
			deny: []string{"identity resolved server-side per request"},
		},
		{
			name:       "team-service with a null team keeps a stable fallback",
			envSourced: true,
			tok:        blank,
			ir:         &auth.IntrospectResult{PrincipalKind: auth.PrincipalKindTeamService},
			want:       []string{"Acting as", "(unknown team)"},
		},
		{
			name:       "user principal renders the introspected identity",
			envSourced: true,
			tok:        blank,
			ir: &auth.IntrospectResult{
				PrincipalKind: auth.PrincipalKindUser,
				User:          &auth.IntrospectUser{Name: "Grace Hopper", Email: "grace@example.test"},
			},
			want: []string{"User", "Grace Hopper", "grace@example.test"},
			deny: []string{"identity resolved server-side per request"},
		},
		{
			name:       "user principal with only an email still names somebody",
			envSourced: true,
			tok:        blank,
			ir: &auth.IntrospectResult{
				PrincipalKind: auth.PrincipalKindUser,
				User:          &auth.IntrospectUser{Email: "only-email@example.test"},
			},
			want: []string{"User", "only-email@example.test"},
			deny: []string{"<>"},
		},
		{
			name:       "user principal with a blank user object degrades to the credential line",
			envSourced: true,
			tok:        blank,
			ir:         &auth.IntrospectResult{PrincipalKind: auth.PrincipalKindUser, User: &auth.IntrospectUser{}},
			want:       []string{"identity resolved server-side per request"},
		},
		{
			name:       "unrecognized principal kind keeps a present team_id",
			envSourced: true,
			tok:        blank,
			ir: &auth.IntrospectResult{
				PrincipalKind: "team_service",
				Team:          &auth.IntrospectTeam{TeamID: "team_future"},
			},
			want: []string{"team_future"},
			deny: []string{"User", "identity resolved server-side per request"},
		},
		{
			name: "introspection is ignored for a disk-sourced token",
			tok:  human,
			ir:   &auth.IntrospectResult{PrincipalKind: auth.PrincipalKindTeamService, Team: &auth.IntrospectTeam{TeamID: "team_x"}},
			want: []string{"Ada Lovelace"},
			deny: []string{"Acting as"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := renderIdentity(tt.envSourced, tt.tok, tt.ir)
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("expected %q in %q", want, got)
				}
			}
			for _, deny := range tt.deny {
				if strings.Contains(got, deny) {
					t.Errorf("did not expect %q in %q", deny, got)
				}
			}
		})
	}
}

// TestAuthFollowUpHint covers the hint that prints under the auth section. Two
// arms give demonstrably wrong advice without this logic: an offline developer
// with a good credential was told "token refresh failed: run `ox login` to
// re-authenticate" (a credential rotation prescribed for a network fault), and
// a CI operator with a truncated SAGEOX_TOKEN was pointed at `ox login`, which
// cannot mint their credential and which they cannot run.
func TestAuthFollowUpHint(t *testing.T) {
	t.Parallel()

	unreachable := fmt.Errorf("could not reach host to validate token: %w: %w",
		errors.New("dial tcp: connection refused"), auth.ErrEndpointUnreachable)

	tests := []struct {
		name          string
		envMalformed  bool
		authErr       error
		loggedInCount int
		want          authHint
	}{
		{"healthy login", false, nil, 1, authHintNone},
		{"no credential at all", false, nil, 0, authHintLogin},
		{"server rejected the token", false, errors.New("server rejected token (HTTP 401)"), 1, authHintRefreshFailed},
		{"endpoint unreachable is not a rejection", false, unreachable, 1, authHintUnreachable},
		{"unreachable with nothing logged in is still unreachable", false, unreachable, 0, authHintUnreachable},
		{"malformed env token suppresses the login advice", true, nil, 0, authHintNone},
		{"malformed env token wins over a reported error", true, errors.New("boom"), 0, authHintNone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := authFollowUpHint(tt.envMalformed, tt.authErr, tt.loggedInCount); got != tt.want {
				t.Errorf("authFollowUpHint(%v, %v, %d) = %v, want %v",
					tt.envMalformed, tt.authErr, tt.loggedInCount, got, tt.want)
			}
		})
	}
}

// --- doctor: `ox login` is the wrong remedy for a refused SAGEOX_TOKEN ---
//
// These live here rather than beside checkAuthentication because this file owns
// the malformed-env-token behavior under test.

// TestDoctorAuth_MalformedEnvTokenIsNotReportedAsNotLoggedIn: with a truncated
// SAGEOX_TOKEN, doctor rendered "NOT LOGGED IN → Run `ox login`". For a team
// token that remedy is not merely useless — `ox login` mints a personal oxp_
// credential, so following it silently changes which principal the CLI acts as,
// which is the failure mode doctor exists to catch.
//
// Once the credential resolver began surfacing the refusal as an error, the
// symptom moved rather than disappearing: doctor then reported a generic
// "check failed" with the raw wrapped error pasted in, and still offered no
// family-aware remedy. Both shapes are asserted against.
func TestDoctorAuth_MalformedEnvTokenIsNotReportedAsNotLoggedIn(t *testing.T) {
	tests := []struct {
		name       string
		envToken   string
		wantPhrase string
	}{
		{"team token", malformedTeamToken, "CI secret store"},
		{"personal token", malformedPersonalPAT, "unset " + auth.EnvVarToken},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupAuthRenderEnv(t, "http://127.0.0.1:1", tt.envToken)

			res := checkAuthentication()

			if strings.Contains(res.message, "NOT LOGGED IN") {
				t.Fatalf("a refused %s must not be reported as an ordinary absence, got message=%q detail=%q",
					auth.EnvVarToken, res.message, res.detail)
			}
			if !strings.Contains(res.message, auth.EnvVarToken) {
				t.Errorf("message must name %s, got %q", auth.EnvVarToken, res.message)
			}
			if !strings.Contains(res.detail, tt.wantPhrase) {
				t.Errorf("expected family-aware remedy containing %q, got %q", tt.wantPhrase, res.detail)
			}
			if res.passed {
				t.Errorf("a refused credential must not pass the check: %+v", res)
			}
		})
	}
}
