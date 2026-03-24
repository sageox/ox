package gitserver

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidatePATLiveness_NilCreds(t *testing.T) {
	result := ValidatePATLiveness(context.Background(), nil)
	assert.True(t, result.Skipped)
	assert.Contains(t, result.Reason, "no credentials")
}

func TestValidatePATLiveness_EmptyToken(t *testing.T) {
	creds := &GitCredentials{Token: ""}
	result := ValidatePATLiveness(context.Background(), creds)
	assert.True(t, result.Skipped)
	assert.Contains(t, result.Reason, "no credentials")
}

func TestValidatePATLiveness_NoRepos(t *testing.T) {
	creds := &GitCredentials{Token: "some-token"}
	result := ValidatePATLiveness(context.Background(), creds)
	assert.True(t, result.Skipped)
	assert.Contains(t, result.Reason, "no repo URL")
}

func TestValidatePATLiveness_Timeout(t *testing.T) {
	// unreachable host — context timeout should cause skip
	creds := &GitCredentials{
		Token: "some-token",
		Repos: map[string]RepoEntry{
			"test": {URL: "https://192.0.2.1:1/test/repo.git"},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	result := ValidatePATLiveness(ctx, creds)
	assert.True(t, result.Skipped, "should skip on timeout")
	assert.Contains(t, result.Reason, "network unreachable")
}

func TestPickProbeRepoURL(t *testing.T) {
	tests := []struct {
		name  string
		creds *GitCredentials
		want  string
	}{
		{
			name:  "nil repos",
			creds: &GitCredentials{},
			want:  "",
		},
		{
			name: "empty repos map",
			creds: &GitCredentials{
				Repos: map[string]RepoEntry{},
			},
			want: "",
		},
		{
			name: "picks first available URL",
			creds: &GitCredentials{
				Repos: map[string]RepoEntry{
					"team1": {URL: "https://git.sageox.ai/team1/repo.git"},
				},
			},
			want: "https://git.sageox.ai/team1/repo.git",
		},
		{
			name: "skips empty URLs",
			creds: &GitCredentials{
				Repos: map[string]RepoEntry{
					"empty": {URL: ""},
					"valid": {URL: "https://git.sageox.ai/valid/repo.git"},
				},
			},
			want: "https://git.sageox.ai/valid/repo.git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pickProbeRepoURL(tt.creds)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBuildProbeURL(t *testing.T) {
	tests := []struct {
		name    string
		repoURL string
		want    string
		wantErr bool
	}{
		{
			name:    "https URL gets oauth2 user",
			repoURL: "https://git.sageox.ai/team/repo.git",
			want:    "https://oauth2@git.sageox.ai/team/repo.git",
		},
		{
			name:    "http URL with port",
			repoURL: "http://localhost:3000/team/repo.git",
			want:    "http://oauth2@localhost:3000/team/repo.git",
		},
		{
			name:    "bare URL gets https scheme",
			repoURL: "git.sageox.ai/team/repo.git",
			want:    "https://oauth2@git.sageox.ai/team/repo.git",
		},
		{
			name:    "invalid URL",
			repoURL: "://not-a-url",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildProbeURL(tt.repoURL)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			gotURL, _ := url.Parse(got)
			wantURL, _ := url.Parse(tt.want)
			assert.Equal(t, wantURL.Scheme, gotURL.Scheme)
			assert.Equal(t, wantURL.Host, gotURL.Host)
			assert.Equal(t, wantURL.Path, gotURL.Path)
			assert.Equal(t, wantURL.User.Username(), gotURL.User.Username())
			// no password — token is via GIT_ASKPASS
			_, hasPass := gotURL.User.Password()
			assert.False(t, hasPass, "token should not be in URL")
		})
	}
}

func TestWriteAskpassScript(t *testing.T) {
	path, err := writeAskpassScript("test-token-123")
	require.NoError(t, err)
	defer os.Remove(path)

	// verify file exists and is executable
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&0100, "script should be executable")

	// verify content echoes the token
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(content), "test-token-123")
}
