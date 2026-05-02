package badge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindReadme(t *testing.T) {
	tests := []struct {
		name       string
		files      []string
		wantExists bool
	}{
		{
			name:       "finds README.md",
			files:      []string{"README.md"},
			wantExists: true,
		},
		{
			name:       "finds readme.md (lowercase)",
			files:      []string{"readme.md"},
			wantExists: true,
		},
		{
			name:       "finds Readme.md",
			files:      []string{"Readme.md"},
			wantExists: true,
		},
		{
			name:       "no readme found",
			files:      []string{"other.txt"},
			wantExists: false,
		},
		{
			name:       "empty directory",
			files:      []string{},
			wantExists: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			// create test files
			for _, f := range tt.files {
				path := filepath.Join(tmpDir, f)
				require.NoError(t, os.WriteFile(path, []byte("# Test"), 0644), "failed to create test file")
			}

			got := FindReadme(tmpDir)

			if tt.wantExists {
				assert.NotEmpty(t, got, "FindReadme() = empty, want a readme file")
				if got != "" {
					// verify the returned file actually exists
					_, err := os.Stat(got)
					assert.False(t, os.IsNotExist(err), "FindReadme() returned non-existent file: %s", got)
				}
			} else {
				assert.Empty(t, got, "FindReadme() = %s, want empty", got)
			}
		})
	}
}

func TestFindReadmePreference(t *testing.T) {
	// test that README.md is preferred over other variants
	tmpDir := t.TempDir()

	// create both files
	for _, f := range []string{"README.md", "readme.md"} {
		path := filepath.Join(tmpDir, f)
		require.NoError(t, os.WriteFile(path, []byte("# Test"), 0644), "failed to create test file")
	}

	got := FindReadme(tmpDir)
	require.NotEmpty(t, got, "FindReadme() returned empty")

	// README.md should be preferred (it comes first in the candidate list)
	assert.Equal(t, "README.md", filepath.Base(got), "FindReadme() should prefer uppercase README.md")
}

func TestHasBadge(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "has full badge markdown",
			content: "# My Project\n\n" + BadgeMarkdown + "\n\nSome content",
			want:    true,
		},
		{
			name:    "has SageOx reference",
			content: "# My Project\n\nPowered by SageOx\n",
			want:    true,
		},
		{
			name:    "has sageox lowercase",
			content: "# My Project\n\nUsing sageox for infra\n",
			want:    true,
		},
		{
			name:    "has sageox/ox link",
			content: "# My Project\n\n[link](https://github.com/sageox/ox)\n",
			want:    true,
		},
		{
			name:    "no badge present",
			content: "# My Project\n\nSome content\n",
			want:    false,
		},
		{
			name:    "empty file",
			content: "",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			readmePath := filepath.Join(tmpDir, "README.md")

			require.NoError(t, os.WriteFile(readmePath, []byte(tt.content), 0644), "failed to create test file")

			assert.Equal(t, tt.want, HasBadge(readmePath), "HasBadge()")
		})
	}
}

func TestInjectBadge(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantContain string
	}{
		{
			name:        "injects after heading",
			content:     "# My Project\n\nSome content",
			wantContain: BadgeMarkdown,
		},
		{
			name:        "injects after existing badges",
			content:     "# My Project\n\n[![Build](https://example.com/badge.svg)](https://example.com)\n\nContent",
			wantContain: BadgeMarkdown,
		},
		{
			name:        "handles empty file",
			content:     "",
			wantContain: BadgeMarkdown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			readmePath := filepath.Join(tmpDir, "README.md")

			require.NoError(t, os.WriteFile(readmePath, []byte(tt.content), 0644), "failed to create test file")

			require.NoError(t, InjectBadge(readmePath), "InjectBadge() error")

			content, err := os.ReadFile(readmePath)
			require.NoError(t, err, "failed to read result")

			assert.Contains(t, string(content), tt.wantContain, "InjectBadge() result does not contain badge")
		})
	}
}

