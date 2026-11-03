package identity

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type giteaRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f giteaRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestGiteaIdentity_RemoteTrust(t *testing.T) {
	tests := []struct {
		name      string
		remote    string
		envURL    string
		envToken  string
		teaURL    string
		wantURL   string
		wantToken string
	}{
		{
			name:     "unscoped environment token",
			remote:   "attacker.example/gitea/repo",
			envToken: "env-token",
		},
		{
			name:     "environment token for another instance",
			remote:   "attacker.example/gitea/repo",
			envURL:   "https://git.example.com",
			envToken: "env-token",
		},
		{
			name:   "tea default for another instance",
			remote: "attacker.example/gitea/repo",
			teaURL: "https://git.example.com",
		},
		{
			name:      "matching tea login ignores unrelated environment token",
			remote:    "git.example.com/team/repo",
			envURL:    "https://other.example.com",
			envToken:  "env-token",
			teaURL:    "https://git.example.com/",
			wantURL:   "https://git.example.com",
			wantToken: "tea-token",
		},
		{
			name:      "matching environment instance",
			remote:    "git.example.com/team/repo",
			envURL:    "https://git.example.com/",
			envToken:  "env-token",
			wantURL:   "https://git.example.com",
			wantToken: "env-token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", dir)
			t.Setenv("GITEA_URL", tt.envURL)
			t.Setenv("GITEA_TOKEN", tt.envToken)
			if tt.teaURL != "" {
				teaDir := filepath.Join(dir, "tea")
				require.NoError(t, os.MkdirAll(teaDir, 0o700))
				config := fmt.Sprintf("logins:\n  - url: %q\n    token: tea-token\n    default: true\n", tt.teaURL)
				require.NoError(t, os.WriteFile(filepath.Join(teaDir, "config.yml"), []byte(config), 0o600))
			}

			// Intercept all requests so regressions cannot contact an external host.
			requests := 0
			original := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = original })
			http.DefaultTransport = giteaRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
				requests++
				assert.Equal(t, tt.wantURL+"/api/v1/user", req.URL.String())
				assert.Equal(t, "token "+tt.wantToken, req.Header.Get("Authorization"))
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"id":7,"login":"testuser"}`)),
					Header:     make(http.Header),
				}, nil
			})

			// Remotes use the normalized host/path form returned by repotools.GetRemoteURLs.
			instanceURL := extractGiteaInstanceURL([]string{tt.remote})
			assert.Equal(t, tt.wantURL, instanceURL)
			assert.Equal(t, tt.wantURL != "", isGiteaInstance(tt.remote))
			if tt.wantURL == "" {
				// Credential selection must also fail closed if detection is bypassed.
				identity, err := getGiteaIdentity("https://" + extractHost(tt.remote))
				assert.Error(t, err)
				assert.Nil(t, identity)
				assert.Zero(t, requests)
			} else {
				identity, err := getGiteaIdentity(instanceURL)
				require.NoError(t, err)
				require.NotNil(t, identity)
				assert.Equal(t, "testuser", identity.Username)
				assert.Equal(t, 1, requests)
			}
		})
	}
}

func TestFetchGiteaUser_DoesNotFollowRedirects(t *testing.T) {
	t.Parallel()

	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("unexpected request to redirect destination")
		_, _ = io.WriteString(w, `{"id":7,"login":"testuser"}`)
	}))
	defer destination.Close()

	instance := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "token test-token", r.Header.Get("Authorization"))
		http.Redirect(w, r, destination.URL+"/api/v1/user", http.StatusFound)
	}))
	defer instance.Close()

	_, err := fetchGiteaUser(instance.URL, "test-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "302")
}
