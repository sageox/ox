package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscoverEndpoints(t *testing.T) {
	tests := []struct {
		name    string
		markers map[string]map[string]string // filename -> marker content
		want    int                          // expected number of unique endpoints
		wantEps []string                     // expected endpoints (in any order)
	}{
		{
			name:    "no markers",
			markers: map[string]map[string]string{},
			want:    0,
		},
		{
			name: "single marker with endpoint",
			markers: map[string]map[string]string{
				".repo_abc123": {
					"repo_id":  "repo_abc123",
					"endpoint": "https://api.sageox.ai",
				},
			},
			want:    1,
			wantEps: []string{"https://sageox.ai"},
		},
		{
			name: "multiple markers same endpoint",
			markers: map[string]map[string]string{
				".repo_abc123": {
					"repo_id":  "repo_abc123",
					"endpoint": "https://api.sageox.ai",
				},
				".repo_def456": {
					"repo_id":  "repo_def456",
					"endpoint": "https://api.sageox.ai",
				},
			},
			want:    1,
			wantEps: []string{"https://sageox.ai"},
		},
		{
			name: "multiple markers different endpoints",
			markers: map[string]map[string]string{
				".repo_abc123": {
					"repo_id":  "repo_abc123",
					"endpoint": "https://api.sageox.ai",
				},
				".repo_def456": {
					"repo_id":  "repo_def456",
					"endpoint": "https://enterprise.example.com",
				},
			},
			want:    2,
			wantEps: []string{"https://sageox.ai", "https://enterprise.example.com"},
		},
		{
			name: "trailing slashes normalized",
			markers: map[string]map[string]string{
				".repo_abc123": {
					"repo_id":  "repo_abc123",
					"endpoint": "https://api.sageox.ai/",
				},
				".repo_def456": {
					"repo_id":  "repo_def456",
					"endpoint": "https://api.sageox.ai",
				},
			},
			want:    1,
			wantEps: []string{"https://sageox.ai"},
		},
		{
			name: "subdomain prefixes normalized to same endpoint",
			markers: map[string]map[string]string{
				".repo_abc123": {
					"repo_id":  "repo_abc123",
					"endpoint": "https://api.sageox.ai",
				},
				".repo_def456": {
					"repo_id":  "repo_def456",
					"endpoint": "https://www.sageox.ai",
				},
				".repo_ghi789": {
					"repo_id":  "repo_ghi789",
					"endpoint": "https://sageox.ai",
				},
			},
			want:    1,
			wantEps: []string{"https://sageox.ai"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// create temp directory
			tmpDir := t.TempDir()
			sageoxDir := filepath.Join(tmpDir, ".sageox")
			require.NoError(t, os.MkdirAll(sageoxDir, 0755))

			// create marker files
			for filename, content := range tt.markers {
				data, _ := json.Marshal(content)
				require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, filename), data, 0644))
			}

			// discover endpoints
			endpoints, err := DiscoverEndpoints(sageoxDir)
			require.NoError(t, err, "DiscoverEndpoints() error")

			assert.Len(t, endpoints, tt.want, "DiscoverEndpoints() returned wrong number of endpoints")

			// verify expected endpoints are present
			for _, wantEp := range tt.wantEps {
				found := false
				for _, got := range endpoints {
					if got.Endpoint == wantEp {
						found = true
						break
					}
				}
				assert.True(t, found, "DiscoverEndpoints() missing endpoint %q", wantEp)
			}
		})
	}
}

func TestDiscoverEndpoints_NonExistentDir(t *testing.T) {
	endpoints, err := DiscoverEndpoints("/nonexistent/path/.sageox")
	assert.NoError(t, err, "DiscoverEndpoints() unexpected error for nonexistent dir")
	assert.Nil(t, endpoints, "DiscoverEndpoints() expected nil for nonexistent dir")
}

