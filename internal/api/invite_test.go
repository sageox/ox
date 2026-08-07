// Package api_test drives CreateTeamInvite strictly through its exported
// surface. The external test package is deliberate: parseServerError,
// parseInviteError and serverError are unreachable from here, so no test in
// this file can drift into asserting on the parser's internals instead of on
// what a caller actually observes (the returned struct, the sentinel, and the
// bytes that went over the wire).
package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// capturedRequest is everything the server observed about one inbound request.
// Captured server-side so assertions are on the bytes that were actually
// transmitted, not on what the caller intended to transmit.
type capturedRequest struct {
	Method string
	// Path is the DECODED path. Convenient to assert against, but it cannot
	// distinguish an escaped %2F from a literal '/', so a ref that escaped into
	// one segment looks identical to one that split into two.
	Path string
	// EscapedPath is what actually went over the wire. Segment-containment
	// assertions must use this one.
	EscapedPath string
	RawQuery    string
	Headers     http.Header
	Body        []byte
}

// recorder collects every request a test's server receives. Guarded because
// net/http invokes handlers on their own goroutines and some tests
// (redirects, cancellation) produce more than one.
type recorder struct {
	mu       sync.Mutex
	requests []capturedRequest
}

func (rec *recorder) record(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.requests = append(rec.requests, capturedRequest{
		Method:      r.Method,
		Path:        r.URL.Path,
		EscapedPath: r.URL.EscapedPath(),
		RawQuery:    r.URL.RawQuery,
		Headers:     r.Header.Clone(),
		Body:        body,
	})
}

func (rec *recorder) all() []capturedRequest {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]capturedRequest(nil), rec.requests...)
}

// last returns the most recent request, failing the test if none arrived.
func (rec *recorder) last(t *testing.T) capturedRequest {
	t.Helper()
	all := rec.all()
	require.NotEmpty(t, all, "server received no request")
	return all[len(all)-1]
}

