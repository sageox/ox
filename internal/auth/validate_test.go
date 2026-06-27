package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// ValidateTokenServerSide must route oxp_ PATs to /api/v1/auth/me (the PAT
// introspection route) and every other token to /oauth2/userinfo. The bug this
// locks out: a PAT sent to /oauth2/userinfo (OAuth/opaque tokens only) always
// 401s with "Token not found", so `ox status` reported a valid PAT as
// unauthenticated even though the server accepted it on the PAT path.
func TestValidateTokenServerSide_RoutesPATToAuthMe(t *testing.T) {
	// httptest serves plain HTTP; without this the endpoint normalizer would
	// reject the http:// scheme before any request is made.
	t.Setenv("OX_ALLOW_PLAINTEXT_ENDPOINT", "1")

	tests := []struct {
		name     string
		token    string
		wantPath string
	}{
		{name: "PAT routes to auth/me", token: "oxp_abc123body_crc01", wantPath: AuthMeEndpoint},
		{name: "opaque access token routes to userinfo", token: "opaque-access-token", wantPath: UserInfoEndpoint},
		{name: "JWT routes to userinfo", token: "header.payload.sig", wantPath: UserInfoEndpoint},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath, gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotAuth = r.Header.Get("Authorization")
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			if err := ValidateTokenServerSide(srv.URL, tt.token); err != nil {
				t.Fatalf("expected success (200), got error: %v", err)
			}
			if gotPath != tt.wantPath {
				t.Errorf("token %q validated at path %q, want %q", tt.token, gotPath, tt.wantPath)
			}
			if want := "Bearer " + tt.token; gotAuth != want {
				t.Errorf("Authorization header = %q, want %q", gotAuth, want)
			}
		})
	}
}

// A non-200 from the validation endpoint must surface as an error (the caller
// treats nil error as "authenticated").
func TestValidateTokenServerSide_RejectsNon200(t *testing.T) {
	t.Setenv("OX_ALLOW_PLAINTEXT_ENDPOINT", "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	if err := ValidateTokenServerSide(srv.URL, "oxp_revoked_token_xyz"); err == nil {
		t.Fatal("expected error for 401, got nil")
	}
}