func TestDiscoverEndpoints_IgnoresNonMarkerFiles(t *testing.T) {
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	// create non-marker files
	os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(`{"endpoint":"https://ignore.me"}`), 0644)
	os.WriteFile(filepath.Join(sageoxDir, "README.md"), []byte("# Test"), 0644)

	// create one valid marker
	marker := map[string]string{
		"repo_id":  "repo_abc123",
		"endpoint": "https://api.sageox.ai",
	}
	data, _ := json.Marshal(marker)
	os.WriteFile(filepath.Join(sageoxDir, ".repo_abc123"), data, 0644)

	endpoints, err := DiscoverEndpoints(sageoxDir)
	require.NoError(t, err, "DiscoverEndpoints() error")

	assert.Len(t, endpoints, 1, "DiscoverEndpoints() returned wrong number of endpoints")
}

func TestSelectEndpoint_SingleEndpoint(t *testing.T) {
	endpoints := []EndpointInfo{
		{Endpoint: "https://sageox.ai", RepoID: "repo_abc123"},
	}

	selected, err := SelectEndpoint(endpoints, "", "")
	require.NoError(t, err, "SelectEndpoint() error")

	assert.Equal(t, "https://sageox.ai", selected, "SelectEndpoint()")
}

func TestSelectEndpoint_FlagProvided(t *testing.T) {
	endpoints := []EndpointInfo{
		{Endpoint: "https://sageox.ai", RepoID: "repo_abc123"},
		{Endpoint: "https://enterprise.example.com", RepoID: "repo_def456"},
	}

	selected, err := SelectEndpoint(endpoints, "", "https://enterprise.example.com")
	require.NoError(t, err, "SelectEndpoint() error")

	assert.Equal(t, "https://enterprise.example.com", selected, "SelectEndpoint()")
}

func TestSelectEndpoint_FlagProvidedWithTrailingSlash(t *testing.T) {
	endpoints := []EndpointInfo{
		{Endpoint: "https://sageox.ai", RepoID: "repo_abc123"},
	}

	selected, err := SelectEndpoint(endpoints, "", "https://sageox.ai/")
	require.NoError(t, err, "SelectEndpoint() error")

	assert.Equal(t, "https://sageox.ai", selected, "SelectEndpoint()")
}

func TestSelectEndpoint_FlagProvidedWithSubdomainPrefix(t *testing.T) {
	endpoints := []EndpointInfo{
		{Endpoint: "https://sageox.ai", RepoID: "repo_abc123"},
	}

	// flag uses api. prefix but should still match the normalized endpoint
	selected, err := SelectEndpoint(endpoints, "", "https://api.sageox.ai")
	require.NoError(t, err, "SelectEndpoint() error")

	assert.Equal(t, "https://sageox.ai", selected, "SelectEndpoint()")
}

func TestSelectEndpoint_FlagNotFound(t *testing.T) {
	endpoints := []EndpointInfo{
		{Endpoint: "https://sageox.ai", RepoID: "repo_abc123"},
	}

	_, err := SelectEndpoint(endpoints, "", "https://nonexistent.com")
	assert.Error(t, err, "SelectEndpoint() expected error for nonexistent endpoint")
	assert.Contains(t, err.Error(), "not found", "SelectEndpoint() error should contain 'not found'")
}

func TestSelectEndpoint_EmptyEndpoints(t *testing.T) {
	_, err := SelectEndpoint([]EndpointInfo{}, "", "")
	assert.Error(t, err, "SelectEndpoint() expected error for empty endpoints")
}

func TestSelectEndpoint_MultipleEndpointsWithDefault(t *testing.T) {
	endpoints := []EndpointInfo{
		{Endpoint: "https://sageox.ai", RepoID: "repo_abc123"},
		{Endpoint: "https://enterprise.example.com", RepoID: "repo_def456"},
	}

	// mock stdin with empty input (use default)
	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	w.Write([]byte("\n"))
	w.Close()
	defer func() { os.Stdin = oldStdin }()

	// capture stdout
	oldStdout := os.Stdout
	os.Stdout, _ = os.Open(os.DevNull)
	defer func() { os.Stdout = oldStdout }()

	selected, err := SelectEndpoint(endpoints, "https://enterprise.example.com", "")
	require.NoError(t, err, "SelectEndpoint() error")

	assert.Equal(t, "https://enterprise.example.com", selected, "SelectEndpoint() should return default")
}