// newInviteServer starts a server that records every request and then defers
// to respond for the reply.
func newInviteServer(t *testing.T, respond func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		respond(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// staticServer replies with a fixed status and body to every request.
func staticServer(t *testing.T, status int, body string) (*httptest.Server, *recorder) {
	t.Helper()
	return newInviteServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
}

func validRequest() api.CreateInviteRequest {
	return api.CreateInviteRequest{Email: "recipient@example.com", Role: api.RoleMember}
}

// inviteSentinels is every sentinel CreateTeamInvite can return. Used to prove
// a response maps to exactly one of them — or, for unmapped statuses, to none.
var inviteSentinels = map[string]error{
	"ErrInviteExists":       api.ErrInviteExists,
	"ErrInviteForbidden":    api.ErrInviteForbidden,
	"ErrInviteNotAMember":   api.ErrInviteNotAMember,
	"ErrPersonalTeam":       api.ErrPersonalTeam,
	"ErrInviteUnsupported":  api.ErrInviteUnsupported,
	"ErrUnauthorized":       api.ErrUnauthorized,
	"ErrVersionUnsupported": api.ErrVersionUnsupported,
}

// requireOnlySentinel asserts err matches want and matches no other sentinel.
// Checking the negatives is the point: the 409 and 404 collisions are only
// meaningfully "disambiguated" if the losing sentinel is also excluded.
func requireOnlySentinel(t *testing.T, err error, want error) {
	t.Helper()
	require.Error(t, err)
	require.ErrorIs(t, err, want, "wrong sentinel; got %v", err)
	for name, other := range inviteSentinels {
		if errors.Is(other, want) {
			continue
		}
		assert.NotErrorIs(t, err, other, "must not also match %s", name)
	}
}

// requireNoSentinel asserts err is a real error that impersonates none of the
// sentinels — the contract for unmapped statuses like 500.
func requireNoSentinel(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	for name, s := range inviteSentinels {
		assert.NotErrorIs(t, err, s, "unmapped status must not be reported as %s", name)
	}
}

// ---------------------------------------------------------------------------
// A. Success path and token containment
// ---------------------------------------------------------------------------

// TestCreateTeamInvite_Success_ReturnsCreatedInvite covers the happy path.
// Failure prevented: a field-name mismatch between the server's 201 body and
// InviteResponse silently yields an invite with a blank ID/expiry, which the
// command then renders as a successful invitation nobody can act on.
func TestCreateTeamInvite_Success_ReturnsCreatedInvite(t *testing.T) {
	t.Parallel()

	srv, _ := staticServer(t, http.StatusCreated, `{
		"id": "inv_01",
		"email": "recipient@example.com",
		"team_id": "team_abc",
		"team_name": "Acme Platform",
		"role": "admin",
		"expires_at": "2026-09-01T00:00:00Z"
	}`)

	client := api.NewRepoClientWithEndpoint(srv.URL).WithAuthToken("tok")
	invite, err := client.CreateTeamInvite(context.Background(), "team_abc", api.CreateInviteRequest{
		Email: "recipient@example.com",
		Role:  api.RoleAdmin,
	})

	require.NoError(t, err)
	require.NotNil(t, invite)
	assert.Equal(t, "inv_01", invite.ID)
	assert.Equal(t, "recipient@example.com", invite.Email)
	assert.Equal(t, "team_abc", invite.TeamID)
	assert.Equal(t, "Acme Platform", invite.TeamName)
	assert.Equal(t, "admin", invite.Role)
	assert.Equal(t, "2026-09-01T00:00:00Z", invite.ExpiresAt)
}

// TestCreateTeamInvite_NeverExposesInviteToken is the containment proof for
// the one live credential on this route.
//
// Failure prevented: someone adds a Token field to InviteResponse (or swaps
// the decode for a map/json.RawMessage) and the plaintext invite token becomes
// reachable by every caller — and therefore by --json output, by %+v in an
// error path, and by any structured log that renders the result.
func TestCreateTeamInvite_NeverExposesInviteToken(t *testing.T) {
	t.Parallel()

	const secret = "SUPERSECRET"
	srv, _ := staticServer(t, http.StatusCreated, `{
		"id": "inv_01",
		"email": "recipient@example.com",
		"team_id": "team_abc",
		"team_name": "Acme",
		"role": "member",
		"expires_at": "2026-09-01T00:00:00Z",
		"token": "`+secret+`",
		"invite_url": "https://sageox.ai/join/`+secret+`"
	}`)

	client := api.NewRepoClientWithEndpoint(srv.URL).WithAuthToken("tok")
	invite, err := client.CreateTeamInvite(context.Background(), "team_abc", validRequest())
	require.NoError(t, err)
	require.NotNil(t, invite)

	// every rendering a caller could plausibly reach for
	marshaled, err := json.Marshal(invite)
	require.NoError(t, err)

	renderings := map[string]string{
		"%v":           fmt.Sprintf("%v", invite),
		"%+v":          fmt.Sprintf("%+v", *invite),
		"%#v":          fmt.Sprintf("%#v", *invite),
		"json.Marshal": string(marshaled),
	}
	for name, rendered := range renderings {
		assert.NotContains(t, rendered, secret, "invite token leaked through %s", name)
	}

	// and the round-trip must not smuggle it through an unnamed key either
	var roundTripped map[string]any
	require.NoError(t, json.Unmarshal(marshaled, &roundTripped))
	for k, v := range roundTripped {
		assert.NotContains(t, fmt.Sprint(v), secret, "invite token leaked through key %q", k)
	}
	assert.NotContains(t, roundTripped, "token")
	assert.NotContains(t, roundTripped, "invite_url")
}

// ---------------------------------------------------------------------------
// B. Request shape: method, path, headers, body
// ---------------------------------------------------------------------------

// TestCreateTeamInvite_RequestShape pins what goes over the wire.
//
// Failure prevented: a wrong method or path silently becomes a 404, which
// parseInviteError reports as ErrInviteUnsupported ("this server does not
// support CLI invitations yet") — a client-side typo diagnosed as a server
// capability gap, aborting the whole batch with a wrong explanation.
func TestCreateTeamInvite_RequestShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		teamRef   string
		authToken string
		wantPath  string
		wantAuth  string
	}{
		// the server's RequireTeamMember middleware resolves either form, so
		// both must reach the same route unmodified
		{
			name:      "team_id ref",
			teamRef:   "team_01HXYZ",
			authToken: "tok-abc",
			wantPath:  "/api/v1/teams/team_01HXYZ/invites",
			wantAuth:  "Bearer tok-abc",
		},
		{
			name:      "slug ref",
			teamRef:   "acme-platform",
			authToken: "tok-abc",
			wantPath:  "/api/v1/teams/acme-platform/invites",
			wantAuth:  "Bearer tok-abc",
		},
		{
			// no token set => no header at all, not "Bearer " with an empty
			// value, which some gateways treat as a malformed credential
			// rather than as an anonymous request
			name:      "no auth token",
			teamRef:   "acme-platform",
			authToken: "",
			wantPath:  "/api/v1/teams/acme-platform/invites",
			wantAuth:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv, rec := staticServer(t, http.StatusCreated, `{"id":"inv_01"}`)

			client := api.NewRepoClientWithEndpoint(srv.URL)
			if tt.authToken != "" {
				client = client.WithAuthToken(tt.authToken)
			}
			_, err := client.CreateTeamInvite(context.Background(), tt.teamRef, validRequest())
			require.NoError(t, err)

			got := rec.last(t)
			assert.Equal(t, http.MethodPost, got.Method)
			assert.Equal(t, tt.wantPath, got.Path)
			assert.Empty(t, got.RawQuery, "no query string belongs on this route")
			assert.Equal(t, "application/json", got.Headers.Get("Content-Type"))
			assert.Equal(t, tt.wantAuth, got.Headers.Get("Authorization"))
			if tt.wantAuth == "" {
				_, present := got.Headers["Authorization"]
				assert.False(t, present, "Authorization header must be absent, not empty")
			}
		})
	}
}

