package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Every bearer token family (personal oxp_, team oxt_, opaque, JWT-shaped)
// must reach the server the same way — the whole point of the unified endpoint
// is that the CLI never branches on the token again.
//
// What this DOES catch, per family: a request sent to any path other than
// IntrospectEndpoint (URL-join drift, a stray double slash, a changed suffix),
// a wrong HTTP method, and a mangled or missing Authorization header. The stub
// 404s anything that is not the introspection path, so a routing regression
// fails as an outright rejection rather than as a confusing string mismatch.
//
// What it does NOT prove: that the old per-family branch is gone. There is no
// branch left in the production code to exercise, so this is a routing and
// header contract test, not a proof of absence.
func TestValidateTokenServerSide_FamilyAgnosticRouting(t *testing.T) {
	// httptest serves plain HTTP; without this the endpoint normalizer would
	// reject the http:// scheme before any request is made.
	t.Setenv("OX_ALLOW_PLAINTEXT_ENDPOINT", "1")

	tokens := []string{
		"oxp_abc123body_crc01",
		"oxt_abc123body_crc01",
		"opaque-access-token",
		"header.payload.sig",
	}
	for _, tok := range tokens {
		t.Run(tok, func(t *testing.T) {
			var gotPath, gotMethod, gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotMethod = r.Method
				gotAuth = r.Header.Get("Authorization")
				if r.URL.Path != IntrospectEndpoint {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(IntrospectResult{Active: true, PrincipalKind: PrincipalKindUser})
			}))
			defer srv.Close()

			if err := ValidateTokenServerSide(srv.URL, tok); err != nil {
				t.Fatalf("expected success (200) at %s, got error: %v (requested path %q)", IntrospectEndpoint, err, gotPath)
			}
			if gotPath != IntrospectEndpoint {
				t.Errorf("token %q validated at path %q, want %q", tok, gotPath, IntrospectEndpoint)
			}
			if gotMethod != http.MethodGet {
				t.Errorf("token %q validated with method %q, want %q", tok, gotMethod, http.MethodGet)
			}
			if want := "Bearer " + tok; gotAuth != want {
				t.Errorf("Authorization header = %q, want %q", gotAuth, want)
			}
		})
	}
}

// A non-200 from the introspection endpoint must surface as an error (the
// caller treats nil error as "authenticated"), for any token family.
func TestValidateTokenServerSide_RejectsNon200(t *testing.T) {
	t.Setenv("OX_ALLOW_PLAINTEXT_ENDPOINT", "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	if err := ValidateTokenServerSide(srv.URL, "oxt_revoked_token_xyz"); err == nil {
		t.Fatal("expected error for 401, got nil")
	}
}

