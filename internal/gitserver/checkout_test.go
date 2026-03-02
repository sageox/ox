package gitserver

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateCheckoutPath(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(t *testing.T) string // returns path to test
		wantErr   bool
		errTarget error
	}{
		{
			name: "non-existent path is valid",
			setup: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "new-repo")
			},
			wantErr: false,
		},
		{
			name: "empty directory is valid",
			setup: func(t *testing.T) string {
				dir := filepath.Join(t.TempDir(), "empty-dir")
				require.NoError(t, os.MkdirAll(dir, 0755))
				return dir
			},
			wantErr: false,
		},
		{
			name: "non-empty directory returns error",
			setup: func(t *testing.T) string {
				dir := filepath.Join(t.TempDir(), "non-empty")
				require.NoError(t, os.MkdirAll(dir, 0755))
				// create a file inside
				require.NoError(t, os.WriteFile(filepath.Join(dir, "file.txt"), []byte("data"), 0644))
				return dir
			},
			wantErr:   true,
			errTarget: ErrPathExists,
		},
		{
			name: "file path returns error",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				filePath := filepath.Join(dir, "file.txt")
				require.NoError(t, os.WriteFile(filePath, []byte("data"), 0644))
				return filePath
			},
			wantErr:   true,
			errTarget: ErrPathExists,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.setup(t)
			err := validateCheckoutPath(path)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errTarget != nil {
					assert.True(t, errors.Is(err, tt.errTarget), "error = %v, want %v", err, tt.errTarget)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestBuildAuthURL(t *testing.T) {
	tests := []struct {
		name    string
		repoURL string
		creds   *GitCredentials
		wantURL string
		wantErr bool
	}{
		{
			name:    "https URL with credentials",
			repoURL: "https://git.example.com/user/repo.git",
			creds: &GitCredentials{
				Token:    "glpat-test-token",
				Username: "testuser",
			},
			wantURL: "https://oauth2:glpat-test-token@git.example.com/user/repo.git",
			wantErr: false,
		},
		{
			name:    "https URL without credentials",
			repoURL: "https://git.example.com/user/repo.git",
			creds:   nil,
			wantURL: "https://git.example.com/user/repo.git",
			wantErr: false,
		},
		{
			name:    "https URL with empty token",
			repoURL: "https://git.example.com/user/repo.git",
			creds: &GitCredentials{
				Token: "",
			},
			wantURL: "https://git.example.com/user/repo.git",
			wantErr: false,
		},
		{
			name:    "ssh URL unchanged",
			repoURL: "git@git.example.com:user/repo.git",
			creds: &GitCredentials{
				Token: "glpat-test-token",
			},
			wantURL: "git@git.example.com:user/repo.git",
			wantErr: false,
		},
		{
			name:    "URL with existing credentials",
			repoURL: "https://old:token@git.example.com/user/repo.git",
			creds: &GitCredentials{
				Token: "glpat-new-token",
			},
			wantURL: "https://oauth2:glpat-new-token@git.example.com/user/repo.git",
			wantErr: false,
		},
		{
			name:    "http localhost with credentials",
			repoURL: "http://localhost:8929/org/repo.git",
			creds: &GitCredentials{
				Token: "glpat-test-token",
			},
			wantURL: "http://oauth2:glpat-test-token@localhost:8929/org/repo.git",
			wantErr: false,
		},
		{
			name:    "http localhost without port",
			repoURL: "http://localhost/org/repo.git",
			creds: &GitCredentials{
				Token: "glpat-test-token",
			},
			wantURL: "http://oauth2:glpat-test-token@localhost/org/repo.git",
			wantErr: false,
		},
		{
			name:    "http 127.0.0.1 with credentials",
			repoURL: "http://127.0.0.1:8929/org/repo.git",
			creds: &GitCredentials{
				Token: "glpat-test-token",
			},
			wantURL: "http://oauth2:glpat-test-token@127.0.0.1:8929/org/repo.git",
			wantErr: false,
		},
		{
			name:    "http external host unchanged (security)",
			repoURL: "http://external-host.com/repo.git",
			creds: &GitCredentials{
				Token: "glpat-test-token",
			},
			wantURL: "http://external-host.com/repo.git",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildAuthURL(tt.repoURL, tt.creds)

			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantURL, got)
		})
	}
}

func TestIsSSHURL(t *testing.T) {
	tests := []struct {
		name   string
		url    string
		wantIs bool
	}{
		{
			name:   "standard SSH URL",
			url:    "git@github.com:user/repo.git",
			wantIs: true,
		},
		{
			name:   "SSH URL with custom host",
			url:    "git@gitlab.example.com:group/repo.git",
			wantIs: true,
		},
		{
			name:   "HTTPS URL",
			url:    "https://github.com/user/repo.git",
			wantIs: false,
		},
		{
			name:   "HTTPS URL with credentials",
			url:    "https://user:token@github.com/user/repo.git",
			wantIs: false,
		},
		{
			name:   "file URL",
			url:    "file:///path/to/repo",
			wantIs: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSSHURL(tt.url)
			assert.Equal(t, tt.wantIs, got)
		})
	}
}

func TestSanitizeURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "URL with credentials",
			url:  "https://user:token@git.example.com/repo.git",
			want: "https://git.example.com/repo.git",
		},
		{
			name: "URL without credentials",
			url:  "https://git.example.com/repo.git",
			want: "https://git.example.com/repo.git",
		},
		{
			name: "SSH URL unchanged",
			url:  "git@github.com:user/repo.git",
			want: "git@github.com:user/repo.git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeURL(tt.url)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDefaultCheckoutPath(t *testing.T) {
	tests := []struct {
		name     string
		repoName string
		workDir  string
		want     string
	}{
		{
			name:     "with explicit workDir",
			repoName: "ledger",
			workDir:  "/home/user/projects/myrepo",
			want:     "/home/user/projects/ledger",
		},
		{
			name:     "team repo name",
			repoName: "team-acme-norms",
			workDir:  "/home/user/projects/myrepo",
			want:     "/home/user/projects/team-acme-norms",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DefaultCheckoutPath(tt.repoName, tt.workDir)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRepoNameFromURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "HTTPS URL with .git suffix",
			url:  "https://git.example.com/user/my-repo.git",
			want: "my-repo",
		},
		{
			name: "HTTPS URL without .git suffix",
			url:  "https://git.example.com/user/my-repo",
			want: "my-repo",
		},
		{
			name: "SSH URL",
			url:  "git@github.com:user/my-repo.git",
			want: "my-repo",
		},
		{
			name: "SSH URL without .git",
			url:  "git@github.com:user/another-repo",
			want: "another-repo",
		},
		{
			name: "nested path",
			url:  "https://git.example.com/group/subgroup/my-repo.git",
			want: "my-repo",
		},
		{
			name: "empty string",
			url:  "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := repoNameFromURL(tt.url)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsGitInstalled(t *testing.T) {
	// this should return true on most development machines
	// we don't fail if git is not installed since that's a valid state
	installed := IsGitInstalled()
	t.Logf("IsGitInstalled() = %v", installed)
}

func TestGetGitVersion(t *testing.T) {
	if !IsGitInstalled() {
		t.Skip("git not installed")
	}

	version, err := GetGitVersion()
	require.NoError(t, err)
	assert.NotEmpty(t, version)

	t.Logf("git version: %s", version)
}

func TestCheckoutOptions(t *testing.T) {
	// verify options struct works correctly
	opts := &CheckoutOptions{
		Depth:  1,
		Branch: "main",
	}

	assert.Equal(t, 1, opts.Depth)
	assert.Equal(t, "main", opts.Branch)
}