// TestCreateTeamInvite_SendsEmailAndRoleVerbatim proves the client is a
// faithful courier for both fields.
//
// Failure prevented: the server's OpenAPI document wrongly advertises role as
// optional with a "member" default while validateInviteRole 400s on empty. If
// the client ever gained an omitempty on Role, or defaulted a blank role
// client-side, every --role=” invocation would either 400 with a confusing
// message or silently invite at the wrong privilege level.
func TestCreateTeamInvite_SendsEmailAndRoleVerbatim(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		email string
		role  string
	}{
		{name: "member", email: "a@example.com", role: api.RoleMember},
		{name: "admin", email: "b@example.com", role: api.RoleAdmin},
		{name: "owner", email: "c@example.com", role: api.RoleOwner},
		// an empty role must still be transmitted so the server's 400 (whose
		// message is load-bearing) is what the user sees
		{name: "empty role is still sent", email: "d@example.com", role: ""},
		// no client-side normalization: the server owns the vocabulary
		{name: "unknown role passes through", email: "e@example.com", role: "Owner"},
		{name: "plus addressing survives", email: "f+tag@example.com", role: api.RoleMember},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv, rec := staticServer(t, http.StatusCreated, `{"id":"inv_01"}`)

			_, err := api.NewRepoClientWithEndpoint(srv.URL).
				WithAuthToken("tok").
				CreateTeamInvite(context.Background(), "team_abc", api.CreateInviteRequest{
					Email: tt.email,
					Role:  tt.role,
				})
			require.NoError(t, err)

			var sent map[string]any
			require.NoError(t, json.Unmarshal(rec.last(t).Body, &sent),
				"request body was not valid JSON: %q", rec.last(t).Body)

			assert.Equal(t, tt.email, sent["email"])

			role, hasRole := sent["role"]
			require.True(t, hasRole, "role key must always be present, even when empty")
			assert.Equal(t, tt.role, role)

			assert.Len(t, sent, 2, "unexpected extra keys in request body: %v", sent)
		})
	}
}

// ---------------------------------------------------------------------------
// C. HTTP 409 — one status, two meanings
// ---------------------------------------------------------------------------

// TestCreateTeamInvite_Conflict_DisambiguatesOnCodeNotStatus covers the
// already-invited / personal-team collision.
//
// Failure prevented: branching on the 409 status instead of the wire code
// tells a user with a personal team "an active invite already exists" — an
// invitation they can neither find nor revoke, because none was ever created.
func TestCreateTeamInvite_Conflict_DisambiguatesOnCodeNotStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want error
	}{
		{
			name: "nested envelope with personal_team_immutable",
			body: `{"error":{"code":"personal_team_immutable","message":"personal teams cannot take invitations"}}`,
			want: api.ErrPersonalTeam,
		},
		{
			// the handler's own 409 uses the OTHER envelope entirely
			name: "flat envelope means already invited",
			body: `{"success":false,"error":"an active invite already exists for this email"}`,
			want: api.ErrInviteExists,
		},
		{
			name: "nested envelope with a different code is not a personal team",
			body: `{"error":{"code":"invite_already_exists","message":"already invited"}}`,
			want: api.ErrInviteExists,
		},
		{
			name: "409 with no body at all",
			body: ``,
			want: api.ErrInviteExists,
		},
		{
			name: "409 with a non-JSON body",
			body: `<html><body>Conflict</body></html>`,
			want: api.ErrInviteExists,
		},
		{
			// the code must be matched exactly; a near-miss is not a personal
			// team, and guessing wrong here suppresses a real conflict
			name: "code that merely contains the token is not a match",
			body: `{"error":{"code":"not_personal_team_immutable_at_all","message":"nope"}}`,
			want: api.ErrInviteExists,
		},
		{
			// the flat envelope's message is free text; a user-supplied team
			// name echoed into it must never be mistaken for a wire code
			name: "flat message quoting the code is not a personal team",
			body: `{"success":false,"error":"personal_team_immutable"}`,
			want: api.ErrInviteExists,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv, _ := staticServer(t, http.StatusConflict, tt.body)

			invite, err := api.NewRepoClientWithEndpoint(srv.URL).
				WithAuthToken("tok").
				CreateTeamInvite(context.Background(), "team_abc", validRequest())

			assert.Nil(t, invite, "no invite may be returned alongside an error")
			requireOnlySentinel(t, err, tt.want)
		})
	}
}

// ---------------------------------------------------------------------------
// D. HTTP 404 — one status, two opposite handlings
// ---------------------------------------------------------------------------

