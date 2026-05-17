package auth

import (
	"strings"
	"testing"
)

// TestRedactedBody pins the trust contract for what reaches user-visible
// error messages and slog output from auth-flow HTTP responses. Tokens and
// session IDs returned in misbehaving server responses must never appear in
// the returned string; only allowlisted error fields are preserved.
//
// Failure prevented: SECREVIEW BYOK P1-B. A debug log or error message shared
// in a support bundle / bug report would otherwise leak credential material
// from an unexpected server response shape.
func TestRedactedBody(t *testing.T) {
	cases := []struct {
		name           string
		body           string
		mustNotContain []string
		mustContain    []string
	}{
		{
			name:           "empty body",
			body:           "",
			mustContain:    []string{"(empty body)"},
			mustNotContain: nil,
		},
		{
			name: "json with error fields — allowlisted, safe to keep",
			body: `{"error":"invalid_grant","error_description":"token expired"}`,
			mustContain: []string{
				`"error":"invalid_grant"`,
				`"error_description":"token expired"`,
			},
			mustNotContain: nil,
		},
		{
			name: "misbehaving server includes refresh_token in error envelope",
			body: `{"error":"server_error","refresh_token":"rt_abc123_secret","access_token":"at_xyz"}`,
			mustContain: []string{
				`"error":"server_error"`,
			},
			mustNotContain: []string{
				"rt_abc123_secret",
				"at_xyz",
				"refresh_token",
				"access_token",
			},
		},
		{
			name: "data-envelope with embedded credentials — flag envelope, drop contents",
			body: `{"data":{"refresh_token":"rt_secret","session_token":"st_secret"}}`,
			mustContain: []string{
				`"_envelope":"data"`,
			},
			mustNotContain: []string{
				"rt_secret",
				"st_secret",
				"refresh_token",
				"session_token",
			},
		},
		{
			name: "non-json body — reported as shape only",
			body: "<html>blah token=secret123</html>",
			mustContain: []string{
				"(non-json body",
			},
			mustNotContain: []string{
				"secret123",
				"token=",
			},
		},
		{
			name: "json without any allowlisted field — no leak",
			body: `{"refresh_token":"rt_secret","internal_id":"abc"}`,
			mustContain: []string{
				"json body",
				"no allowlisted error fields",
			},
			mustNotContain: []string{
				"rt_secret",
				"refresh_token",
				"internal_id",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactedBody([]byte(tc.body), "application/json")
			for _, must := range tc.mustContain {
				if !strings.Contains(got, must) {
					t.Errorf("redactedBody(%q) = %q; expected to contain %q", tc.body, got, must)
				}
			}
			for _, banned := range tc.mustNotContain {
				if strings.Contains(got, banned) {
					t.Errorf("redactedBody(%q) = %q; MUST NOT contain %q (credential leak)", tc.body, got, banned)
				}
			}
		})
	}
}