func TestFindBadgeInsertionPoint(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  int
	}{
		{
			name:  "after heading",
			lines: []string{"# Title", "", "Content"},
			want:  1,
		},
		{
			name:  "after existing badge",
			lines: []string{"# Title", "[![Badge](url)](link)", "", "Content"},
			want:  2,
		},
		{
			name:  "after multiple badges",
			lines: []string{"# Title", "[![Badge1](url1)](link1)", "[![Badge2](url2)](link2)", "", "Content"},
			want:  3,
		},
		{
			name:  "empty file",
			lines: []string{},
			want:  0,
		},
		{
			name:  "no heading",
			lines: []string{"Just content", "More content"},
			want:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, findBadgeInsertionPoint(tt.lines), "findBadgeInsertionPoint()")
		})
	}
}

func TestBadgeMarkdown(t *testing.T) {
	// verify badge contains expected elements
	assert.Contains(t, BadgeMarkdown, "powered%20by-SageOx", "BadgeMarkdown should contain 'powered by SageOx'")
	assert.Contains(t, BadgeMarkdown, "7A8F78", "BadgeMarkdown should contain SageOx brand color")
	assert.Contains(t, BadgeMarkdown, "github.com/sageox/ox", "BadgeMarkdown should link to ox repo")
}

func TestFindReadme_EmptyGitRoot(t *testing.T) {
	assert.Empty(t, FindReadme(""), "FindReadme with empty gitRoot should return empty")
}

func TestFindReadme_AllCandidates(t *testing.T) {
	// verify each candidate filename is individually discoverable
	for _, candidate := range readmeCandidates {
		t.Run(candidate, func(t *testing.T) {
			tmpDir := t.TempDir()
			path := filepath.Join(tmpDir, candidate)
			require.NoError(t, os.WriteFile(path, []byte("# Test"), 0644))

			got := FindReadme(tmpDir)
			// on case-insensitive filesystems (macOS), multiple candidates may match
			// just verify we found something in the right directory
			assert.NotEmpty(t, got, "should find a readme for candidate %s", candidate)
			assert.Equal(t, tmpDir, filepath.Dir(got), "found readme should be in the right directory")
		})
	}
}

func TestHasBadge_EmptyPath(t *testing.T) {
	assert.False(t, HasBadge(""), "HasBadge with empty path should return false")
}

func TestHasBadge_NonexistentFile(t *testing.T) {
	assert.False(t, HasBadge("/nonexistent/path/README.md"), "HasBadge with missing file should return false")
}

func TestHasBadge_UnreadableFile(t *testing.T) {
	tmpDir := t.TempDir()
	readmePath := filepath.Join(tmpDir, "README.md")
	require.NoError(t, os.WriteFile(readmePath, []byte("# Test"), 0000))

	// on non-root systems, 0000 permissions prevent reading
	got := HasBadge(readmePath)
	// either false (can't read) or true (root can read anything) - just shouldn't panic
	_ = got
}

func TestHasBadge_CaseInsensitivity(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "uppercase SAGEOX",
			content: "# Project\n\nUsing SAGEOX\n",
			want:    true,
		},
		{
			name:    "mixed case SaGeOx",
			content: "# Project\n\nSaGeOx reference\n",
			want:    true,
		},
		{
			name:    "url-encoded pattern lowercase",
			content: "# Project\n\npowered%20by-sageox badge\n",
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			readmePath := filepath.Join(tmpDir, "README.md")
			require.NoError(t, os.WriteFile(readmePath, []byte(tt.content), 0644))

			assert.Equal(t, tt.want, HasBadge(readmePath))
		})
	}
}

func TestInjectBadge_NonexistentFile(t *testing.T) {
	err := InjectBadge("/nonexistent/path/README.md")
	assert.Error(t, err, "InjectBadge on missing file should error")
}

func TestInjectBadge_PreservesExistingContent(t *testing.T) {
	tmpDir := t.TempDir()
	readmePath := filepath.Join(tmpDir, "README.md")
	original := "# My Project\n\nSome content here\n\nMore stuff"
	require.NoError(t, os.WriteFile(readmePath, []byte(original), 0644))

	require.NoError(t, InjectBadge(readmePath))

	content, err := os.ReadFile(readmePath)
	require.NoError(t, err)

	result := string(content)
	assert.Contains(t, result, BadgeMarkdown, "should contain badge")
	assert.Contains(t, result, "Some content here", "should preserve original content")
	assert.Contains(t, result, "More stuff", "should preserve all original content")
}