// TestCreateTeamInvite_NotFound_SeparatesMissingRouteFromNonMember is the
// highest-consequence mapping in the file.
//
// Failure prevented: the two 404s need opposite handling. A non-member 404 is
// ONE recipient's outcome; an unrouted 404 means the server predates the
// feature and must abort the batch. Swapping them either (a) tells a user with
// a typo'd team that their whole SageOx deployment is too old, or (b) marches
// through fifty addresses against a route that does not exist, reporting each
// as "not a member".
func TestCreateTeamInvite_NotFound_SeparatesMissingRouteFromNonMember(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want error
	}{
		{
			// the real api-go response for a team the caller cannot see
			name: "nested envelope means not a member",
			body: `{"error":{"code":"NOT_FOUND","message":"team not found"}}`,
			want: api.ErrInviteNotAMember,
		},
		{
			name: "flat envelope means not a member",
			body: `{"success":false,"error":"team not found"}`,
			want: api.ErrInviteNotAMember,
		},
		{
			// chi's actual trie-miss body, byte for byte, including the
			// trailing newline — this is the string that decides whether the
			// command aborts
			name: "chi trie miss means unsupported",
			body: "404 page not found\n",
			want: api.ErrInviteUnsupported,
		},
		{
			name: "empty body means unsupported",
			body: ``,
			want: api.ErrInviteUnsupported,
		},
		{
			name: "whitespace-only body means unsupported",
			body: "  \n\t ",
			want: api.ErrInviteUnsupported,
		},
		{
			name: "proxy HTML error page means unsupported",
			body: `<html><head><title>404 Not Found</title></head><body><h1>Not Found</h1></body></html>`,
			want: api.ErrInviteUnsupported,
		},
		{
			// valid JSON but not an error envelope: nothing here says "the
			// server considered your request and declined it"
			name: "empty JSON object means unsupported",
			body: `{}`,
			want: api.ErrInviteUnsupported,
		},
		{
			name: "JSON null means unsupported",
			body: `null`,
			want: api.ErrInviteUnsupported,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv, _ := staticServer(t, http.StatusNotFound, tt.body)

			invite, err := api.NewRepoClientWithEndpoint(srv.URL).
				WithAuthToken("tok").
				CreateTeamInvite(context.Background(), "team_abc", validRequest())

			assert.Nil(t, invite)
			requireOnlySentinel(t, err, tt.want)
		})
	}
}

// ---------------------------------------------------------------------------
// E. Status mapping outside the two collisions
// ---------------------------------------------------------------------------

// TestCreateTeamInvite_StatusToSentinel covers the unambiguous statuses.
//
// Failure prevented: 401 must reach the caller as ErrUnauthorized because that
// is what stops the batch and tells the user to run `ox login`; without it the
// command grinds through every address emitting identical auth failures.
func TestCreateTeamInvite_StatusToSentinel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{
			name:   "401 nested envelope",
			status: http.StatusUnauthorized,
			body:   `{"error":{"code":"unauthorized","message":"missing token"}}`,
			want:   api.ErrUnauthorized,
		},
		{
			// the status alone must be enough; a 401 from a gateway carries no
			// SageOx envelope at all
			name:   "401 non-JSON body",
			status: http.StatusUnauthorized,
			body:   `Unauthorized`,
			want:   api.ErrUnauthorized,
		},
		{
			name:   "403 flat envelope",
			status: http.StatusForbidden,
			body:   `{"success":false,"error":"you cannot invite at that role"}`,
			want:   api.ErrInviteForbidden,
		},
		{
			name:   "403 empty body",
			status: http.StatusForbidden,
			body:   ``,
			want:   api.ErrInviteForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv, _ := staticServer(t, tt.status, tt.body)

			invite, err := api.NewRepoClientWithEndpoint(srv.URL).
				WithAuthToken("tok").
				CreateTeamInvite(context.Background(), "team_abc", validRequest())

			assert.Nil(t, invite)
			requireOnlySentinel(t, err, tt.want)
		})
	}
}

