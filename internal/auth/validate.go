package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/logger"
	"github.com/sageox/ox/internal/useragent"
)

// Principal kinds reported by the introspection endpoint in
// IntrospectResult.PrincipalKind. The wire strings live here and nowhere else:
// a caller comparing against its own literal would silently fall into its
// generic branch if the server ever renamed one — dropping the team identity
// with no compile error and no log line to notice it by.
const (
	PrincipalKindUser        = "user"
	PrincipalKindTeamService = "team-service"
)

// ErrEndpointUnreachable wraps any failure to REACH the introspection endpoint,
// as distinct from the server answering "no". A caller that conflates the two
// tells an offline user their credential was rejected and advises a needless
// credential rotation.
var ErrEndpointUnreachable = errors.New("introspection endpoint unreachable")

// ErrTokenRejected marks an introspection answer of 401 or 403: the server
// was reached and refused the credential. It is the third outcome next to
// success and ErrEndpointUnreachable, so a caller can tell "sign in again"
// from "the server is having a bad day" (a 5xx is neither) without parsing
// the message. The error text itself is unchanged.
var ErrTokenRejected = errors.New("token rejected by server")

// rejectedError keeps the original message and adds ErrTokenRejected to the
// chain for errors.Is.
type rejectedError struct{ err error }

func (e *rejectedError) Error() string { return e.err.Error() }
func (e *rejectedError) Unwrap() []error {
	return []error{e.err, ErrTokenRejected}
}

// rejectedByStatus tags err with ErrTokenRejected when status is 401 or 403.
func rejectedByStatus(status int, err error) error {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return &rejectedError{err: err}
	}
	return err
}

// maxIntrospectBody bounds the response body we are willing to buffer. A
// legitimate introspection response is a few hundred bytes; 1 MiB leaves an
// enormous margin while keeping a hostile or misconfigured endpoint from
// driving an unbounded allocation in the CLI.
const maxIntrospectBody = 1 << 20

// IntrospectResult is the parsed shape of the introspection endpoint (see
// IntrospectEndpoint) — the single door that validates EVERY bearer credential
// family (session/JWT, personal oxp_, team oxt_) and reports what/who it
// authenticates as.
//
// Field names mirror the server's response exactly and are FROZEN. The shape:
//
//	{"active": true, "principal_kind": "user"|"team-service",
//	 "scope": "...", "token_type": "Bearer", "expires_at": <timestamp|null>,
//	 "user": {"id","email","name","tier"} | null,
//	 "team": {"team_id"} | null,
//	 "token": {"prefix","name"} | null,
//	 "coworker": {"id","display_name"} | null}
//
// Exactly one of User/Team is non-nil, matching PrincipalKind.
type IntrospectResult struct {
	Active        bool                 `json:"active"`
	PrincipalKind string               `json:"principal_kind"` // PrincipalKindUser | PrincipalKindTeamService
	Scope         string               `json:"scope"`
	TokenType     string               `json:"token_type"`
	ExpiresAt     *FlexTime            `json:"expires_at"`
	User          *IntrospectUser      `json:"user"`
	Team          *IntrospectTeam      `json:"team"`
	Token         *IntrospectTokenInfo `json:"token"`
	// Coworker stays undecoded because a missing key and null mean different
	// things: a server older than the field sends no key, while null says the
	// team token has no AI coworker attached. See TeamCoworker.
	Coworker json.RawMessage `json:"coworker"`
}

// FlexTime tolerates every timestamp encoding a conforming server might
// legitimately send for expires_at: an RFC 3339 string, a JSON null, an empty
// string, or a numeric unix epoch (the RFC 7662 convention for this field).
//
// A single unparseable value must never turn a live credential into "malformed
// introspection response". expires_at has no consumer that would notice a zero
// value, so rejecting on it would reintroduce exactly the spurious-rejection
// class this endpoint exists to remove.
type FlexTime struct{ time.Time }

// UnmarshalJSON NEVER returns an error — that is the entire point of the type.
// An error here would abort the surrounding decode (the JSON decoder returns an
// Unmarshaler's error immediately rather than saving it and continuing), taking
// the principal fields down with it. Unrecognized input leaves the zero time.
func (f *FlexTime) UnmarshalJSON(b []byte) error {
	f.Time = time.Time{}

	raw := strings.TrimSpace(string(b))
	if raw == "" || raw == "null" {
		return nil
	}

	if raw[0] == '"' {
		var s string
		if json.Unmarshal(b, &s) != nil {
			return nil
		}
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(s)); err == nil {
			f.Time = t
		}
		return nil
	}

	// Numeric unix epoch, integer or fractional seconds.
	var secs float64
	if json.Unmarshal(b, &secs) != nil {
		return nil
	}
	whole := int64(secs)
	f.Time = time.Unix(whole, int64((secs-float64(whole))*float64(time.Second))).UTC()
	return nil
}