func TestInjectBadge_HeadingImmediatelyFollowedByContent(t *testing.T) {
	// exercises the branch where insertAt < len(lines) && lines[insertAt] is non-empty
	tmpDir := t.TempDir()
	readmePath := filepath.Join(tmpDir, "README.md")
	// no blank line between heading and content
	original := "# Title\nContent right after heading"
	require.NoError(t, os.WriteFile(readmePath, []byte(original), 0644))

	require.NoError(t, InjectBadge(readmePath))

	content, err := os.ReadFile(readmePath)
	require.NoError(t, err)
	result := string(content)

	assert.Contains(t, result, BadgeMarkdown)
	assert.Contains(t, result, "Content right after heading")

	// badge should be between title and content, separated by blank lines
	lines := strings.Split(result, "\n")
	badgeIdx := -1
	for i, l := range lines {
		if strings.Contains(l, "powered%20by-SageOx") {
			badgeIdx = i
			break
		}
	}
	require.Greater(t, badgeIdx, 0, "badge should be after heading")
	// blank line should separate badge from the following content
	assert.Empty(t, strings.TrimSpace(lines[badgeIdx+1]), "blank line expected after badge before content")
}

func TestInjectBadge_IdempotenceCheck(t *testing.T) {
	// injecting badge twice should result in two badges (InjectBadge doesn't check for dupes)
	// but the content should not be corrupted
	tmpDir := t.TempDir()
	readmePath := filepath.Join(tmpDir, "README.md")
	require.NoError(t, os.WriteFile(readmePath, []byte("# Project\n\nContent"), 0644))

	require.NoError(t, InjectBadge(readmePath))
	require.NoError(t, InjectBadge(readmePath))

	content, err := os.ReadFile(readmePath)
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(string(content), "powered%20by-SageOx"),
		"second inject places another badge (caller should use HasBadge to guard)")
}

func TestFindBadgeInsertionPoint_EdgeCases(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  int
	}{
		{
			name:  "heading only, no content after",
			lines: []string{"# Title"},
			want:  1,
		},
		{
			name:  "heading with image (not badge link syntax)",
			lines: []string{"# Title", "![screenshot](img.png)", "Content"},
			want:  2,
		},
		{
			name:  "heading with mixed badges and images",
			lines: []string{"# Title", "[![CI](ci.svg)](link)", "![alt](img.png)", "", "Content"},
			want:  3,
		},
		{
			name:  "multiple headings, only first matters",
			lines: []string{"# Title", "## Subtitle", "Content"},
			want:  1, // insert after first heading, ## is non-badge non-empty content
		},
		{
			name:  "heading then empty lines then content",
			lines: []string{"# Title", "", "", "Content"},
			want:  1,
		},
		{
			name:  "single empty line",
			lines: []string{""},
			want:  0,
		},
		{
			name:  "badges with blank lines between them",
			lines: []string{"# Title", "[![B1](u1)](l1)", "", "[![B2](u2)](l2)", "Content"},
			// empty lines don't stop scanning; second badge is still found
			want: 4, // after second badge
		},
		{
			name:  "whitespace-only lines between heading and content",
			lines: []string{"# Title", "   ", "\t", "Content"},
			want:  1, // whitespace lines are treated as empty, Content stops the scan
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, findBadgeInsertionPoint(tt.lines))
		})
	}
}

func TestStatusConstants(t *testing.T) {
	// verify status constants have expected values (guards against accidental changes)
	assert.Equal(t, "", StatusNotAsked)
	assert.Equal(t, "added", StatusAdded)
	assert.Equal(t, "declined", StatusDeclined)
}

func TestMarkNotNow(t *testing.T) {
	// MarkNotNow is intentionally a no-op; verify it doesn't panic
	MarkNotNow("")
	MarkNotNow("/some/path")
}