// TestCreateTeamInvite_UnmappedStatusCarriesServerMessage covers everything
// with no sentinel.
//
// Failure prevented: two ways to lose the user's only diagnostic. (1) An
// unmapped status quietly matching a sentinel would render a 500 as
// "already invited". (2) Dropping the server's message leaves "HTTP 400" on
// screen when the server said exactly which roles are legal — the invite
// command's most common recoverable failure.
func TestCreateTeamInvite_UnmappedStatusCarriesServerMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		status   int
		body     string
		contains []string
	}{
		{
			name:     "400 flat envelope surfaces the role vocabulary",
			status:   http.StatusBadRequest,
			body:     `{"success":false,"error":"role must be owner, admin, or member"}`,
			contains: []string{"400", "role must be owner, admin, or member"},
		},
		{
			name:     "400 nested envelope surfaces its message",
			status:   http.StatusBadRequest,
			body:     `{"error":{"code":"invalid_email","message":"email is not valid"}}`,
			contains: []string{"400", "email is not valid"},
		},
		{
			name:     "500 nested envelope",
			status:   http.StatusInternalServerError,
			body:     `{"error":{"code":"internal","message":"database unavailable"}}`,
			contains: []string{"500", "database unavailable"},
		},
		{
			// no envelope at all: the raw body is better than nothing
			name:     "502 raw gateway text",
			status:   http.StatusBadGateway,
			body:     "upstream connect error",
			contains: []string{"502", "upstream connect error"},
		},
		{
			name:     "503 with no body still names the status",
			status:   http.StatusServiceUnavailable,
			body:     ``,
			contains: []string{"503"},
		},
		{
			name:     "429 rate limited",
			status:   http.StatusTooManyRequests,
			body:     `{"success":false,"error":"slow down"}`,
			contains: []string{"429", "slow down"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv, _ := staticServer(t, tt.status, tt.body)

			invite, err := api.NewRepoClientWithEndpoint(srv.URL).
				WithAuthToken("tok").
				CreateTeamInvite(context.Background(), "team_abc", validRequest())

			assert.Nil(t, invite)
			requireNoSentinel(t, err)
			for _, want := range tt.contains {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

// TestCreateTeamInvite_VersionGateBeatsBodyParsing covers the 426 hard block.
//
// Failure prevented: if the body were parsed before the version gate, a 426
// carrying an error envelope would surface as a generic "HTTP 426" instead of
// ErrVersionUnsupported — and the command would keep marching through the
// remaining addresses against a server that has already refused this CLI.
func TestCreateTeamInvite_VersionGateBeatsBodyParsing(t *testing.T) {
	t.Parallel()

	srv, _ := newInviteServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-SageOx-Min-Version", "9.9.9")
		w.WriteHeader(http.StatusUpgradeRequired)
		_, _ = io.WriteString(w, `{"success":false,"error":"upgrade required"}`)
	})

	invite, err := api.NewRepoClientWithEndpoint(srv.URL).
		WithAuthToken("tok").
		CreateTeamInvite(context.Background(), "team_abc", validRequest())

	assert.Nil(t, invite)
	require.ErrorIs(t, err, api.ErrVersionUnsupported)
}

// ---------------------------------------------------------------------------
// F. Hostile bodies — the parser must never panic and never mis-classify
// ---------------------------------------------------------------------------

// TestCreateTeamInvite_HostileErrorBodies_NeverPanicNeverSucceed sweeps every
// shape of junk that can arrive where an error envelope was expected, across
// every status the mapper special-cases.
//
// Failure prevented: the `error` key is a string in one envelope and an object
// in the other, so it has to be decoded as a raw message and re-tried. Any
// shape outside those two (number, array, null, nested junk) must be survived,
// not panicked on — and must never turn a non-2xx into a nil error, which the
// command would render as a successful invitation.
func TestCreateTeamInvite_HostileErrorBodies_NeverPanicNeverSucceed(t *testing.T) {
	t.Parallel()

	bodies := []struct {
		name string
		body string
	}{
		{"empty", ``},
		{"whitespace", "   \n  "},
		{"json null", `null`},
		{"empty object", `{}`},
		{"empty array", `[]`},
		{"array of objects", `[{"error":"boom"}]`},
		{"bare string", `"boom"`},
		{"bare number", `42`},
		{"error is a number", `{"error":123}`},
		{"error is null", `{"error":null}`},
		{"error is true", `{"error":true}`},
		{"error is an empty object", `{"error":{}}`},
		{"error is an array", `{"error":["a","b"]}`},
		{"error is an array of objects", `{"error":[{"code":"x"}]}`},
		{"error nests another error", `{"error":{"error":{"error":{"code":"x"}}}}`},
		{"code is a number", `{"error":{"code":123,"message":"m"}}`},
		{"message is an object", `{"error":{"code":"x","message":{"nested":"m"}}}`},
		{"truncated json", `{"error":{"code":"x"`},
		{"trailing garbage after json", `{"error":"boom"}<<<`},
		{"duplicate error keys", `{"error":"flat","error":{"code":"personal_team_immutable"}}`},
		{"html", `<html><body>500</body></html>`},
		{"plain text", `something went terribly wrong`},
		{"nul bytes", "\x00\x00\x00"},
		{"deeply nested arrays", strings.Repeat("[", 64) + strings.Repeat("]", 64)},
		{"unicode noise", `{"error":"💥 boom"}`},
	}

	statuses := []int{
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusConflict,
		http.StatusInternalServerError,
	}

	for _, status := range statuses {
		for _, b := range bodies {
			t.Run(fmt.Sprintf("%d/%s", status, b.name), func(t *testing.T) {
				t.Parallel()
				srv, _ := staticServer(t, status, b.body)

				invite, err := api.NewRepoClientWithEndpoint(srv.URL).
					WithAuthToken("tok").
					CreateTeamInvite(context.Background(), "team_abc", validRequest())

				require.Error(t, err, "a non-2xx must never produce a nil error")
				assert.Nil(t, invite, "a non-2xx must never produce an invite")
				assert.NotEmpty(t, err.Error(), "the error must say something")

				// a mis-classification here is worse than a generic error:
				// 404 and 409 are the two overloaded statuses, so junk must
				// never be read as the *other* meaning by accident
				if status != http.StatusConflict {
					assert.NotErrorIs(t, err, api.ErrPersonalTeam)
					assert.NotErrorIs(t, err, api.ErrInviteExists)
				}
				if status != http.StatusNotFound {
					assert.NotErrorIs(t, err, api.ErrInviteNotAMember)
					assert.NotErrorIs(t, err, api.ErrInviteUnsupported)
				}
			})
		}
	}
}

// TestCreateTeamInvite_MalformedSuccessBody covers a 2xx whose body is not a
// usable invite.
//
// Failure prevented: reporting "invited" on a body the client could not decode
// hands the user an invite ID they cannot look up. A decode failure on a 201
// must surface as an error, never as a zero-valued success.
func TestCreateTeamInvite_MalformedSuccessBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "truncated json", body: `{"id":"inv_01","email":`, wantErr: true},
		{name: "empty body", body: ``, wantErr: true},
		{name: "html", body: `<html>ok</html>`, wantErr: true},
		{name: "json array", body: `[{"id":"inv_01"}]`, wantErr: true},
		{name: "bare string", body: `"inv_01"`, wantErr: true},
		{name: "wrong field types", body: `{"id":123}`, wantErr: true},
		{name: "trailing garbage", body: `{"id":"inv_01"}trailing`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv, _ := staticServer(t, http.StatusCreated, tt.body)

			invite, err := api.NewRepoClientWithEndpoint(srv.URL).
				WithAuthToken("tok").
				CreateTeamInvite(context.Background(), "team_abc", validRequest())

			if tt.wantErr {
				require.Error(t, err)
				assert.Nil(t, invite, "a partially decoded invite must not escape")
				return
			}
			require.NoError(t, err)
		})
	}
}