// Introspect must parse the frozen user-principal response shape (see the
// IntrospectResult doc comment) — this is what cmd/ox/status.go relies on to
// decide whether to render "User <name> <email>".
func TestIntrospect_ParsesUserPrincipal(t *testing.T) {
	t.Setenv("OX_ALLOW_PLAINTEXT_ENDPOINT", "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"active": true,
			"principal_kind": "user",
			"scope": "full-access",
			"token_type": "Bearer",
			"expires_at": null,
			"user": {"id": "usr_abc", "email": "person-a@test.sageox.ai", "name": "Person A", "tier": "pro"},
			"team": null,
			"token": null
		}`))
	}))
	defer srv.Close()

	result, err := Introspect(srv.URL, "oxp_test_4bDZfN")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.PrincipalKind != "user" {
		t.Errorf("PrincipalKind = %q, want %q", result.PrincipalKind, "user")
	}
	if result.User == nil || result.User.Email != "person-a@test.sageox.ai" {
		t.Errorf("User = %+v, want email person-a@test.sageox.ai", result.User)
	}
	if result.Team != nil {
		t.Errorf("Team = %+v, want nil for a user principal", result.Team)
	}
}

// Introspect must parse the frozen team-principal response shape —
// PrincipalKindTeamService plus Team.TeamID and Token.Name populated.
// This is the exact response cmd/ox/status.go's "acting as team <team_id>"
// rendering depends on.
func TestIntrospect_ParsesTeamPrincipal(t *testing.T) {
	t.Setenv("OX_ALLOW_PLAINTEXT_ENDPOINT", "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"active": true,
			"principal_kind": "team-service",
			"scope": "read-only",
			"token_type": "Bearer",
			"expires_at": "2026-08-16T19:42:43Z",
			"user": null,
			"team": {"team_id": "team_abc123"},
			"token": {"prefix": "oxt_ab12", "name": "ci-deploy"}
		}`))
	}))
	defer srv.Close()

	result, err := Introspect(srv.URL, "oxt_test_1ljPfr")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.PrincipalKind != PrincipalKindTeamService {
		t.Errorf("PrincipalKind = %q, want %q", result.PrincipalKind, PrincipalKindTeamService)
	}
	if result.Team == nil || result.Team.TeamID != "team_abc123" {
		t.Errorf("Team = %+v, want team_id team_abc123", result.Team)
	}
	if result.Token == nil || result.Token.Name != "ci-deploy" {
		t.Errorf("Token = %+v, want name ci-deploy", result.Token)
	}
	if result.User != nil {
		t.Errorf("User = %+v, want nil for a team principal", result.User)
	}
}

// A 200 with malformed JSON must not be treated as success — a caller acting
// on a zero-value IntrospectResult would render a blank identity as if it
// were a real (if empty) principal.
func TestIntrospect_MalformedBodyRejected(t *testing.T) {
	t.Setenv("OX_ALLOW_PLAINTEXT_ENDPOINT", "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	if _, err := Introspect(srv.URL, "oxp_test_4bDZfN"); err == nil {
		t.Fatal("expected error for malformed JSON body, got nil")
	}
}

// A 200 with active:false must be rejected rather than treated as a valid
// (if inactive) principal. The server answers a bare 401 instead of returning
// this shape, but the client must not trust a 200 blindly.
func TestIntrospect_InactiveTokenRejected(t *testing.T) {
	t.Setenv("OX_ALLOW_PLAINTEXT_ENDPOINT", "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(IntrospectResult{Active: false, PrincipalKind: "user"})
	}))
	defer srv.Close()

	if _, err := Introspect(srv.URL, "oxp_test_4bDZfN"); err == nil {
		t.Fatal("expected error for active:false, got nil")
	}
}

