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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, extractHost(tt.url))
		})
	}
}

func TestExtractGiteaInstanceURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		urls []string
		want string
	}{
		{"gitea in url", []string{"https://gitea.example.com/user/repo.git"}, "https://gitea.example.com"},
		{"no gitea", []string{"https://github.com/user/repo.git"}, ""},
		{"empty urls", nil, ""},
		{"mixed remotes", []string{"https://github.com/a/b", "https://gitea.corp.com/c/d"}, "https://gitea.corp.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractGiteaInstanceURL(tt.urls)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsGiteaInstance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		url  string
		want bool
	}{
		{"https://gitea.example.com/user/repo", true},
		{"https://GITEA.CORP.COM/user/repo", true},
		{"https://github.com/user/repo", false},
		{"https://gitlab.com/user/repo", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			t.Parallel()
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
