package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
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
				LastSync:         time.Now().Add(-30 * time.Second),
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
			text, semantic := status.FormatGitRepoStatus(tt.status)
			assert.Contains(t, text, tt.wantText)
			assert.Equal(t, tt.wantSemantic, semantic)
		})
	}
}

func TestFormatTimeAgo(t *testing.T) {
	t.Parallel()

	now := time.Now()
	tests := []struct {
		name string
		t    time.Time
		want string
	}{
		{"just now", now.Add(-5 * time.Second), "just now"},
		{"1 minute ago", now.Add(-1 * time.Minute), "1 minute ago"},
		{"multiple minutes", now.Add(-15 * time.Minute), "15 minutes ago"},
		{"1 hour ago", now.Add(-1 * time.Hour), "1 hour ago"},
		{"multiple hours", now.Add(-5 * time.Hour), "5 hours ago"},
		{"1 day ago", now.Add(-25 * time.Hour), "1 day ago"},
		{"multiple days", now.Add(-72 * time.Hour), "3 days ago"},
		{"1 week ago", now.Add(-8 * 24 * time.Hour), "1 week ago"},
		{"multiple weeks", now.Add(-21 * 24 * time.Hour), "3 weeks ago"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := status.FormatTimeAgo(tt.t)
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