// "Your credential is unusable" and "you have no credential" are different
// facts with different remedies, and the auth checks must not flatten one into
// the other. A CI operator whose team service token was truncated in transit
// gets "NOT LOGGED IN → run `ox login`" if the reason is swallowed — and
// `ox login` mints personal tokens, so it cannot replace a team token. That is
// the wrong remedy delivered confidently.
//
// The second half of this table is the counterweight: an ordinary
// unauthenticated user must still be (false, nil). Turning "no credential"
// into a hard error would be a far worse regression than the one being fixed.
func TestAuthChecks_MalformedEnvTokenSurfacesReason(t *testing.T) {
	const ep = "https://sageox.ai"

	checks := []struct {
		name string
		fn   func(string) (bool, error)
	}{
		{"IsAuthenticatedForEndpoint", IsAuthenticatedForEndpoint},
		{"IsAuthCredentialValidForEndpoint", IsAuthCredentialValidForEndpoint},
	}

	for _, c := range checks {
		t.Run(c.name+"/malformed env token", func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("SAGEOX_ENDPOINT", "")

			// A real team token with the last character of its CRC suffix
			// lost — the exact shape a truncated copy/paste produces.
			full := validSageOxTestToken(TeamTokenPrefix, "ci_service")
			truncated := full[:len(full)-1]
			t.Setenv(EnvVarToken, truncated)

			ok, err := c.fn(ep)
			if ok {
				t.Error("reported authenticated on a credential that failed the local format check")
			}
			if !errors.Is(err, ErrEnvTokenMalformed) {
				t.Errorf("err = %v, want an error wrapping ErrEnvTokenMalformed; swallowing it renders "+
					"\"not logged in\" and sends the operator to a command that cannot fix a team token", err)
			}
			if err != nil && strings.Contains(err.Error(), truncated) {
				t.Errorf("error echoes the credential value: %q", err.Error())
			}
		})

		t.Run(c.name+"/no credential at all", func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("SAGEOX_ENDPOINT", "")
			t.Setenv(EnvVarToken, "")

			ok, err := c.fn(ep)
			if ok {
				t.Error("reported authenticated with no credential present")
			}
			if err != nil {
				t.Errorf("err = %v, want nil: being logged out is a normal state, not a failure. "+
					"An error here turns every unauthenticated invocation into a hard error", err)
			}
		})

		// The row above cannot catch an over-broad fix, because a logged-out
		// user produces no error at all — the error branch is never entered.
		// This one does: an unreadable auth.json is a genuine resolution
		// failure that must STILL degrade to "not authenticated, no error".
		// Only the env-token sentinel is worth reporting.
		t.Run(c.name+"/unreadable auth.json still degrades quietly", func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("SAGEOX_ENDPOINT", "")
			t.Setenv(EnvVarToken, "")

			authPath, err := GetAuthFilePath()
			if err != nil {
				t.Fatalf("GetAuthFilePath: %v", err)
			}
			if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			if err := os.WriteFile(authPath, []byte("{ this is not valid json"), 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			ok, err := c.fn(ep)
			if ok {
				t.Error("reported authenticated from an unreadable auth.json")
			}
			if err != nil {
				t.Errorf("err = %v, want nil: only ErrEnvTokenMalformed is worth surfacing here. "+
					"Reporting every resolution failure would make a corrupt auth.json a hard error "+
					"for commands that only wanted to know whether to show a login hint", err)
			}
		})
	}
}

// "The server never answered" and "the server said no" are different facts
// with different remedies. Conflating them tells a developer who is merely
// offline that their credential was rejected and advises a needless rotation.
// Both halves of this test matter: a sentinel that is always true is useless,
// so a live 401 must NOT satisfy it.
func TestIntrospect_UnreachableIsDistinguishable(t *testing.T) {
	t.Setenv("OX_ALLOW_PLAINTEXT_ENDPOINT", "1")

	// A well-formed URL with nothing listening behind it.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	_, err := Introspect(deadURL, "oxp_test_4bDZfN")
	if err == nil {
		t.Fatal("expected an error from an unreachable endpoint, got nil")
	}
	if !errors.Is(err, ErrEndpointUnreachable) {
		t.Errorf("unreachable endpoint did not yield ErrEndpointUnreachable: %v", err)
	}
	// The transport cause must stay discoverable — a caller diagnosing a
	// proxy, DNS, or TLS failure needs the underlying error, not just the tag.
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		t.Errorf("underlying transport error is not discoverable via errors.As: %v", err)
	}

	// A live server answering 401 is a rejection, not an unreachable endpoint.
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer live.Close()

	_, err = Introspect(live.URL, "oxp_test_4bDZfN")
	if err == nil {
		t.Fatal("expected an error from a 401, got nil")
	}
	if errors.Is(err, ErrEndpointUnreachable) {
		t.Errorf("a 401 from a live server must not be classified unreachable: %v", err)
	}
}