// setupTestEnv sets XDG_CONFIG_HOME and SAGEOX_ENDPOINT to isolate
// auth/config from the real user environment. Returns a cleanup function.
func setupTestEnv(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()

	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	t.Setenv("OX_XDG_DISABLE", "") // ensure XDG mode
	t.Setenv("OX_USER_CONFIG", "") // don't override user config path
	t.Setenv("SAGEOX_ENDPOINT", "https://test.sageox.ai")

	return tmpDir
}

// writeAuthToken writes a valid auth token to the test config directory.
func writeAuthToken(t *testing.T, configHome string, ep string, accessToken string) {
	t.Helper()
	authDir := filepath.Join(configHome, "sageox")
	require.NoError(t, os.MkdirAll(authDir, 0755))

	store := auth.AuthStore{
		Tokens: map[string]*auth.StoredToken{
			ep: {
				AccessToken: accessToken,
				ExpiresAt:   time.Now().Add(time.Hour),
				TokenType:   "bearer",
			},
		},
	}
	data, err := json.Marshal(store)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(authDir, "auth.json"), data, 0600))
}

// writeUserConfig writes a user config YAML to the test config directory.
func writeUserConfig(t *testing.T, configHome string, cfg *config.UserConfig) {
	t.Helper()
	configDir := filepath.Join(configHome, "sageox")
	require.NoError(t, os.MkdirAll(configDir, 0755))

	// use OX_USER_CONFIG to point directly at our file
	configPath := filepath.Join(configDir, "config.yaml")
	t.Setenv("OX_USER_CONFIG", configPath)

	// manually write YAML since we can't import gopkg.in/yaml.v3 here
	// without adding a dependency. Write a minimal YAML by hand.
	var content string
	if cfg.Badge != nil {
		content = "badge:\n"
		if cfg.Badge.SuggestionStatus != "" {
			content += "  suggestion_status: " + cfg.Badge.SuggestionStatus + "\n"
		}
	}
	require.NoError(t, os.WriteFile(configPath, []byte(content), 0644))
}

// createProjectWithReadme creates a temp dir with .sageox/config.json and a README.
func createProjectWithReadme(t *testing.T, readmeContent string, badgeStatus string) string {
	t.Helper()
	projectDir := t.TempDir()

	// create README
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "README.md"), []byte(readmeContent), 0644))

	// create .sageox/config.json
	sageoxDir := filepath.Join(projectDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	cfg := config.ProjectConfig{
		ConfigVersion: "2",
		BadgeStatus:   badgeStatus,
	}
	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), data, 0600))

	return projectDir
}

func TestShouldSuggest_NotAuthenticated(t *testing.T) {
	setupTestEnv(t)
	// no auth token written -> not authenticated
	projectDir := createProjectWithReadme(t, "# Project\n\nContent", "")
	writeUserConfig(t, os.Getenv("XDG_CONFIG_HOME"), &config.UserConfig{})

	assert.False(t, ShouldSuggest(projectDir), "should not suggest when not authenticated")
}

func TestShouldSuggest_GloballyDeclined(t *testing.T) {
	configHome := setupTestEnv(t)
	ep := "https://test.sageox.ai"
	writeAuthToken(t, configHome, ep, "test-token")

	writeUserConfig(t, configHome, &config.UserConfig{
		Badge: &config.BadgeConfig{
			SuggestionStatus: StatusDeclined,
		},
	})

	projectDir := createProjectWithReadme(t, "# Project\n\nContent", "")
	assert.False(t, ShouldSuggest(projectDir), "should not suggest when globally declined")
}

func TestShouldSuggest_ProjectDeclined(t *testing.T) {
	configHome := setupTestEnv(t)
	ep := "https://test.sageox.ai"
	writeAuthToken(t, configHome, ep, "test-token")
	writeUserConfig(t, configHome, &config.UserConfig{})

	projectDir := createProjectWithReadme(t, "# Project\n\nContent", StatusDeclined)
	assert.False(t, ShouldSuggest(projectDir), "should not suggest when project declined")
}

