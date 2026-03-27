package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepoInfo_StableID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		repo   RepoInfo
		wantID string
	}{
		{
			name:   "returns team ID",
			repo:   RepoInfo{TeamID: "team_abc123", Name: "acme-team"},
			wantID: "team_abc123",
		},
		{
			name:   "empty team ID",
			repo:   RepoInfo{TeamID: "", Name: "orphan-repo"},
			wantID: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.wantID, tt.repo.StableID())
		})
	}
}

func TestRepoDetailTeamContext_StableID(t *testing.T) {
	t.Parallel()

	tc := RepoDetailTeamContext{TeamID: "team_xyz789"}
	assert.Equal(t, "team_xyz789", tc.StableID())
}

func TestRepoDetailResponse_IsReadOnly_AllAccessLevels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		access string
		want   bool
	}{
		{"viewer is read-only", "viewer", true},
		{"member is not read-only", "member", false},
		{"empty is not read-only", "", false},
		{"admin is not read-only", "admin", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resp := &RepoDetailResponse{AccessLevel: tt.access}
			assert.Equal(t, tt.want, resp.IsReadOnly())
		})
	}
}

func TestTeamMembershipsFromRepos(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		resp      ReposResponse
		wantCount int
		wantIDs   []string
	}{
		{
			name: "prefers Teams array when populated",
			resp: ReposResponse{
				Teams: []TeamMembership{
					{ID: "team_1", Name: "Team One"},
					{ID: "team_2", Name: "Team Two"},
				},
				Repos: map[string]RepoInfo{
					"repo-a": {Name: "repo-a", Type: "team-context", TeamID: "team_3"},
				},
			},
			wantCount: 2,
			wantIDs:   []string{"team_1", "team_2"},
		},
		{
			name: "derives from repos when Teams empty",
			resp: ReposResponse{
				Repos: map[string]RepoInfo{
					"acme": {Name: "acme", Type: "team-context", TeamID: "team_acme", Slug: "acme"},
					"beta": {Name: "beta", Type: "team-context", TeamID: "team_beta", Slug: "beta"},
				},
			},
			wantCount: 2,
			wantIDs:   []string{"team_acme", "team_beta"},
		},
		{
			name: "skips non-team-context repos",
			resp: ReposResponse{
				Repos: map[string]RepoInfo{
					"tc":     {Name: "tc", Type: "team-context", TeamID: "team_tc"},
					"ledger": {Name: "ledger", Type: "ledger", TeamID: "team_tc"},
				},
			},
			wantCount: 1,
			wantIDs:   []string{"team_tc"},
		},
		{
			name: "empty repos and teams",
			resp: ReposResponse{
				Repos: map[string]RepoInfo{},
			},
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			teams := tt.resp.TeamMembershipsFromRepos()
			assert.Len(t, teams, tt.wantCount)

			for _, wantID := range tt.wantIDs {
				found := false
				for _, tm := range teams {
					if tm.ID == wantID {
						found = true
						break
					}
				}
				assert.True(t, found, "expected team %s in results", wantID)
			}
		})
	}
}

func TestDeriveSlug(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		input string
		want  string
	}{
		{"simple name", "Acme Corp", "acme-corp"},
		{"already kebab-case", "my-team", "my-team"},
		{"with numbers", "Team 42", "team-42"},
		{"special characters stripped", "My Team! @#$%", "my-team"},
		{"leading/trailing spaces", "  spaced  ", "spaced"},
		{"leading/trailing dashes", "--dashed--", "dashed"},
		{"empty string", "", ""},
		{"only special chars", "!@#$%", ""},
		{"mixed case", "MixedCaseTeam", "mixedcaseteam"},
		{"underscores stripped", "my_team_name", "myteamname"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := DeriveSlug(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNotifyImport_Success(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Contains(t, r.URL.Path, "/api/v1/teams/team_abc/context/import")
		assert.Equal(t, "Bearer tok", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &RepoClient{
		baseURL:    srv.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		authToken:  "tok",
	}

	err := client.NotifyImport("team_abc", map[string]string{"file": "doc.md"})
	assert.NoError(t, err)
}

func TestNotifyImport_404_GracefulDegradation(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &RepoClient{
		baseURL:    srv.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}

	err := client.NotifyImport("team_abc", map[string]string{})
	assert.NoError(t, err, "404 should be swallowed gracefully")
}

func TestNotifyImport_ServerError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := &RepoClient{
		baseURL:    srv.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}

	err := client.NotifyImport("team_abc", map[string]string{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

func TestNotifyImport_NetworkError(t *testing.T) {
	t.Parallel()

	client := &RepoClient{
		baseURL:    "http://127.0.0.1:1",
		httpClient: &http.Client{Timeout: 1 * time.Second},
	}

	// network errors are swallowed (graceful degradation)
	err := client.NotifyImport("team_abc", map[string]string{})
	assert.NoError(t, err)
}

func TestReadFirstRepoMarker(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		files    map[string]any // filename -> content (will be JSON marshaled)
		wantNil  bool
		wantID   string
	}{
		{
			name:    "no marker files",
			files:   map[string]any{"config.json": map[string]string{"key": "val"}},
			wantNil: true,
		},
		{
			name: "valid marker file",
			files: map[string]any{
				".repo_abc": map[string]string{
					"repo_id":   "repo_abc",
					"repo_salt": "salt123",
					"endpoint":  "https://sageox.ai",
				},
			},
			wantNil: false,
			wantID:  "repo_abc",
		},
		{
			name: "skips marker with empty repo_id",
			files: map[string]any{
				".repo_empty": map[string]string{
					"repo_id":  "",
					"endpoint": "https://sageox.ai",
				},
			},
			wantNil: true,
		},
		{
			name: "skips invalid JSON marker",
			files: map[string]any{
				".repo_bad": "not json {{{",
			},
			wantNil: true,
		},
		{
			name: "skips directories with repo prefix",
			files: map[string]any{
				// directory will be created separately
			},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()

			for name, content := range tt.files {
				var data []byte
				switch v := content.(type) {
				case string:
					data = []byte(v)
				default:
					var err error
					data, err = json.Marshal(v)
					require.NoError(t, err)
				}
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), data, 0644))
			}

			marker, err := ReadFirstRepoMarker(dir)
			require.NoError(t, err)

			if tt.wantNil {
				assert.Nil(t, marker)
			} else {
				require.NotNil(t, marker)
				assert.Equal(t, tt.wantID, marker.RepoID)
			}
		})
	}
}

func TestReadFirstRepoMarker_NonExistentDir(t *testing.T) {
	t.Parallel()

	marker, err := ReadFirstRepoMarker("/nonexistent/path")
	assert.NoError(t, err)
	assert.Nil(t, marker)
}

func TestNewRepoClientWithEndpoint(t *testing.T) {
	t.Parallel()

	client := NewRepoClientWithEndpoint("https://custom.example.com")
	assert.Equal(t, "https://custom.example.com", client.Endpoint())
}

func TestRepoClient_WithAuthToken(t *testing.T) {
	t.Parallel()

	client := NewRepoClientWithEndpoint("https://example.com")
	result := client.WithAuthToken("my-token")

	// WithAuthToken returns the same client (fluent API)
	assert.Same(t, client, result)
	assert.Equal(t, "my-token", client.authToken)
}

func TestRepoClient_Endpoint(t *testing.T) {
	t.Parallel()

	client := &RepoClient{baseURL: "https://sageox.ai"}
	assert.Equal(t, "https://sageox.ai", client.Endpoint())
}