// The returned error is printed to the terminal by `ox status` and captured by
// CI logs. A server that echoes the presented credential back in its error
// body must not be able to leak it there. The slog ReplaceAttr redaction hook
// does NOT cover this path — it only scrubs log attributes, never a returned
// error — so the redaction has to happen at the format site.
func TestIntrospect_ErrorBodyIsRedacted(t *testing.T) {
	t.Setenv("OX_ALLOW_PLAINTEXT_ENDPOINT", "1")

	const leaked = "oxp_deadbeefdeadbeefdead"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprintf(w, `{"error":"invalid_token","error_description":"presented credential %s was rejected"}`, leaked)
	}))
	defer srv.Close()

	_, err := Introspect(srv.URL, leaked)
	if err == nil {
		t.Fatal("expected error for 401, got nil")
	}
	if strings.Contains(err.Error(), leaked) {
		t.Errorf("returned error leaks the credential: %q", err.Error())
	}
	// The redaction must not swallow the message the operator needs.
	for _, want := range []string{"presented credential", "was rejected"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("returned error = %q, want it to still contain %q", err.Error(), want)
		}
	}
}

// Go's JSON decoder saves-and-continues on a type mismatch: it reports an
// *json.UnmarshalTypeError but still populates every field it could decode.
// Gating the error message on a nil unmarshal error therefore throws away a
// perfectly good error_description whenever a sibling field has the wrong
// type, and the operator gets a bare status code instead of the reason.
func TestIntrospect_ErrorFieldSurvivesTypeMismatch(t *testing.T) {
	t.Setenv("OX_ALLOW_PLAINTEXT_ENDPOINT", "1")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":123,"error_description":"session expired"}`))
	}))
	defer srv.Close()

	_, err := Introspect(srv.URL, "oxp_test_4bDZfN")
	if err == nil {
		t.Fatal("expected error for 401, got nil")
	}
	if !strings.Contains(err.Error(), "session expired") {
		t.Errorf("returned error = %q, want it to surface the error_description %q", err.Error(), "session expired")
	}
}

