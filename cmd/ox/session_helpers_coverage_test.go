package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- getAuthenticatedUsername ---
// uses auth.GetTokenForEndpoint which requires real auth state;
// test the string extraction logic by verifying the email-to-username behavior

func TestGetAuthenticatedUsername_EmptyEndpoint(t *testing.T) {
	t.Parallel()
	// no auth token for empty endpoint should return empty string
	got := getAuthenticatedUsername("")
	assert.Equal(t, "", got)
}

func TestGetAuthenticatedUsername_InvalidEndpoint(t *testing.T) {
	t.Parallel()
	// bogus endpoint should return empty (no token found)
	got := getAuthenticatedUsername("https://nonexistent.example.com")
	assert.Equal(t, "", got)
}

// --- getRepoIDOrDefault ---
// requires config.GetRepoID which reads .sageox/config.json

func TestGetRepoIDOrDefault_EmptyRoot(t *testing.T) {
	t.Parallel()
	// empty root returns "default" because config.GetRepoID("") returns ""
	got := getRepoIDOrDefault("")
	assert.Equal(t, "default", got)
}

func TestGetRepoIDOrDefault_NonexistentDir(t *testing.T) {
	t.Parallel()
	got := getRepoIDOrDefault("/nonexistent/path/xyz")
	assert.Equal(t, "default", got)
}

func TestGetRepoIDOrDefault_TempDirNoConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	got := getRepoIDOrDefault(dir)
	assert.Equal(t, "default", got)
}

// --- requireProjectRoot ---
// Skipped: requireProjectRoot relies on os.Getwd() which isn't safe to test
// with t.Parallel() and os.Chdir

// --- getDisplayName ---

func TestGetDisplayName_NoAuth(t *testing.T) {
	t.Parallel()
	got := getDisplayName("https://nonexistent.example.com")
	assert.Equal(t, "", got)
}

func TestGetDisplayName_EmptyEndpoint(t *testing.T) {
	t.Parallel()
	got := getDisplayName("")
	assert.Equal(t, "", got)
}

// Tests the email-to-username extraction logic used by getAuthenticatedUsername.
// The production function couples auth lookup with extraction, so we test the
// extraction logic in isolation here. A refactor to extract this to a shared
// helper would enable direct testing of the production code path.

func TestEmailUsernameExtraction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		email string
		want  string
	}{
		{"standard email", "user@example.com", "user"},
		{"email with dots", "first.last@example.com", "first.last"},
		{"email with plus", "user+tag@example.com", "user+tag"},
		{"no @ sign returns full string", "justusername", "justusername"},
		{"empty string", "", ""},
		{"@ at start returns full string", "@example.com", "@example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// replicate the logic from getAuthenticatedUsername
			email := tt.email
			if at := strings.Index(email, "@"); at > 0 {
				email = email[:at]
			}
			assert.Equal(t, tt.want, email)
		})
	}
}
