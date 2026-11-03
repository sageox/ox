package identity

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- normalizeGiteaURL ---
// Preserve credential scope while normalizing host case and a trailing slash.

func TestNormalizeGiteaURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
		want string
	}{
		{"https url", "https://gitea.example.com/user/repo", "https://gitea.example.com/user/repo"},
		{"http url", "http://gitea.example.com/user/repo", "http://gitea.example.com/user/repo"},
		{"with port", "https://gitea.example.com:3000/user/repo", "https://gitea.example.com:3000/user/repo"},
		{"trailing slash", "https://gitea.example.com/", "https://gitea.example.com"},
		{"host case", "https://GITEA.EXAMPLE.COM/", "https://gitea.example.com"},
		{"empty", "", ""},
		{"just host", "https://gitea.local", "https://gitea.local"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, normalizeGiteaURL(tt.url))
		})
	}
}

func TestTrimTrailingSlash(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"https://example.com/", "https://example.com"},
		{"https://example.com", "https://example.com"},
		{"/", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, trimTrailingSlash(tt.input))
		})
	}
}

// --- xdgConfigHome ---
// Prevents config file lookups in wrong directory when XDG_CONFIG_HOME is set vs unset

func TestXdgConfigHome(t *testing.T) {
	t.Run("respects XDG_CONFIG_HOME env", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "/custom/config")
		assert.Equal(t, "/custom/config", xdgConfigHome())
	})

	t.Run("falls back to home/.config when unset", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")
		result := xdgConfigHome()
		// should end with .config
		assert.Contains(t, result, ".config")
	})
}

// --- normalizeGiteaURL edge case ---
// Prevents panic or wrong comparison when URL is malformed (no scheme)

func TestNormalizeGiteaURL_Invalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
		want string
	}{
		{"bare host with trailing slash", "gitea.local/", ""},
		{"bare host no scheme", "gitea.local", ""},
		{"invalid scheme with trailing slash", "://gitea.local/", ""},
		{"invalid scheme no trailing slash", "://gitea.local", ""},
		{"unsupported scheme", "ssh://gitea.local", ""},
		{"userinfo", "https://user@gitea.local", ""},
		{"query", "https://gitea.local?other", ""},
		{"fragment", "https://gitea.local#other", ""},
		{"invalid port", "https://gitea.local:bad", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := normalizeGiteaURL(tt.url)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- person_info edge cases not in existing tests ---

func TestFormatNameAsDisplay_Unicode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"cjk name", "太郎 山田", "太郎 山."},
		{"accented name", "José García", "José G."},
		{"single unicode char", "李", "李"},
		{"emoji name gracefully handled", "🎉 Party", "🎉 P."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, formatNameAsDisplay(tt.input))
		})
	}
}

func TestCapitalize_Unicode(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "Über", capitalize("über"))
	assert.Equal(t, "Ñoño", capitalize("ñoño"))
}
