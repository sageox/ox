package lfs

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateActionHref_RejectsCrossHost is the load-bearing test for
// ox-90gh: an action whose Href points at a different host than the
// batch response came from must be rejected before any bearer header is
// shipped to the attacker.
func TestValidateActionHref_RejectsCrossHost(t *testing.T) {
	bad := &Action{
		Href:          "https://attacker.example/leak",
		TrustedHost:   "lfs.sageox.ai",
		TrustedScheme: "https",
		Header:        map[string]string{"Authorization": "Bearer SECRET"},
	}
	err := validateActionHref(bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "trusted host")
}

func TestValidateActionHref_AllowsSameHost(t *testing.T) {
	ok := &Action{
		Href:          "https://lfs.sageox.ai/objects/abc",
		TrustedHost:   "lfs.sageox.ai",
		TrustedScheme: "https",
	}
	assert.NoError(t, validateActionHref(ok))
}

func TestValidateActionHref_AllowsLoopbackHTTP(t *testing.T) {
	// httptest servers run on 127.0.0.1; allow http for them.
	for _, href := range []string{
		"http://127.0.0.1:9999/o",
		"http://localhost:8080/o",
		"http://[::1]:8080/o",
	} {
		err := validateActionHref(&Action{Href: href})
		assert.NoError(t, err, "loopback http should be allowed: %s", href)
	}
}

func TestValidateActionHref_RejectsPlainHTTPNonLoopback(t *testing.T) {
	err := validateActionHref(&Action{
		Href:        "http://lfs.sageox.ai/o",
		TrustedHost: "lfs.sageox.ai",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "https required")
}

func TestValidateActionHref_RejectsEmptyHref(t *testing.T) {
	err := validateActionHref(&Action{Href: "", TrustedHost: "lfs.sageox.ai"})
	require.Error(t, err)
}

func TestValidateActionHref_NoStampDefaultsToAllow(t *testing.T) {
	// Backwards compat for tests that build Actions directly without
	// going through doBatch — TrustedHost==""  means "no constraint".
	err := validateActionHref(&Action{Href: "https://anything.example/x"})
	assert.NoError(t, err)
}

// TestUploadObject_RejectsMismatchedHost asserts the validator gate
// fires before any bytes (or, more importantly, any header) is sent.
func TestUploadObject_RejectsMismatchedHost(t *testing.T) {
	// Set up a malicious "attacker" that would receive the bearer if the
	// gate weren't in place. The test fails if the server is even hit.
	hit := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit <- struct{}{}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	bad := &Action{
		Href:          srv.URL + "/leak",
		TrustedHost:   "lfs.sageox.ai", // doesn't match srv.URL's host
		TrustedScheme: "https",
		Header:        map[string]string{"Authorization": "Bearer SECRET-LEAKED"},
	}
	err := UploadObject(bad, []byte("payload"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "trusted host")
	assert.Empty(t, hit, "attacker server must not have been contacted")
}

// TestLFSClient_RedirectsRefused verifies the http.Client refuses to
// follow redirects on action requests. A real attacker could chain a
// 200 from the original host followed by a 302 to attacker.example —
// http.ErrUseLastResponse stops that.
func TestLFSClient_RedirectsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://attacker.example/leak", http.StatusFound)
	}))
	defer srv.Close()

	action := &Action{
		Href: srv.URL + "/object",
		// TrustedHost="" — accept the original host; the redirect is
		// what we're testing the resistance to.
	}
	_, err := DownloadObject(action)
	// DownloadObject returns nil error when the response is 200..299.
	// A 302 from the test server WITH http.ErrUseLastResponse on the
	// CheckRedirect means resp.StatusCode stays at 302 — caller sees
	// "download returned HTTP 302" rather than following.
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "302")
}
