package identity

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- A. osUsername fallback ---

func TestOsUsername_FromUSER(t *testing.T) {
	t.Setenv("USER", "testuser")
	t.Setenv("USERNAME", "")
	assert.Equal(t, "testuser", osUsername())
}

func TestOsUsername_FromUSERNAME(t *testing.T) {
	t.Setenv("USER", "")
	t.Setenv("USERNAME", "winuser")
	assert.Equal(t, "winuser", osUsername())
}

func TestOsUsername_Empty(t *testing.T) {
	t.Setenv("USER", "")
	t.Setenv("USERNAME", "")
	assert.Equal(t, "", osUsername())
}

// --- B. Attribution field guarantees ---
// Verifies that attribution fields always satisfy their contracts:
// Username: non-empty, lowercase
// Name: non-empty
// DisplayName: non-empty

func TestResolveAttribution_NoAuth_FieldGuarantees(t *testing.T) {
	// empty endpoint means no OAuth data — falls back to git/OS
	attr := ResolveAttribution("", "")

	assert.NotEmpty(t, attr.Username, "Username must never be empty")
	assert.Equal(t, attr.Username, strings.ToLower(attr.Username), "Username must be lowercase")
	assert.NotEmpty(t, attr.Name, "Name must never be empty")
	assert.NotEmpty(t, attr.DisplayName, "DisplayName must never be empty")
}

func TestResolveAttribution_NoAuth_NoGit_FallsBackToOS(t *testing.T) {
	// force "no git" by removing git from PATH so DetectGitIdentity fails
	t.Setenv("PATH", t.TempDir()) // empty dir — no git binary
	t.Setenv("USER", "testosuser")
	attr := ResolveAttribution("https://nonexistent.example.com", "")

	assert.Equal(t, "testosuser", attr.Username, "should fall back to OS username when git unavailable")
	assert.Equal(t, attr.Username, strings.ToLower(attr.Username))
}

func TestResolveAttribution_ConfigDisplayName_Overrides(t *testing.T) {
	// explicit configDisplayName should be used as-is
	attr := ResolveAttribution("", "port8080")
	assert.Equal(t, "port8080", attr.DisplayName)
}

func TestResolveAttribution_ConfigDisplayName_TrimmedWhitespace(t *testing.T) {
	attr := ResolveAttribution("", "  port8080  ")
	assert.Equal(t, "port8080", attr.DisplayName)
}

func TestResolveAttribution_EmptyConfigDisplayName_DerivesFallback(t *testing.T) {
	attr := ResolveAttribution("", "")
	// DisplayName should be derived from available sources, never empty
	assert.NotEmpty(t, attr.DisplayName)
}

// --- C. Convenience functions ---
// Verifies that convenience functions return the same values as ResolveAttribution

func TestAttributionUsername_MatchesResolve(t *testing.T) {
	attr := ResolveAttribution("", "")
	assert.Equal(t, attr.Username, AttributionUsername("", ""))
}

func TestAttributionEmail_MatchesResolve(t *testing.T) {
	attr := ResolveAttribution("", "")
	assert.Equal(t, attr.Email, AttributionEmail("", ""))
}

func TestAttributionName_MatchesResolve(t *testing.T) {
	attr := ResolveAttribution("", "")
	assert.Equal(t, attr.Name, AttributionName("", ""))
}

func TestAttributionDisplayName_MatchesResolve(t *testing.T) {
	attr := ResolveAttribution("", "")
	assert.Equal(t, attr.DisplayName, AttributionDisplayName("", ""))
}

// --- D. Privacy guarantees ---
// Verifies that DisplayName is not the full Name when both are available

func TestAttribution_DisplayNameIsNotFullName_WhenMultiPart(t *testing.T) {
	// this test relies on having a multi-part name from git config.
	// we can't control git config in tests, so we test the derivation
	// logic via formatNameAsDisplay directly — that's tested in person_info_test.go.
	// here we just verify the struct contract.
	attr := ResolveAttribution("", "CustomDisplay")
	assert.Equal(t, "CustomDisplay", attr.DisplayName)
	// Name comes from git/OS, not from configDisplayName
	assert.NotEqual(t, "CustomDisplay", attr.Name,
		"Name should come from git/OS, not from configDisplayName")
}

// --- E. Bogus endpoint ---
// Verifies graceful handling of invalid endpoints

func TestResolveAttribution_BogusEndpoint(t *testing.T) {
	attr := ResolveAttribution("https://this-does-not-exist.example.com:99999", "")
	assert.NotEmpty(t, attr.Username, "should fall back gracefully with bogus endpoint")
	assert.NotEmpty(t, attr.DisplayName)
}