// ---------------------------------------------------------------------------
// G. Hostile transport
// ---------------------------------------------------------------------------

// TestCreateTeamInvite_TruncatedBodyWithLyingContentLength covers a server
// that promises more bytes than it delivers and then drops the connection.
//
// Failure prevented: io.ReadAll returns ErrUnexpectedEOF with a partial buffer.
// Decoding that partial buffer, or ignoring the read error, would turn a
// half-delivered response into either a bogus invite or a panic.
func TestCreateTeamInvite_TruncatedBodyWithLyingContentLength(t *testing.T) {
	t.Parallel()

	srv, _ := newInviteServer(t, func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		bw := bufio.NewWriter(conn)
		// Content-Length claims 500 bytes; only a fragment follows
		_, _ = bw.WriteString("HTTP/1.1 201 Created\r\nContent-Type: application/json\r\nContent-Length: 500\r\n\r\n")
		_, _ = bw.WriteString(`{"id":"inv_01","email":"recipient@exa`)
		_ = bw.Flush()
	})

	invite, err := api.NewRepoClientWithEndpoint(srv.URL).
		WithAuthToken("tok").
		CreateTeamInvite(context.Background(), "team_abc", validRequest())

	require.Error(t, err, "a truncated body must not be reported as a created invite")
	assert.Nil(t, invite)
}

// TestCreateTeamInvite_ConnectionClosedBeforeResponse covers a server that
// accepts the request and hangs up without answering.
//
// Failure prevented: a transport-level failure must return an error rather
// than a nil invite with a nil error — the shape the command reads as success.
func TestCreateTeamInvite_ConnectionClosedBeforeResponse(t *testing.T) {
	t.Parallel()

	srv, _ := newInviteServer(t, func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			return
		}
		if tcp, ok := conn.(*net.TCPConn); ok {
			// RST rather than FIN so the client sees a hard transport failure
			_ = tcp.SetLinger(0)
		}
		_ = conn.Close()
	})

	invite, err := api.NewRepoClientWithEndpoint(srv.URL).
		WithAuthToken("tok").
		CreateTeamInvite(context.Background(), "team_abc", validRequest())

	require.Error(t, err)
	assert.Nil(t, invite)
}

// TestCreateTeamInvite_ContextCancellation covers both cancellation windows.
//
// Failure prevented: if ctx were not threaded onto the request, Ctrl-C during
// a multi-recipient invite run would be ignored until the client's own 10s
// timeout — once per remaining address.
func TestCreateTeamInvite_ContextCancellation(t *testing.T) {
	t.Parallel()

	t.Run("canceled before the call", func(t *testing.T) {
		t.Parallel()
		srv, rec := staticServer(t, http.StatusCreated, `{"id":"inv_01"}`)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		invite, err := api.NewRepoClientWithEndpoint(srv.URL).
			WithAuthToken("tok").
			CreateTeamInvite(ctx, "team_abc", validRequest())

		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
		assert.Nil(t, invite)
		assert.Empty(t, rec.all(), "a canceled context must not reach the server")
	})

	t.Run("canceled in flight", func(t *testing.T) {
		t.Parallel()
		inFlight := make(chan struct{})
		release := make(chan struct{})
		srv, _ := newInviteServer(t, func(w http.ResponseWriter, _ *http.Request) {
			close(inFlight)
			<-release // hold the response open until the test cancels
			w.WriteHeader(http.StatusCreated)
		})
		t.Cleanup(func() { close(release) })

		ctx, cancel := context.WithCancel(context.Background())

		type result struct {
			invite *api.InviteResponse
			err    error
		}
		done := make(chan result, 1)
		go func() {
			inv, err := api.NewRepoClientWithEndpoint(srv.URL).
				WithAuthToken("tok").
				CreateTeamInvite(ctx, "team_abc", validRequest())
			done <- result{inv, err}
		}()

		<-inFlight
		cancel()

		got := <-done
		require.Error(t, got.err, "cancellation must abort the in-flight request")
		assert.ErrorIs(t, got.err, context.Canceled)
		assert.Nil(t, got.invite)
	})
}

