package identity

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtractHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
		want string
	}{
		{"https url", "https://github.com/sageox/ox.git", "github.com"},
		{"http url", "http://gitlab.com/sageox/ox.git", "gitlab.com"},
		{"ssh url", "git@github.com:sageox/ox.git", "github.com"},
		{"https with port", "https://gitea.example.com:3000/user/repo.git", "gitea.example.com:3000"},
		{"bare host", "example.com/repo.git", "example.com"},
		{"empty string", "", ""},
		{"ssh with subdomain", "git@git.internal.company.com:team/repo.git", "git.internal.company.com"},
		{"ssh scheme", "ssh://git@git.internal.company.com/team/repo.git", "git.internal.company.com"},
		{"userinfo is not host", "https://gitea.example.com@attacker.example/repo", "attacker.example"},
		{"case insensitive host", "https://GITEA.EXAMPLE.COM/repo", "gitea.example.com"},
		{"invalid URL", "https://host:invalid/repo", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, extractHost(tt.url))
		})
	}
}

func TestExtractGiteaInstanceURL(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GITEA_URL", "https://git.corp.com")
	t.Setenv("GITEA_TOKEN", "corp-token")

	tests := []struct {
		name string
		urls []string
		want string
	}{
		{"unconfigured gitea host", []string{"https://gitea.example.com/user/repo.git"}, ""},
		{"gitea in path", []string{"https://attacker.example/gitea/repo.git"}, ""},
		{"configured host", []string{"https://git.corp.com/user/repo.git"}, "https://git.corp.com"},
		{"normalized remote", []string{"git.corp.com/user/repo"}, "https://git.corp.com"},
		{"ssh remote", []string{"git@git.corp.com:user/repo.git"}, "https://git.corp.com"},
		{"different port", []string{"https://git.corp.com:3000/user/repo.git"}, ""},
		{"host suffix", []string{"https://git.corp.com.attacker.example/gitea/repo.git"}, ""},
		{"no gitea", []string{"https://github.com/user/repo.git"}, ""},
		{"empty urls", nil, ""},
		{"mixed remotes", []string{"https://attacker.example/gitea/repo.git", "https://git.corp.com/c/d"}, "https://git.corp.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractGiteaInstanceURL(tt.urls)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsGiteaInstance(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GITEA_URL", "https://gitea.corp.com")
	t.Setenv("GITEA_TOKEN", "corp-token")

	tests := []struct {
		url  string
		want bool
	}{
		{"https://gitea.example.com/user/repo", false},
		{"https://GITEA.CORP.COM/user/repo", true},
		{"https://attacker.example/gitea/repo", false},
		{"https://gitea.corp.com@attacker.example/repo", false},
		{"https://github.com/user/repo", false},
		{"https://gitlab.com/user/repo", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got := isGiteaInstance(tt.url)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestResolveWithConfig_CollectionNone(t *testing.T) {
	// "none" mode should only return git config identity, no API calls
	result, err := ResolveWithConfig(&Config{Collection: "none"})
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.NotNil(t, result.Primary)
	assert.Equal(t, "git", result.Primary.Source)
	assert.NotNil(t, result.Git)
	// no provider identities should be set
	assert.Nil(t, result.SageOx)
	assert.Nil(t, result.GitHub)
	assert.Nil(t, result.GitLab)
}

func TestResolveWithConfig_NilConfig(t *testing.T) {
	// nil config should default to "all" collection and not panic
	result, err := ResolveWithConfig(nil)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.NotNil(t, result.Primary)
}

func TestResolveWithConfig_DisabledProviders(t *testing.T) {
	result, err := ResolveWithConfig(&Config{
		Collection: "all",
		Disable:    []string{"github", "gitlab", "bitbucket"},
	})
	assert.NoError(t, err)
	assert.NotNil(t, result)
	// disabled providers should not be populated
	assert.Nil(t, result.GitHub)
	assert.Nil(t, result.GitLab)
	assert.Nil(t, result.Bitbucket)
}

func TestGetGitIdentity(t *testing.T) {
	identity := getGitIdentity()
	assert.NotNil(t, identity)
	assert.Equal(t, "git", identity.Source)
	assert.False(t, identity.Verified)
}

func TestGetSageOxIdentity_NoToken(t *testing.T) {
	// without a valid SageOx token, should return nil
	// (this test runs in CI where there's no auth)
	identity := getSageOxIdentity()
	// may be nil (no token) or non-nil (if dev has local auth)
	if identity != nil {
		assert.Equal(t, "sageox", identity.Source)
		assert.True(t, identity.Verified)
	}
}