func TestShouldSuggest_ProjectAlreadyAdded(t *testing.T) {
	configHome := setupTestEnv(t)
	ep := "https://test.sageox.ai"
	writeAuthToken(t, configHome, ep, "test-token")
	writeUserConfig(t, configHome, &config.UserConfig{})

	projectDir := createProjectWithReadme(t, "# Project\n\nContent", StatusAdded)
	assert.False(t, ShouldSuggest(projectDir), "should not suggest when project already has badge status added")
}

func TestShouldSuggest_BadgeAlreadyInReadme(t *testing.T) {
	configHome := setupTestEnv(t)
	ep := "https://test.sageox.ai"
	writeAuthToken(t, configHome, ep, "test-token")
	writeUserConfig(t, configHome, &config.UserConfig{})

	// README already mentions sageox
	projectDir := createProjectWithReadme(t, "# Project\n\n"+BadgeMarkdown+"\n\nContent", "")
	assert.False(t, ShouldSuggest(projectDir), "should not suggest when badge already in README")
}

func TestShouldSuggest_NoReadme(t *testing.T) {
	configHome := setupTestEnv(t)
	ep := "https://test.sageox.ai"
	writeAuthToken(t, configHome, ep, "test-token")
	writeUserConfig(t, configHome, &config.UserConfig{})

	// project dir without README
	projectDir := t.TempDir()
	sageoxDir := filepath.Join(projectDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	cfg := config.ProjectConfig{ConfigVersion: "2"}
	data, _ := json.Marshal(cfg)
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), data, 0600))

	assert.False(t, ShouldSuggest(projectDir), "should not suggest when no README exists")
}

func TestShouldSuggest_EmptyGitRoot(t *testing.T) {
	configHome := setupTestEnv(t)
	ep := "https://test.sageox.ai"
	writeAuthToken(t, configHome, ep, "test-token")
	writeUserConfig(t, configHome, &config.UserConfig{})

	// empty gitRoot -> should return true since project/readme checks are skipped
	got := ShouldSuggest("")
	assert.True(t, got, "empty gitRoot skips project/readme checks, should suggest if authed")
}

func TestShouldSuggest_AllConditionsMet(t *testing.T) {
	configHome := setupTestEnv(t)
	ep := "https://test.sageox.ai"
	writeAuthToken(t, configHome, ep, "test-token")
	writeUserConfig(t, configHome, &config.UserConfig{})

	projectDir := createProjectWithReadme(t, "# My Project\n\nSome content\n", "")
	assert.True(t, ShouldSuggest(projectDir), "should suggest when all conditions are met")
}

func TestMarkDeclined_UpdatesProjectConfig(t *testing.T) {
	configHome := setupTestEnv(t)
	writeUserConfig(t, configHome, &config.UserConfig{})

	projectDir := createProjectWithReadme(t, "# Project\n", "")

	MarkDeclined(projectDir)

	// verify project config was updated
	projectCfg, err := config.LoadProjectConfig(projectDir)
	require.NoError(t, err)
	assert.Equal(t, StatusDeclined, projectCfg.BadgeStatus)
}

func TestMarkDeclined_EmptyGitRoot(t *testing.T) {
	configHome := setupTestEnv(t)
	writeUserConfig(t, configHome, &config.UserConfig{})

	// should not panic with empty gitRoot
	MarkDeclined("")
}

func TestMarkAdded_UpdatesProjectConfig(t *testing.T) {
	configHome := setupTestEnv(t)
	writeUserConfig(t, configHome, &config.UserConfig{})

	projectDir := createProjectWithReadme(t, "# Project\n", "")

	MarkAdded(projectDir)

	projectCfg, err := config.LoadProjectConfig(projectDir)
	require.NoError(t, err)
	assert.Equal(t, StatusAdded, projectCfg.BadgeStatus)
}

func TestMarkAdded_EmptyGitRoot(t *testing.T) {
	configHome := setupTestEnv(t)
	writeUserConfig(t, configHome, &config.UserConfig{})

	// should not panic with empty gitRoot
	MarkAdded("")
}
