package identity

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/runtime"
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

// --- F. AI coworker of a team token ---

// Failure prevented: a coworker's work is attributed to its machine's git
// identity, or its name is abbreviated like a person's.
func TestResolveAttribution_TeamCoworker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"active":true,"principal_kind":"team-service","coworker":{"id":"agt_rip","display_name":"Rip Van Winkle"}}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("SAGEOX_ENDPOINT", srv.URL)
	t.Setenv("SAGEOX_TOKEN", "oxt_test_1ljPfr")

	assert.Equal(t, Attribution{
		Username:    "rip-van-winkle",
		Name:        "Rip Van Winkle",
		DisplayName: "Rip Van Winkle",
		AI:          true,
	}, ResolveAttribution(srv.URL, "port8080"))
}

// --- G. Personal token in an Amp project orb ---

// Failure prevented (sageox/ox#1425): an Amp project orb commits as its
// project's commit author, "Amp", so git's identity named and credited every
// session there "amp". The verified owner of SAGEOX_TOKEN must name the work
// in an orb; on a laptop the slug stays git's so existing session names hold.
func TestResolveAttribution_PersonalTokenInAnOrb(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"user":{"id":"usr_avery","email":"avery@test.sageox.ai","name":"Avery Quinn"}}`)
	}))
	t.Cleanup(srv.Close)
	gitConfig := t.TempDir() + "/gitconfig"
	require.NoError(t, os.WriteFile(gitConfig, []byte("[user]\n\tname = Amp\n\temail = amp@test.sageox.ai\n"), 0o600))

	for _, tc := range []struct {
		name         string
		ampOrb       string
		wantUsername string
	}{
		{"Amp orb: the token's owner names the session", "1", "avery"},
		{"laptop: the slug stays git's", "", "amp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			t.Setenv("SAGEOX_ENDPOINT", srv.URL)
			t.Setenv("SAGEOX_TOKEN", "oxp_test_4bDZfN")
			t.Setenv("GIT_CONFIG_GLOBAL", gitConfig)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			t.Chdir(t.TempDir())
			t.Setenv("AMP_ORB", tc.ampOrb)
			runtime.Reset()
			t.Cleanup(runtime.Reset)

			attr := ResolveAttribution(srv.URL, "")

			assert.Equal(t, tc.wantUsername, attr.Username)
			assert.Equal(t, "avery@test.sageox.ai", attr.Email, "the verified owner beats git")
			assert.Equal(t, "Avery Quinn", attr.Name)
			assert.Equal(t, "Avery Q.", attr.DisplayName)
		})
	}
}