func TestSelectEndpoint_MultipleEndpointsExplicitSelection(t *testing.T) {
	endpoints := []EndpointInfo{
		{Endpoint: "https://sageox.ai", RepoID: "repo_abc123"},
		{Endpoint: "https://enterprise.example.com", RepoID: "repo_def456"},
	}

	// mock stdin with "1" selection
	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	w.Write([]byte("1\n"))
	w.Close()
	defer func() { os.Stdin = oldStdin }()

	// capture stdout
	oldStdout := os.Stdout
	os.Stdout, _ = os.Open(os.DevNull)
	defer func() { os.Stdout = oldStdout }()

	selected, err := SelectEndpoint(endpoints, "", "")
	require.NoError(t, err, "SelectEndpoint() error")

	// endpoints are sorted alphabetically, so enterprise.example.com is first (e < s)
	assert.Equal(t, "https://enterprise.example.com", selected, "SelectEndpoint()")
}

func TestSelectEndpoint_InvalidSelection(t *testing.T) {
	endpoints := []EndpointInfo{
		{Endpoint: "https://sageox.ai", RepoID: "repo_abc123"},
		{Endpoint: "https://enterprise.example.com", RepoID: "repo_def456"},
	}

	// mock stdin with invalid input
	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	w.Write([]byte("invalid\n"))
	w.Close()
	defer func() { os.Stdin = oldStdin }()

	// capture stdout
	oldStdout := os.Stdout
	os.Stdout, _ = os.Open(os.DevNull)
	defer func() { os.Stdout = oldStdout }()

	_, err := SelectEndpoint(endpoints, "", "")
	assert.Error(t, err, "SelectEndpoint() expected error for invalid selection")
}

func TestSelectEndpoint_OutOfRangeSelection(t *testing.T) {
	// need multiple endpoints to trigger the selection prompt
	endpoints := []EndpointInfo{
		{Endpoint: "https://sageox.ai", RepoID: "repo_abc123"},
		{Endpoint: "https://enterprise.example.com", RepoID: "repo_def456"},
	}

	// mock stdin with out of range input (5 when only 2 exist)
	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	w.Write([]byte("5\n"))
	w.Close()
	defer func() { os.Stdin = oldStdin }()

	// capture stdout
	oldStdout := os.Stdout
	os.Stdout, _ = os.Open(os.DevNull)
	defer func() { os.Stdout = oldStdout }()

	_, err := SelectEndpoint(endpoints, "", "")
	assert.Error(t, err, "SelectEndpoint() expected error for out of range selection")
}

func TestSelectEndpoint_ZeroSelection(t *testing.T) {
	endpoints := []EndpointInfo{
		{Endpoint: "https://sageox.ai", RepoID: "repo_abc123"},
		{Endpoint: "https://enterprise.example.com", RepoID: "repo_def456"},
	}

	// mock stdin with 0 input (below valid range)
	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	w.Write([]byte("0\n"))
	w.Close()
	defer func() { os.Stdin = oldStdin }()

	// capture stdout
	oldStdout := os.Stdout
	os.Stdout, _ = os.Open(os.DevNull)
	defer func() { os.Stdout = oldStdout }()

	_, err := SelectEndpoint(endpoints, "", "")
	assert.Error(t, err, "SelectEndpoint() expected error for zero selection")
}

