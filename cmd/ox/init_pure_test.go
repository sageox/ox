package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractUUIDSuffix_Pure(t *testing.T) {
	tests := []struct {
		name   string
		repoID string
		want   string
	}{
		{name: "standard repo id", repoID: "repo_01jfk3mab123", want: "01jfk3mab123"},
		{name: "no prefix", repoID: "01jfk3mab123", want: "01jfk3mab123"},
		{name: "empty string", repoID: "", want: ""},
		{name: "just prefix", repoID: "repo_", want: ""},
		{name: "double prefix", repoID: "repo_repo_abc", want: "repo_abc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractUUIDSuffix(tt.repoID)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDisplayPath(t *testing.T) {
	tests := []struct {
		name    string
		absPath string
		want    string
	}{
		{
			name:    "sageox config file",
			absPath: "/home/user/project/.sageox/config.json",
			want:    ".sageox/config.json",
		},
		{
			name:    "sageox marker file",
			absPath: "/home/user/project/.sageox/.repo_abc123",
			want:    ".sageox/.repo_abc123",
		},
		{
			name:    "root-level file",
			absPath: "/home/user/project/AGENTS.md",
			want:    "AGENTS.md",
		},
		{
			name:    "nested non-sageox file",
			absPath: "/home/user/project/src/main.go",
			want:    "main.go",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := displayPath(tt.absPath)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGenerateSageoxLinksSection(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.ProjectConfig
		want string
	}{
		{
			name: "nil config",
			cfg:  nil,
			want: "",
		},
		{
			name: "empty config",
			cfg:  &config.ProjectConfig{},
			want: "",
		},
		{
			name: "repo id only",
			cfg:  &config.ProjectConfig{RepoID: "repo_abc123", Endpoint: "https://sageox.ai"},
			want: "## SageOx Links\n\n- **Repository Dashboard:** https://sageox.ai/repo/repo_abc123\n",
		},
		{
			name: "team id only",
			cfg:  &config.ProjectConfig{TeamID: "team_xyz789", Endpoint: "https://sageox.ai"},
			want: "## SageOx Links\n\n- **Team Dashboard:** https://sageox.ai/team/team_xyz789\n",
		},
		{
			name: "both ids",
			cfg:  &config.ProjectConfig{RepoID: "repo_abc123", TeamID: "team_xyz789", Endpoint: "https://sageox.ai"},
			want: "## SageOx Links\n\n- **Repository Dashboard:** https://sageox.ai/repo/repo_abc123\n- **Team Dashboard:** https://sageox.ai/team/team_xyz789\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := generateSageoxLinksSection(tt.cfg)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestEnsureSageoxConfig_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	requireSageoxDir(t, tmpDir)

	// first call creates
	result1 := ensureSageoxConfig(tmpDir)
	assert.Equal(t, configCreated, result1)

	// second call preserves
	result2 := ensureSageoxConfig(tmpDir)
	assert.Equal(t, configPreserved, result2)

	// config should be valid
	cfg, err := config.LoadProjectConfig(tmpDir)
	require.NoError(t, err)
	assert.Equal(t, config.CurrentConfigVersion, cfg.ConfigVersion)
}

func TestEnsureSageoxConfig_CorruptJSON(t *testing.T) {
	tmpDir := t.TempDir()
	requireSageoxDir(t, tmpDir)

	// write corrupt JSON
	configPath := filepath.Join(tmpDir, ".sageox", "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{broken"), 0644))

	// should recreate
	result := ensureSageoxConfig(tmpDir)
	assert.Equal(t, configCreated, result)

	// config should now be valid
	cfg, err := config.LoadProjectConfig(tmpDir)
	require.NoError(t, err)
	assert.Equal(t, config.CurrentConfigVersion, cfg.ConfigVersion)
}

func TestDetectRepoMarkersForEndpoint(t *testing.T) {
	tests := []struct {
		name           string
		markers        []repoMarker
		targetEndpoint string
		wantCount      int
		wantIDs        []string
	}{
		{
			name:           "empty directory",
			markers:        nil,
			targetEndpoint: "https://sageox.ai",
			wantCount:      0,
		},
		{
			name: "matching endpoint",
			markers: []repoMarker{
				{RepoID: "repo_abc", Endpoint: "https://sageox.ai"},
			},
			targetEndpoint: "https://sageox.ai",
			wantCount:      1,
			wantIDs:        []string{"repo_abc"},
		},
		{
			name: "non-matching endpoint",
			markers: []repoMarker{
				{RepoID: "repo_abc", Endpoint: "https://staging.sageox.ai"},
			},
			targetEndpoint: "https://sageox.ai",
			wantCount:      0,
		},
		{
			name: "mixed endpoints",
			markers: []repoMarker{
				{RepoID: "repo_aaa", Endpoint: "https://sageox.ai"},
				{RepoID: "repo_bbb", Endpoint: "https://staging.sageox.ai"},
				{RepoID: "repo_ccc", Endpoint: "https://sageox.ai"},
			},
			targetEndpoint: "https://sageox.ai",
			wantCount:      2,
			wantIDs:        []string{"repo_aaa", "repo_ccc"},
		},
		{
			name: "sorted by repo id",
			markers: []repoMarker{
				{RepoID: "repo_zzz", Endpoint: "https://sageox.ai"},
				{RepoID: "repo_aaa", Endpoint: "https://sageox.ai"},
			},
			targetEndpoint: "https://sageox.ai",
			wantCount:      2,
			wantIDs:        []string{"repo_aaa", "repo_zzz"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sageoxDir := filepath.Join(t.TempDir(), ".sageox")
			require.NoError(t, os.MkdirAll(sageoxDir, 0755))

			for _, m := range tt.markers {
				uuid := extractUUIDSuffix(m.RepoID)
				data, _ := json.Marshal(m)
				require.NoError(t, os.WriteFile(
					filepath.Join(sageoxDir, ".repo_"+uuid),
					data, 0644,
				))
			}

			ids, err := detectRepoMarkersForEndpoint(sageoxDir, tt.targetEndpoint)
			require.NoError(t, err)
			assert.Len(t, ids, tt.wantCount)
			if tt.wantIDs != nil {
				assert.Equal(t, tt.wantIDs, ids)
			}
		})
	}
}

func TestDetectRepoMarkersForEndpoint_MissingDir(t *testing.T) {
	ids, err := detectRepoMarkersForEndpoint("/nonexistent/path/.sageox", "https://sageox.ai")
	require.NoError(t, err)
	assert.Nil(t, ids)
}

func TestDetectRepoMarkersForEndpoint_SkipsMalformedJSON(t *testing.T) {
	sageoxDir := filepath.Join(t.TempDir(), ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	// write a malformed marker
	require.NoError(t, os.WriteFile(
		filepath.Join(sageoxDir, ".repo_bad"),
		[]byte("{not json}"),
		0644,
	))
	// write a valid marker
	data, _ := json.Marshal(repoMarker{RepoID: "repo_good", Endpoint: "https://sageox.ai"})
	require.NoError(t, os.WriteFile(
		filepath.Join(sageoxDir, ".repo_good"),
		data,
		0644,
	))

	ids, err := detectRepoMarkersForEndpoint(sageoxDir, "https://sageox.ai")
	require.NoError(t, err)
	assert.Equal(t, []string{"repo_good"}, ids)
}

func TestUpdateSageoxReadmeWithLinks(t *testing.T) {
	t.Run("nil config is no-op", func(t *testing.T) {
		err := updateSageoxReadmeWithLinks("/nonexistent", nil)
		assert.NoError(t, err)
	})

	t.Run("skips if links section already exists", func(t *testing.T) {
		tmpFile := filepath.Join(t.TempDir(), "README.md")
		content := "# SageOx\n\n## SageOx Links\n\n- existing\n"
		require.NoError(t, os.WriteFile(tmpFile, []byte(content), 0644))

		cfg := &config.ProjectConfig{RepoID: "repo_new", TeamID: "team_new"}
		require.NoError(t, updateSageoxReadmeWithLinks(tmpFile, cfg))

		data, _ := os.ReadFile(tmpFile)
		// should not contain the new repo_id
		assert.NotContains(t, string(data), "repo_new")
	})

	t.Run("inserts after separator", func(t *testing.T) {
		tmpFile := filepath.Join(t.TempDir(), "README.md")
		content := "# SageOx\n\n---\n\n## For AI Coworkers\n"
		require.NoError(t, os.WriteFile(tmpFile, []byte(content), 0644))

		cfg := &config.ProjectConfig{RepoID: "repo_test123"}
		require.NoError(t, updateSageoxReadmeWithLinks(tmpFile, cfg))

		data, _ := os.ReadFile(tmpFile)
		assert.Contains(t, string(data), "## SageOx Links")
		assert.Contains(t, string(data), "repo_test123")
	})

	t.Run("appends when no separator", func(t *testing.T) {
		tmpFile := filepath.Join(t.TempDir(), "README.md")
		content := "# SageOx\n\nNo separator here.\n"
		require.NoError(t, os.WriteFile(tmpFile, []byte(content), 0644))

		cfg := &config.ProjectConfig{TeamID: "team_abc"}
		require.NoError(t, updateSageoxReadmeWithLinks(tmpFile, cfg))

		data, _ := os.ReadFile(tmpFile)
		assert.Contains(t, string(data), "## SageOx Links")
		assert.Contains(t, string(data), "team_abc")
	})
}

func TestStringSlicesEqual_TableDriven(t *testing.T) {
	tests := []struct {
		name string
		a    []string
		b    []string
		want bool
	}{
		{name: "both nil", a: nil, b: nil, want: true},
		{name: "nil vs empty", a: nil, b: []string{}, want: true},
		{name: "empty vs nil", a: []string{}, b: nil, want: true},
		{name: "both empty", a: []string{}, b: []string{}, want: true},
		{name: "equal", a: []string{"x", "y"}, b: []string{"x", "y"}, want: true},
		{name: "different order", a: []string{"x", "y"}, b: []string{"y", "x"}, want: false},
		{name: "different length", a: []string{"x"}, b: []string{"x", "y"}, want: false},
		{name: "nil vs nonempty", a: nil, b: []string{"x"}, want: false},
		{name: "single element equal", a: []string{"a"}, b: []string{"a"}, want: true},
		{name: "single element diff", a: []string{"a"}, b: []string{"b"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, stringSlicesEqual(tt.a, tt.b))
		})
	}
}

// TestFormatTeamLabel_Pure covers the team label shown on init's success line.
// Failure prevented: the outcome line reports an opaque team ID, so a user with
// more than one team cannot tell which team the repo was just bound to.
func TestFormatTeamLabel_Pure(t *testing.T) {
	tests := []struct {
		name     string
		teamName string
		teamID   string
		want     string
	}{
		{
			name:     "named team leads with the name",
			teamName: "Platform",
			teamID:   "team_abc123",
			want:     "Platform (team_abc123)",
		},
		{
			name:     "no name falls back to the bare id",
			teamName: "",
			teamID:   "team_abc123",
			want:     "team_abc123",
		},
		{
			name:     "whitespace-only name is treated as absent",
			teamName: "   ",
			teamID:   "team_abc123",
			want:     "team_abc123",
		},
		{
			name:     "name containing spaces is preserved",
			teamName: "Developer Experience",
			teamID:   "team_xyz789",
			want:     "Developer Experience (team_xyz789)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTeamLabel(tt.teamName, tt.teamID)
			assert.Equal(t, tt.want, got)
		})
	}
}
