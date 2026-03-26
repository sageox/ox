package gitserver

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasNonOauth2Userinfo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{"oauth2_user", "https://oauth2:token@host.com/repo.git", false},
		{"deploy_token", "https://deploy-token:token@host.com/repo.git", true},
		{"custom_user", "https://user:pass@host.com/repo.git", true},
		{"no_userinfo", "https://host.com/repo.git", false},
		{"empty_url", "", false},
		{"invalid_url", "://invalid", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := hasNonOauth2Userinfo(tt.url)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestExtractHost_EdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"https_with_port", "https://git.example.com:443/repo", "git.example.com"},
		{"http_localhost", "http://localhost:3000/repo", "localhost"},
		{"bare_hostname", "example.com", "example.com"},
		{"bare_with_port", "example.com:8443", "example.com"},
		{"ipv4_address", "http://192.168.1.1/repo", "192.168.1.1"},
		{"empty_string", "", ""},
		{"just_scheme", "https://", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractHost(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRepoNameFromURL_EdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"ssh_no_colon_parts", "git@host", ""},
		{"https_root_path", "https://host.com/", ""},
		{"https_just_host", "https://host.com", ""},
		{"ssh_with_nested_path", "git@github.com:org/sub/repo.git", "repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := repoNameFromURL(tt.url)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSanitizeURL_EdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"oauth2_creds", "https://oauth2:secret@host.com/repo.git", "https://host.com/repo.git"},
		{"empty_password", "https://user@host.com/repo.git", "https://host.com/repo.git"},
		{"no_path", "https://user:pass@host.com", "https://host.com"},
		{"ssh_passthrough", "git@host.com:user/repo.git", "git@host.com:user/repo.git"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := sanitizeURL(tt.url)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBuildAuthURL_EdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		url     string
		creds   *GitCredentials
		want    string
		wantErr bool
	}{
		{
			name:  "nil_creds",
			url:   "https://host.com/repo.git",
			creds: nil,
			want:  "https://host.com/repo.git",
		},
		{
			name:  "empty_token",
			url:   "https://host.com/repo.git",
			creds: &GitCredentials{Token: ""},
			want:  "https://host.com/repo.git",
		},
		{
			name:  "http_non_local_rejected",
			url:   "http://external.com/repo.git",
			creds: &GitCredentials{Token: "tok123"},
			want:  "http://external.com/repo.git",
		},
		{
			name:  "http_127_accepted",
			url:   "http://127.0.0.1:8080/repo.git",
			creds: &GitCredentials{Token: "tok123"},
			want:  "http://oauth2:tok123@127.0.0.1:8080/repo.git",
		},
		{
			name:  "file_scheme_no_creds",
			url:   "file:///path/to/repo",
			creds: &GitCredentials{Token: "tok123"},
			want:  "file:///path/to/repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := BuildAuthURL(tt.url, tt.creds)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSanitizeRemoteURL_MoreCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"strips_all_creds", "https://user:pass@example.com/repo.git", "https://example.com/repo.git"},
		{"ssh_unchanged", "git@github.com:org/repo.git", "git@github.com:org/repo.git"},
		{"no_creds_unchanged", "https://example.com/repo.git", "https://example.com/repo.git"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, SanitizeRemoteURL(tt.input))
		})
	}
}

func TestIsSSHURL_MoreCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		url    string
		wantIs bool
	}{
		{"git@github.com:user/repo", true},
		{"user@host:path", true},
		{"https://github.com/repo", false},
		{"http://localhost/repo", false},
		{"file:///path", false},
		{"", false},
		{"no-at-sign", false},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.wantIs, isSSHURL(tt.url))
		})
	}
}

func TestDefaultCheckoutPath_EmptyWorkDir(t *testing.T) {
	path := DefaultCheckoutPath("ledger", "")
	assert.NotEmpty(t, path)
	assert.Contains(t, path, "ledger")
}