func TestHasMultipleEndpoints(t *testing.T) {
	tests := []struct {
		name    string
		markers map[string]map[string]string
		want    bool
	}{
		{
			name:    "no markers",
			markers: map[string]map[string]string{},
			want:    false,
		},
		{
			name: "single endpoint",
			markers: map[string]map[string]string{
				".repo_abc123": {"repo_id": "repo_abc123", "endpoint": "https://sageox.ai"},
			},
			want: false,
		},
		{
			name: "multiple endpoints",
			markers: map[string]map[string]string{
				".repo_abc123": {"repo_id": "repo_abc123", "endpoint": "https://sageox.ai"},
				".repo_def456": {"repo_id": "repo_def456", "endpoint": "https://enterprise.example.com"},
			},
			want: true,
		},
		{
			name: "same endpoint with different subdomain prefixes",
			markers: map[string]map[string]string{
				".repo_abc123": {"repo_id": "repo_abc123", "endpoint": "https://api.sageox.ai"},
				".repo_def456": {"repo_id": "repo_def456", "endpoint": "https://www.sageox.ai"},
			},
			want: false,
		},
		{
			name: "app and git prefixes collapse",
			markers: map[string]map[string]string{
				".repo_abc123": {"repo_id": "repo_abc123", "endpoint": "https://app.sageox.ai"},
				".repo_def456": {"repo_id": "repo_def456", "endpoint": "https://git.sageox.ai"},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			sageoxDir := filepath.Join(tmpDir, ".sageox")
			os.MkdirAll(sageoxDir, 0755)

			for filename, content := range tt.markers {
				data, _ := json.Marshal(content)
				os.WriteFile(filepath.Join(sageoxDir, filename), data, 0644)
			}

			got := HasMultipleEndpoints(sageoxDir)
			assert.Equal(t, tt.want, got, "HasMultipleEndpoints()")
		})
	}
}

func TestDiscoverEndpoints_AppAndGitPrefixes(t *testing.T) {
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	os.MkdirAll(sageoxDir, 0755)

	markers := map[string]map[string]string{
		".repo_abc123": {"repo_id": "repo_abc123", "endpoint": "https://app.sageox.ai"},
		".repo_def456": {"repo_id": "repo_def456", "endpoint": "https://git.sageox.ai"},
		".repo_ghi789": {"repo_id": "repo_ghi789", "endpoint": "https://sageox.ai"},
	}
	for filename, content := range markers {
		data, _ := json.Marshal(content)
		os.WriteFile(filepath.Join(sageoxDir, filename), data, 0644)
	}

	endpoints, err := DiscoverEndpoints(sageoxDir)
	require.NoError(t, err)
	assert.Len(t, endpoints, 1, "all prefixed variants should collapse to one endpoint")
	assert.Equal(t, "https://sageox.ai", endpoints[0].Endpoint)
}

func TestSelectEndpoint_PrefixedDefaultEndpoint(t *testing.T) {
	endpoints := []EndpointInfo{
		{Endpoint: "https://sageox.ai", RepoID: "repo_abc123"},
		{Endpoint: "https://enterprise.example.com", RepoID: "repo_def456"},
	}

	// simulate user selecting "1" (the default)
	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	w.WriteString("1\n")
	w.Close()
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()

	// default endpoint has a prefix - should still match
	selected, err := SelectEndpoint(endpoints, "https://api.sageox.ai", "")
	require.NoError(t, err)
	assert.Equal(t, "https://enterprise.example.com", selected, "should select option 1")
}

func TestSelectEndpoint_FlagWithPrefixMultipleEndpoints(t *testing.T) {
	endpoints := []EndpointInfo{
		{Endpoint: "https://sageox.ai", RepoID: "repo_abc123"},
		{Endpoint: "https://enterprise.example.com", RepoID: "repo_def456"},
	}

	// flag uses www. prefix - should match the normalized sageox.ai endpoint
	selected, err := SelectEndpoint(endpoints, "", "https://www.sageox.ai")
	require.NoError(t, err)
	assert.Equal(t, "https://sageox.ai", selected)
}