// TestCreateTeamInvite_LargeResponseBody covers an oversized 201.
//
// Failure prevented: a padded response must still decode correctly and must
// not smuggle the token past the struct filter just because the body is big
// enough that no human will read it.
func TestCreateTeamInvite_LargeResponseBody(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: builds and transfers a multi-megabyte response body")
	}

	const secret = "SUPERSECRET"
	padding := strings.Repeat("x", 4<<20) // 4 MiB
	body := fmt.Sprintf(`{"id":"inv_01","email":"recipient@example.com","token":%q,"padding":%q}`, secret, padding)

	srv, _ := staticServer(t, http.StatusCreated, body)

	invite, err := api.NewRepoClientWithEndpoint(srv.URL).
		WithAuthToken("tok").
		CreateTeamInvite(context.Background(), "team_abc", validRequest())

	require.NoError(t, err)
	require.NotNil(t, invite)
	assert.Equal(t, "inv_01", invite.ID)
	assert.NotContains(t, fmt.Sprintf("%+v", *invite), secret)
}

// ---------------------------------------------------------------------------
// H. URL construction
// ---------------------------------------------------------------------------

// TestCreateTeamInvite_BaseURLTrailingSlash covers endpoint values that were
// stored with a trailing slash.
//
// Failure prevented: "https://host/" + "/api/v1/..." yields "//api/v1/...",
// which chi answers with a 404 — and a 404 on this route is read as
// ErrInviteUnsupported, so a cosmetic config difference would be reported as
// "this SageOx server does not support CLI invitations yet".
func TestCreateTeamInvite_BaseURLTrailingSlash(t *testing.T) {
	t.Parallel()

	srv, rec := staticServer(t, http.StatusCreated, `{"id":"inv_01"}`)

	_, err := api.NewRepoClientWithEndpoint(srv.URL+"/").
		WithAuthToken("tok").
		CreateTeamInvite(context.Background(), "team_abc", validRequest())
	require.NoError(t, err)

	got := rec.last(t)
	assert.Equal(t, "/api/v1/teams/team_abc/invites", got.Path)
	assert.NotContains(t, got.Path, "//")
}

// TestCreateTeamInvite_TeamRefIsPathEscaped checks that a team ref is confined
// to one path segment.
//
// teamRef is escaped with url.PathEscape, so it occupies exactly one segment
// whatever the user typed after --team — and an unrecognized team is
// deliberately passed through verbatim to let the server resolve it.
//
// Failure prevented: dropping the escaping lets a ref containing '?' or '#'
// terminate the path early. The "/invites" suffix vanishes, the POST lands on
// another route, and the resulting 404 — which carries no JSON body — is
// reported as ErrInviteUnsupported ("this SageOx server does not support CLI
// invitations yet"). A typo'd team name would abort the whole batch with a
// diagnosis pointing at the server instead of at the flag.
func TestCreateTeamInvite_TeamRefIsPathEscaped(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		teamRef string
	}{
		{name: "plain team id", teamRef: "team_01HXYZ"},
		{name: "slug", teamRef: "acme-platform"},
		{name: "space", teamRef: "acme platform"},
		{name: "non-ascii", teamRef: "équipe"},
		{name: "question mark", teamRef: "acme?role=owner"},
		{name: "hash", teamRef: "acme#fragment"},
		// A ref that is already a (broken) percent-sequence must be escaped as
		// literal text rather than passed through as encoding. Failure
		// prevented: treating it as encoding either fails to build the request
		// or silently changes which team is addressed.
		{name: "invalid percent escape", teamRef: "acme%zz"},
		{name: "percent literal", teamRef: "100%team"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv, rec := staticServer(t, http.StatusCreated, `{"id":"inv_01"}`)

			_, err := api.NewRepoClientWithEndpoint(srv.URL).
				WithAuthToken("tok").
				CreateTeamInvite(context.Background(), tt.teamRef, validRequest())
			require.NoError(t, err)

			got := rec.last(t)
			assert.Equal(t, "/api/v1/teams/"+tt.teamRef+"/invites", got.Path,
				"the ref must occupy exactly one path segment and /invites must survive")
			assert.Empty(t, got.RawQuery,
				"nothing from the team ref may leak into the query string")
		})
	}
}