// IntrospectUser is the personal-principal (oxp_ / session) branch of
// IntrospectResult.
type IntrospectUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Tier  string `json:"tier"`
}

// IntrospectTeam is the team-principal (oxt_) branch of IntrospectResult.
type IntrospectTeam struct {
	TeamID string `json:"team_id"`
}

// IntrospectTokenInfo describes the bearer credential itself — never the
// secret, only its display prefix and the name it was minted with.
type IntrospectTokenInfo struct {
	Prefix string `json:"prefix"`
	Name   string `json:"name"`
}

// Introspect validates accessToken against the server's single,
// family-agnostic introspection endpoint and returns the principal it
// authenticates as. Callers that only care whether the token is valid should
// use ValidateTokenServerSide; callers that need to know WHAT it authenticates
// as (status, doctor) call Introspect directly.
//
// A non-200 response — invalid, expired, revoked, or malformed, for ANY token
// family — is returned as an error. The server deliberately answers a uniform
// 401 rather than a typed rejection reason, so that a caller cannot use the
// reason to enumerate which credentials exist; the error string is therefore
// the only signal available to distinguish "no" from "yes, and here is who".
//
// A failure to reach the server at all is returned wrapped in
// ErrEndpointUnreachable so callers can tell "offline" from "rejected" without
// string-matching. The underlying transport error stays discoverable via
// errors.As.
func Introspect(ep, accessToken string) (*IntrospectResult, error) {
	return introspect(context.Background(), ep, accessToken)
}

func introspect(ctx context.Context, ep, accessToken string) (*IntrospectResult, error) {
	ep = endpoint.NormalizeEndpoint(ep)
	url := strings.TrimSuffix(ep, "/") + IntrospectEndpoint

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := useragent.NewRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create validation request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	logger.LogHTTPRequest("GET", url)
	start := time.Now()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("GET", url, err, duration)
		slog.Debug("server-side token validation failed (network)", "endpoint", ep, "error", err)
		// Two %w verbs: the transport error stays discoverable (errors.As for
		// *url.Error, net.Error, context deadline) AND the failure is tagged
		// with the sentinel so a caller can render "could not verify" instead
		// of "rejected". Telling an offline developer with a perfectly good
		// credential to mint a fresh one is a needless rotation.
		return nil, fmt.Errorf("could not reach %s to validate token: %w: %w",
			endpoint.NormalizeSlug(ep), err, ErrEndpointUnreachable)
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse("GET", url, resp.StatusCode, duration)

	// Bounded on BOTH paths: the success path reads the body too, and an
	// unbounded io.ReadAll of an arbitrary endpoint is an unbounded allocation.
	body, _ := io.ReadAll(http.MaxBytesReader(nil, resp.Body, maxIntrospectBody))

	if resp.StatusCode != http.StatusOK {
		slog.Debug("server-side token validation rejected",
			"endpoint", ep,
			"status", resp.StatusCode,
			"response", redactedBody(body, resp.Header.Get("Content-Type")),
		)

		var errResp struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		// A type mismatch on one field must not discard the others. The JSON
		// decoder saves-and-continues on *json.UnmarshalTypeError, so
		// {"error":123,"error_description":"session expired"} still yields a
		// usable description alongside a non-nil error; gating on a nil error
		// throws the good message away and falls through to a bare status
		// code. A syntax error is still a hard skip — nothing was decoded, and
		// a non-JSON body is not something to quote back at the operator.
		var typeErr *json.UnmarshalTypeError
		if uerr := json.Unmarshal(body, &errResp); uerr == nil || errors.As(uerr, &typeErr) {
			// RedactSecrets, not the slog ReplaceAttr hook: that hook only
			// scrubs log ATTRIBUTES and never sees a returned error. This
			// string is printed to the terminal by `ox status` and captured in
			// CI logs, so a server that echoes the presented credential back
			// in its error body would leak it there. Do not "simplify" this
			// into the redactedBody call above — they cover different sinks.
			if errResp.ErrorDescription != "" {
				return nil, rejectedByStatus(resp.StatusCode, fmt.Errorf("server rejected token: %s", logger.RedactSecrets(errResp.ErrorDescription)))
			}
			if errResp.Error != "" {
				return nil, rejectedByStatus(resp.StatusCode, fmt.Errorf("server rejected token: %s", logger.RedactSecrets(errResp.Error)))
			}
		}
		return nil, rejectedByStatus(resp.StatusCode, fmt.Errorf("server rejected token (HTTP %d)", resp.StatusCode))
	}

	var result IntrospectResult
	if err := json.Unmarshal(body, &result); err != nil {
		// A type error and a syntax error are different in kind, and that
		// difference decides whether the answer is still usable.
		//
		// This endpoint exists to stop spuriously rejecting live credentials.
		// Rejecting one because `scope` came back as a number is the same
		// defect as rejecting one because `expires_at` did (see FlexTime). The
		// decoder saves a type error and keeps going, so every other field is
		// still populated. Active and PrincipalKind are the only two fields
		// the CLI consumes for a validity decision — if both decoded cleanly,
		// the server answered the question we asked, and a wrong-typed field
		// we never read is not grounds to throw that answer away.
		//
		// A syntax error is different in kind: nothing decoded, so Active
		// being false is an artifact of the parse failing rather than a
		// statement from the server. The same reasoning covers a type error on
		// `active` itself — a zero value is the ABSENCE of a verdict, never a
		// verdict — so both fail closed.
		//
		// Do not loosen this further. The two fields gated on are load-bearing
		// precisely because they are the two we act on; tolerating a type
		// error on either would turn "we could not read the answer" into "yes".
		var typeErr *json.UnmarshalTypeError
		if !errors.As(err, &typeErr) || !result.Active || result.PrincipalKind == "" {
			return nil, fmt.Errorf("malformed introspection response: %w", err)
		}
		slog.Debug("introspection response had an unreadable field, principal decoded cleanly",
			"endpoint", ep, "field", typeErr.Field, "principal_kind", result.PrincipalKind, "error", err)
	}
	if !result.Active {
		// Defensive only: the server contract is a bare 401 for any invalid
		// token rather than a 200 carrying {"active":false}, but a future
		// server change or a non-conformant test double could still hand
		// this back, and treating it as valid would be the wrong failure
		// direction.
		return nil, fmt.Errorf("server reported inactive token")
	}
	return &result, nil
}