// expires_at has no consumer that would notice a zero value, so no encoding of
// it may turn a live, valid credential into "malformed introspection
// response". A numeric unix epoch is the RFC 7662 convention for this field and
// is a completely plausible server encoding; an empty string and an
// unparseable value are plausible too. Every one of them must still yield a
// usable principal.
func TestIntrospect_FlexibleExpiresAt(t *testing.T) {
	t.Setenv("OX_ALLOW_PLAINTEXT_ENDPOINT", "1")

	tests := []struct {
		name string
		raw  string // the raw JSON value for expires_at
	}{
		{"rfc3339 string", `"2026-08-16T19:42:43Z"`},
		{"json null", `null`},
		{"empty string", `""`},
		{"unix seconds integer", `1786000000`},
		{"unix seconds float", `1786000000.5`},
		{"unparseable string", `"tomorrow-ish"`},
		{"unexpected object", `{"seconds":1786000000}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := fmt.Sprintf(`{
				"active": true,
				"principal_kind": "user",
				"scope": "full-access",
				"token_type": "Bearer",
				"expires_at": %s,
				"user": {"id": "usr_abc", "email": "person-a@test.sageox.ai", "name": "Person A", "tier": "pro"},
				"team": null,
				"token": null
			}`, tt.raw)

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()

			result, err := Introspect(srv.URL, "oxp_test_4bDZfN")
			if err != nil {
				t.Fatalf("expires_at %s made the whole parse fail: %v", tt.raw, err)
			}
			if !result.Active {
				t.Error("Active = false, want true")
			}
			if result.PrincipalKind != PrincipalKindUser {
				t.Errorf("PrincipalKind = %q, want %q", result.PrincipalKind, PrincipalKindUser)
			}
			if result.User == nil || result.User.Email != "person-a@test.sageox.ai" {
				t.Errorf("User = %+v, want the principal intact", result.User)
			}
		})
	}
}

// A wrong-typed field the CLI never reads is not grounds to throw away a
// server answer that decoded cleanly where it matters. This pins the boundary
// in BOTH directions: tolerate a type error only when active and
// principal_kind both survived, and never let a type error on active itself be
// tolerated into looking active.
func TestIntrospect_TolerantOfUnreadFieldTypeErrors(t *testing.T) {
	t.Setenv("OX_ALLOW_PLAINTEXT_ENDPOINT", "1")

	const user = `"user": {"id": "usr_abc", "email": "person-a@test.sageox.ai", "name": "Person A", "tier": "pro"}`

	tests := []struct {
		name       string
		body       string
		wantAccept bool
		wantErr    string // substring the rejection must carry, when the wording itself matters
		why        string
	}{
		{
			name:       "wrong-typed scope",
			body:       `{"active":true,"principal_kind":"user","scope":123,` + user + `}`,
			wantAccept: true,
			why:        "scope has no consumer; the validity question was answered",
		},
		{
			name:       "wrong-typed token_type",
			body:       `{"active":true,"principal_kind":"user","token_type":42,` + user + `}`,
			wantAccept: true,
			why:        "token_type has no consumer",
		},
		{
			name:       "wrong-typed token object",
			body:       `{"active":true,"principal_kind":"user","token":"oxt_ab12",` + user + `}`,
			wantAccept: true,
			why:        "token is display-only; a nil Token renders a generic label",
		},
		{
			name:       "wrong-typed user object",
			body:       `{"active":true,"principal_kind":"user","user":"person-a"}`,
			wantAccept: true,
			why:        "identity rendering degrades to generic; the credential is still live",
		},
		{
			name:       "wrong-typed active",
			body:       `{"active":"yes","principal_kind":"user",` + user + `}`,
			wantAccept: false,
			// The wording is load-bearing here: the server did NOT report this
			// token inactive, we failed to read what it reported. Falling
			// through to the downstream inactive check would reject the token
			// (right outcome) while telling the operator something false about
			// why (wrong diagnosis, and it points at the credential instead of
			// at the server response).
			wantErr: "malformed introspection response",
			why:     "active never decoded; treating the zero value as a verdict would fail open",
		},
		{
			name:       "missing principal_kind alongside a type error",
			body:       `{"active":true,"scope":123,` + user + `}`,
			wantAccept: false,
			why:        "the server did not say what this authenticates as",
		},
		{
			name:       "active false alongside a type error",
			body:       `{"active":false,"principal_kind":"user","scope":123}`,
			wantAccept: false,
			why:        "an explicit inactive verdict still wins",
		},
		{
			name:       "syntax error mid-body",
			body:       `{"active":true,"principal_kind":"user",`,
			wantAccept: false,
			why:        "nothing decoded; active being false is an artifact of the parse, not a statement",
		},
		{
			name:       "not json at all",
			body:       `not json`,
			wantAccept: false,
			why:        "same asymmetry: a syntax error is different in kind from a type error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()

			result, err := Introspect(srv.URL, "oxp_test_4bDZfN")

			if !tt.wantAccept {
				if err == nil {
					t.Fatalf("body %s was accepted; it must be rejected (%s)", tt.body, tt.why)
				}
				if tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("rejection = %q, want it to contain %q (%s)", err.Error(), tt.wantErr, tt.why)
				}
				return
			}
			if err != nil {
				t.Fatalf("body %s was rejected (%v); it must be accepted (%s)", tt.body, err, tt.why)
			}
			if !result.Active {
				t.Error("Active = false, want true")
			}
			if result.PrincipalKind != PrincipalKindUser {
				t.Errorf("PrincipalKind = %q, want %q", result.PrincipalKind, PrincipalKindUser)
			}
		})
	}
}

// The success path reads the response body too, so an unbounded io.ReadAll
// here would let any endpoint the CLI is pointed at drive an unbounded
// allocation. The observable consequence of the cap is that an oversized body
// arrives truncated and therefore fails to parse — without the cap this same
// body parses cleanly, which is exactly the unbounded buffering we are
// refusing to do.
func TestIntrospect_BodyIsBounded(t *testing.T) {
	t.Setenv("OX_ALLOW_PLAINTEXT_ENDPOINT", "1")

	// Well-formed JSON, far larger than any legitimate introspection response.
	oversized := `{"active":true,"principal_kind":"user","scope":"` +
		strings.Repeat("x", 3<<20) + `","user":null,"team":null,"token":null}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, oversized)
	}))
	defer srv.Close()

	if _, err := Introspect(srv.URL, "oxp_test_4bDZfN"); err == nil {
		t.Fatalf("a %d-byte body was buffered and parsed in full; the read is not bounded", len(oversized))
	}
}

// Tolerance is only half the requirement: "never fails the parse" would also be
// satisfied by a type that silently discarded every value. The encodings we
// claim to support must actually decode to the right instant, and only the
// genuinely unrecognizable ones may fall back to the zero time.
func TestFlexTime_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		wantMillis int64 // 0 means "zero time"
	}{
		{"rfc3339 utc", `"2026-08-16T19:42:43Z"`, time.Date(2026, 8, 16, 19, 42, 43, 0, time.UTC).UnixMilli()},
		{"rfc3339 offset", `"2026-08-16T14:42:43-05:00"`, time.Date(2026, 8, 16, 19, 42, 43, 0, time.UTC).UnixMilli()},
		{"unix seconds integer", `1786000000`, 1786000000 * 1000},
		{"unix seconds float", `1786000000.5`, 1786000000*1000 + 500},
		{"json null", `null`, 0},
		{"empty string", `""`, 0},
		{"whitespace string", `"   "`, 0},
		{"unparseable string", `"tomorrow-ish"`, 0},
		{"unexpected object", `{"seconds":1786000000}`, 0},
		{"unexpected bool", `true`, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var ft FlexTime
			if err := ft.UnmarshalJSON([]byte(tt.raw)); err != nil {
				t.Fatalf("UnmarshalJSON(%s) returned %v; it must NEVER return an error — "+
					"an Unmarshaler error aborts the surrounding decode and takes the principal with it", tt.raw, err)
			}

			var got int64
			if !ft.IsZero() {
				got = ft.UnixMilli()
			}
			if got != tt.wantMillis {
				t.Errorf("UnmarshalJSON(%s) = %v (unix_ms %d), want unix_ms %d", tt.raw, ft.Time, got, tt.wantMillis)
			}
		})
	}
}

// TestIntrospect_RejectedIsTagged: a 401 or 403 carries ErrTokenRejected with
// the message unchanged; a 5xx and an unreachable endpoint do not. Failure
// prevented: the read gate telling a user to sign in again because the
// server had a bad minute, or passing a rejected token as "unverified".
func TestIntrospect_RejectedIsTagged(t *testing.T) {
	t.Setenv("OX_ALLOW_PLAINTEXT_ENDPOINT", "1")
	tests := []struct {
		status   int
		body     string
		rejected bool
		wantMsg  string
	}{
		{http.StatusUnauthorized, "", true, "server rejected token (HTTP 401)"},
		{http.StatusUnauthorized, `{"error":"invalid_token"}`, true, "server rejected token: invalid_token"},
		{http.StatusForbidden, `{"error_description":"revoked"}`, true, "server rejected token: revoked"},
		{http.StatusBadGateway, "", false, "server rejected token (HTTP 502)"},
	}
	for _, tt := range tests {
		t.Run(http.StatusText(tt.status)+tt.body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			_, err := Introspect(srv.URL, "oxp_test_4bDZfN")
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := errors.Is(err, ErrTokenRejected); got != tt.rejected {
				t.Errorf("errors.Is(ErrTokenRejected) = %v, want %v (err: %v)", got, tt.rejected, err)
			}
			if err.Error() != tt.wantMsg {
				t.Errorf("message = %q, want %q", err.Error(), tt.wantMsg)
			}
		})
	}

	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	if _, err := Introspect(deadURL, "oxp_test_4bDZfN"); errors.Is(err, ErrTokenRejected) {
		t.Errorf("an unreachable endpoint is not a rejection: %v", err)
	}
}