// TestCreateTeamInvite_TeamRefNeverLandsOnAnotherRoute is the containment
// assertion behind the escaping above: whatever the user typed after --team,
// the request must still be aimed at that team's invites collection.
//
// Failure prevented: a ref that truncates the path sends the POST to a
// different route entirely. That request 404s, and a 404 without a JSON body
// is read as "this server has no invite endpoint" — so a typo'd team name
// would abort the whole batch with a wrong and very confusing explanation.
func TestCreateTeamInvite_TeamRefNeverLandsOnAnotherRoute(t *testing.T) {
	t.Parallel()

	for _, ref := range []string{"acme?role=owner", "acme#fragment", "acme%zz", "a/b"} {
		t.Run(ref, func(t *testing.T) {
			t.Parallel()
			srv, rec := staticServer(t, http.StatusCreated, `{"id":"inv_01"}`)

			_, err := api.NewRepoClientWithEndpoint(srv.URL).
				WithAuthToken("tok").
				CreateTeamInvite(context.Background(), ref, validRequest())
			require.NoError(t, err)

			got := rec.last(t)

			// Assert on the ESCAPED path. The decoded Path cannot tell an
			// escaped %2F from a real '/', so "a/b" would look correctly
			// contained there even if it had split the path in two.
			assert.Equal(t, "/api/v1/teams/"+url.PathEscape(ref)+"/invites", got.EscapedPath,
				"the ref must occupy exactly one wire path segment")

			// Exactly four segments: api, v1, teams, <ref>, invites. Counting
			// on the escaped form is what catches a ref that added a segment.
			segs := strings.Split(strings.TrimPrefix(got.EscapedPath, "/"), "/")
			assert.Len(t, segs, 5, "ref must not add or remove a path segment: %q", got.EscapedPath)
			assert.Equal(t, "invites", segs[len(segs)-1])

			assert.Empty(t, got.RawQuery,
				"nothing from the team ref may leak into the query string")
		})
	}
}

// ---------------------------------------------------------------------------
// I. Redirects
// ---------------------------------------------------------------------------

// TestCreateTeamInvite_RedirectMustNotForgeSuccess covers an HTTP 302 on the
// invites route.
//
// KNOWN BUG (this test is expected to FAIL): the client uses the default
// redirect policy, and Go rewrites a POST into a GET when following a
// 301/302/303. The invite body is discarded, the final GET creates nothing,
// and whatever that GET returns is decoded as the created invite — so the
// command reports "invited" for an invitation that never existed. Any
// canonicalizing redirect in front of the API (host canonicalization,
// http->https, chi's RedirectSlashes, an SSO captive portal) triggers it.
//
// Failure prevented: a silent false success — the worst failure mode this
// route has, because the user believes a teammate was invited and never
// checks again.
func TestCreateTeamInvite_RedirectMustNotForgeSuccess(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/teams/team_abc/invites", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		http.Redirect(w, r, "/api/v1/teams/team_abc", http.StatusFound)
	})
	mux.HandleFunc("/api/v1/teams/team_abc", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"team_abc","team_name":"Acme"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	invite, err := api.NewRepoClientWithEndpoint(srv.URL).
		WithAuthToken("tok").
		CreateTeamInvite(context.Background(), "team_abc", validRequest())

	for _, req := range rec.all() {
		assert.Equal(t, http.MethodPost, req.Method,
			"a redirect must not downgrade the invite POST to %s %s", req.Method, req.Path)
		assert.NotEmpty(t, req.Body, "the invite payload must survive the redirect")
	}
	if err == nil {
		t.Errorf("KNOWN BUG: reported a created invite (%+v) although no POST ever "+
			"reached a handler that creates one", invite)
	}
}

// TestCreateTeamInvite_DotSegmentTeamRefIsRefused covers the one class of ref
// that escaping cannot make safe.
//
// url.PathEscape leaves "." and ".." untouched (dots are unreserved), so a ref
// of ".." builds ".../teams/../invites" — which any normalizing proxy or server
// collapses to ".../invites", a different endpoint. The 404 that follows has no
// JSON body and would be reported to the user as "this server does not support
// CLI invitations".
//
// Failure prevented: a team ref silently retargeting the request at a route the
// user never asked for, diagnosed as a server capability gap.
func TestCreateTeamInvite_DotSegmentTeamRefIsRefused(t *testing.T) {
	t.Parallel()

	for _, ref := range []string{".", "..", "  ..  ", ""} {
		t.Run(fmt.Sprintf("%q", ref), func(t *testing.T) {
			t.Parallel()
			srv, rec := staticServer(t, http.StatusCreated, `{"id":"inv_01"}`)

			invite, err := api.NewRepoClientWithEndpoint(srv.URL).
				WithAuthToken("tok").
				CreateTeamInvite(context.Background(), ref, validRequest())

			require.ErrorIs(t, err, api.ErrInvalidTeamRef)
			assert.Nil(t, invite)
			assert.Empty(t, rec.all(), "a ref that cannot be placed safely must never be sent")
		})
	}
}

// The same guard must cover every invite operation, not just create — --list
// and --cancel take the identical user-supplied --team value.
func TestListAndRevoke_RefuseDotSegmentTeamRef(t *testing.T) {
	t.Parallel()

	srv, rec := staticServer(t, http.StatusOK, `{"invites":[],"total":0}`)
	client := api.NewRepoClientWithEndpoint(srv.URL).WithAuthToken("tok")

	_, listErr := client.ListTeamInvites(context.Background(), "..")
	require.ErrorIs(t, listErr, api.ErrInvalidTeamRef)

	revokeErr := client.RevokeTeamInvite(context.Background(), "..", "inv_01")
	require.ErrorIs(t, revokeErr, api.ErrInvalidTeamRef)

	assert.Empty(t, rec.all(), "neither operation may reach the network with an unsafe ref")
}