// ValidateTokenServerSide checks whether the server accepts the given bearer
// token. It calls the single family-agnostic introspection endpoint
// regardless of token family (session/JWT, personal oxp_, team oxt_) — see
// IntrospectEndpoint. Returns nil on success, or an error describing the
// rejection.
func ValidateTokenServerSide(ep, accessToken string) error {
	_, err := Introspect(ep, accessToken)
	return err
}

// --- Server-validated auth checks ---
// These contact the server to verify the token is actually accepted.
// Use for commands that gate real work: init, doctor, status.

// IsAuthenticatedForEndpoint validates that the user has a working token for
// the given endpoint by checking both local validity and server acceptance
// (via the single family-agnostic introspection endpoint, regardless of token
// family).
func IsAuthenticatedForEndpoint(ep string) (bool, error) {
	// local credential check first (fast-fail if no token)
	token, err := EnsureValidTokenForEndpoint(ep, 0)
	if err != nil {
		// "The credential you named is unusable" must not collapse into "you
		// have no credential". They have different remedies: `ox login` mints
		// personal tokens and cannot replace a team service token, so
		// reporting a truncated SAGEOX_TOKEN as "not logged in" sends a CI
		// operator to a command that cannot fix their problem. Every other
		// error stays swallowed on purpose — see the return below.
		if errors.Is(err, ErrEnvTokenMalformed) {
			return false, err
		}
		raw, _ := GetTokenForEndpoint(ep)
		if raw != nil {
			return false, fmt.Errorf("token refresh failed: %w", err)
		}
		// A missing or unreadable auth.json is not a failure to report: being
		// logged out is a normal state, and most callers here use
		// `authenticated, _ :=` and would be unaffected anyway.
		return false, nil
	}
	if token == nil {
		return false, nil
	}

	// server-side validation
	if err := ValidateTokenServerSide(ep, token.AccessToken); err != nil {
		slog.Debug("server-side auth validation failed", "endpoint", ep, "error", err)
		return false, err
	}

	return true, nil
}

// IsAuthenticated validates that the user has a working token for the current
// endpoint, including server-side validation.
func IsAuthenticated() (bool, error) {
	return IsAuthenticatedForEndpoint(endpoint.Get())
}

// --- Local-only credential checks ---
// These only check local auth.json state. Use for fast checks where network
// latency is unacceptable: login prompts, daemon hot paths, background polling.

// IsAuthCredentialValidForEndpoint checks if a locally valid (non-expired) token
// exists for a specific endpoint. Does NOT contact the server.
func IsAuthCredentialValidForEndpoint(ep string) (bool, error) {
	token, err := EnsureValidTokenForEndpoint(ep, 0)
	if err != nil {
		// Same reasoning as IsAuthenticatedForEndpoint: a named-but-unusable
		// credential is a different fact from no credential, and only that one
		// is worth reporting. Everything else still degrades to "not
		// authenticated, no error".
		if errors.Is(err, ErrEnvTokenMalformed) {
			return false, err
		}
		raw, _ := GetTokenForEndpoint(ep)
		if raw != nil {
			return false, fmt.Errorf("token refresh failed: %w", err)
		}
		return false, nil
	}

	if token == nil {
		return false, nil
	}

	return true, nil
}

// IsAuthCredentialValid checks if a locally valid (non-expired) token exists for
// the current endpoint. Does NOT contact the server.
func IsAuthCredentialValid() (bool, error) {
	return IsAuthCredentialValidForEndpoint(endpoint.Get())
}
