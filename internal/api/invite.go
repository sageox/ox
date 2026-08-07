package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sageox/ox/internal/logger"
	"github.com/sageox/ox/internal/useragent"
)

// teamInvitesPath is the create-invite endpoint. The %s accepts either a
// team_id (team_xxx) or a team slug — the server's RequireTeamMember
// middleware resolves both.
const teamInvitesPath = "/api/v1/teams/%s/invites"

// Team roles. The server rejects anything outside this set with a 400 whose
// message is load-bearing ("role must be owner, admin, or member").
const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// codePersonalTeamImmutable is the server's stable wire code for a mutation
// aimed at a private per-user team. It shares HTTP 409 with the
// already-invited case, so the code — never the status — disambiguates.
const codePersonalTeamImmutable = "personal_team_immutable"

// CreateInviteRequest is the body of POST /api/v1/teams/{team_id}/invites.
//
// Role is REQUIRED despite the server's OpenAPI document claiming it is
// optional with a "member" default: validateInviteRole rejects an empty
// string with a 400. Never send this struct with a blank Role.
type CreateInviteRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

// InviteResponse is the 201 body.
//
// It deliberately OMITS the server's `token` field. The server returns the
// plaintext invite token in-band so the web composer can offer a copy-link
// fallback; ox has no such need, and an invite token is a live credential.
// Not declaring the field is what structurally guarantees ox cannot print it,
// log it, or leak it into --json output — a guarantee a "remember not to
// print it" convention could not make.
type InviteResponse struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	TeamID    string `json:"team_id"`
	TeamName  string `json:"team_name"`
	Role      string `json:"role"`
	ExpiresAt string `json:"expires_at"`
}

// Invite-specific sentinels. Callers branch on these with errors.Is to render
// a per-recipient outcome; every one of them is a normal, expected answer
// rather than a failure worth a stack trace.
var (
	// ErrInviteExists is HTTP 409: an active invite for this address already
	// exists on this team. The desired end state already holds.
	ErrInviteExists = errors.New("an active invite already exists for this email")

	// ErrInviteForbidden is HTTP 403: the server refused this operation.
	//
	// The CLI deliberately does NOT model who is allowed to invite, list, or
	// cancel. That policy lives on the server and can change — today any
	// active member may invite, tomorrow it could be owner/admin only — and a
	// client that hardcodes today's rule would confidently give wrong advice
	// the day it changes. Callers wrap this sentinel around the SERVER's own
	// message and show that, rather than inventing an explanation.
	ErrInviteForbidden = errors.New("not permitted")

	// ErrInviteNotAMember is HTTP 404 carrying a JSON error body. The server
	// deliberately answers 404 rather than 403 for a team the caller is not a
	// member of, so team existence cannot be probed. Preserve that ambiguity
	// when rendering: never claim the team does or does not exist.
	ErrInviteNotAMember = errors.New("no such team, or you are not a member of it")

	// ErrPersonalTeam is HTTP 409 with code personal_team_immutable: private
	// per-user teams are structurally single-member and take no invitations.
	ErrPersonalTeam = errors.New("personal teams are single-member and cannot take invitations")

	// ErrInviteUnsupported is a bare 404 with no JSON error body — the route
	// is not registered, i.e. the server predates CLI invitations. Distinct
	// from ErrInviteNotAMember, which is a 404 that DOES carry a JSON body.
	ErrInviteUnsupported = errors.New("this SageOx server does not support CLI invitations yet")
)

// serverError models the two mutually incompatible error envelopes api-go
// returns on this one route. The handler's own failures use {success, error};
// the middleware's and the personal-team guard's use {error:{code,message}}.
// Both shapes arrive on the same request path depending on how far into the
// stack the request got, so every response must be tried against both.
type serverError struct {
	// Flat envelope: {"success": false, "error": "message"}
	Success *bool  `json:"success"`
	FlatMsg string `json:"-"`

	// Nested envelope: {"error": {"code": "...", "message": "..."}}
	Code       string `json:"-"`
	NestedMsg  string `json:"-"`
	wasDecoded bool
}

// parseServerError decodes whichever envelope the body carries. It reports
// wasDecoded=false for a body that is not recognizable JSON error output at
// all, which is the signal that distinguishes an unrouted 404 from a
// deliberate one.
func parseServerError(body []byte) serverError {
	var se serverError
	if len(bytes.TrimSpace(body)) == 0 {
		return se
	}

	// The `error` key is a string in the flat envelope and an object in the
	// nested one, so it has to be decoded as a raw message first.
	var probe struct {
		Success *bool           `json:"success"`
		Error   json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return se
	}
	se.Success = probe.Success

	if len(probe.Error) == 0 {
		// Valid JSON with no `error` key — not an error envelope we know.
		return se
	}

	var flat string
	if err := json.Unmarshal(probe.Error, &flat); err == nil {
		se.FlatMsg = flat
		se.wasDecoded = true
		return se
	}

	var nested struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(probe.Error, &nested); err == nil {
		se.Code = nested.Code
		se.NestedMsg = nested.Message
		se.wasDecoded = true
	}
	return se
}

// message returns whichever envelope's human message was present.
func (s serverError) message() string {
	if s.FlatMsg != "" {
		return s.FlatMsg
	}
	return s.NestedMsg
}

// parseInviteError maps a non-2xx create-invite response onto a sentinel.
// Both error envelopes are considered, and the two same-status collisions
// (409 already-invited vs personal-team, 404 non-member vs absent route) are
// resolved on the body, never on the status code alone.
func parseInviteError(status int, body []byte) error {
	se := parseServerError(body)

	switch status {
	case http.StatusUnauthorized:
		return ErrUnauthorized

	case http.StatusForbidden:
		// Carry the server's wording through verbatim — it is the only
		// authority on why this was refused.
		return &ForbiddenError{Reason: se.message()}

	case http.StatusNotFound:
		// A deliberate "you are not a member" 404 carries a JSON error body.
		// An unrouted request does not — chi answers with plain text. That
		// difference is the only way to tell "you can't see this team" from
		// "this server is too old", and they need opposite handling: the
		// first is one recipient's outcome, the second aborts the command.
		if se.wasDecoded {
			return ErrInviteNotAMember
		}
		return ErrInviteUnsupported

	case http.StatusConflict:
		if se.Code == codePersonalTeamImmutable {
			return ErrPersonalTeam
		}
		return ErrInviteExists
	}

	if msg := se.message(); msg != "" {
		return fmt.Errorf("HTTP %d: %s", status, msg)
	}
	if raw := strings.TrimSpace(string(body)); raw != "" {
		return fmt.Errorf("HTTP %d: %s", status, raw)
	}
	return fmt.Errorf("HTTP %d", status)
}

// ErrInvalidTeamRef is a team reference that cannot be placed in a URL path
// safely. Only dot segments qualify: everything else is escaped and handed to
// the server, which is the authority on whether a team exists.
var ErrInvalidTeamRef = errors.New("invalid team reference")

// validateTeamRef rejects the refs that survive escaping and still change which
// route the request reaches.
//
// url.PathEscape leaves "." and ".." untouched — dots are unreserved — so a
// ref of ".." builds ".../teams/../invites", which any normalizing proxy or
// server collapses to ".../invites": a different endpoint entirely. The
// resulting 404 would be reported to the user as "this server does not support
// CLI invitations". No team id or slug is ever a dot segment, so refusing them
// costs nothing.
func validateTeamRef(ref string) error {
	switch strings.TrimSpace(ref) {
	case "":
		return fmt.Errorf("%w: empty", ErrInvalidTeamRef)
	case ".", "..":
		return fmt.Errorf("%w: %q is a path segment, not a team", ErrInvalidTeamRef, ref)
	}
	return nil
}

// inviteURL builds the create/list URL for a team reference.
//
// teamRef is escaped as a single path segment. It reaches here verbatim from
// `--team`, and an unescaped `?` or `#` would silently truncate the path — the
// request would land on a different route, 404, and be reported to the user as
// "this server doesn't support invitations" instead of "bad team name".
func (c *RepoClient) inviteURL(teamRef string) string {
	return strings.TrimSuffix(c.baseURL, "/") + fmt.Sprintf(teamInvitesPath, url.PathEscape(teamRef))
}

// inviteIDURL builds the per-invite URL used to revoke a pending invitation.
func (c *RepoClient) inviteIDURL(teamRef, inviteID string) string {
	return c.inviteURL(teamRef) + "/" + url.PathEscape(inviteID)
}

// noRedirectClient returns a shallow copy of the client that refuses to follow
// redirects.
//
// Go's default policy rewrites a redirected POST into a GET and drops the body.
// A canonicalizing redirect in front of the API (http→https, host
// canonicalization, chi's RedirectSlashes) would therefore turn "create this
// invitation" into a plain GET of some other resource — which can answer 200
// with a body that happens to unmarshal, making ox report an invitation as sent
// when no handler ever saw the email address. Surfacing the 3xx as an error is
// the honest outcome. Go also strips Authorization across a cross-host
// redirect, so following one could not have worked anyway.
func (c *RepoClient) noRedirectClient() *http.Client {
	clone := *c.httpClient
	clone.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &clone
}

// CreateTeamInvite sends one invitation and returns the created invite.
//
// teamRef may be a team_id or a slug. One address per call — the server has no
// bulk form; callers invite several people by calling this repeatedly.
//
// A nil error means the invite row was created, NOT that an email was
// delivered: the server dispatches the mail asynchronously and a send failure
// never fails the request.
func (c *RepoClient) CreateTeamInvite(ctx context.Context, teamRef string, req CreateInviteRequest) (*InviteResponse, error) {
	if err := validateTeamRef(teamRef); err != nil {
		return nil, err
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	respBody, err := c.inviteRequest(ctx, "POST", c.inviteURL(teamRef), body)
	if err != nil {
		return nil, err
	}

	var invite InviteResponse
	if err := json.Unmarshal(respBody, &invite); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &invite, nil
}

// PendingInvite is one row of GET /api/v1/teams/{team_id}/invites.
//
// The list endpoint never returns tokens (the server builds these responses
// with includeToken=false), so unlike the create path there is nothing
// sensitive to withhold here.
type PendingInvite struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	Role        string `json:"role"`
	InviterName string `json:"inviter_name"`
	InvitedAt   string `json:"invited_at"`
	ExpiresAt   string `json:"expires_at"`
	Status      string `json:"status"`
}

// ListTeamInvites returns the team's invitations that are still outstanding —
// not yet accepted, not revoked, not expired.
//
// Whether the caller is allowed to see them is the server's decision; a
// refusal comes back as ErrInviteForbidden carrying the server's own reason.
func (c *RepoClient) ListTeamInvites(ctx context.Context, teamRef string) ([]PendingInvite, error) {
	if err := validateTeamRef(teamRef); err != nil {
		return nil, err
	}
	body, err := c.inviteRequest(ctx, "GET", c.inviteURL(teamRef), nil)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Invites []PendingInvite `json:"invites"`
		Total   int             `json:"total"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return resp.Invites, nil
}

// RevokeTeamInvite cancels a single pending invitation by its id — the id
// shown by ListTeamInvites, not the invite token. Answers 204 with no body.
func (c *RepoClient) RevokeTeamInvite(ctx context.Context, teamRef, inviteID string) error {
	if err := validateTeamRef(teamRef); err != nil {
		return err
	}
	_, err := c.inviteRequest(ctx, "DELETE", c.inviteIDURL(teamRef, inviteID), nil)
	return err
}

// inviteRequest performs a request against the invite endpoints and returns
// the 2xx body. Every invite operation goes through here, so the redirect
// policy, version check, and dual-envelope error mapping are applied uniformly
// and cannot drift between create, list, and revoke.
//
// It deliberately logs NEITHER the request body nor the response body. The
// request carries a recipient's email address (PII) and the create response
// carries a live invite token; both would otherwise land in a log file. This
// matches the same deliberate omission in RegisterRepo and MergeRepo.
func (c *RepoClient) inviteRequest(ctx context.Context, method, reqURL string, body []byte) ([]byte, error) {
	logger.LogHTTPRequest(method, reqURL)
	start := time.Now()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	httpReq, err := useragent.NewRequest(ctx, method, reqURL, reader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	if c.authToken != "" {
		httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.authToken))
	}

	resp, err := c.noRedirectClient().Do(httpReq)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError(method, reqURL, err, duration)
		return nil, fmt.Errorf("network error: %w", err)
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse(method, reqURL, resp.StatusCode, duration)

	if CheckVersionResponse(resp) {
		return nil, ErrVersionUnsupported
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseInviteError(resp.StatusCode, respBody)
	}
	return respBody, nil
}

// ForbiddenError is a 403 that carries the server's own explanation.
//
// The reason is held as a field rather than being formatted into the message
// and parsed back out: string round-tripping would break the moment the
// wrapping text changed, and silently — the caller would just start showing a
// generic sentence instead of the server's.
//
// It satisfies errors.Is(err, ErrInviteForbidden) so existing callers that
// only care about the class keep working.
type ForbiddenError struct {
	Reason string
}

func (e *ForbiddenError) Error() string {
	if e.Reason == "" {
		return ErrInviteForbidden.Error()
	}
	return ErrInviteForbidden.Error() + ": " + e.Reason
}

func (e *ForbiddenError) Is(target error) bool { return target == ErrInviteForbidden }

// InviteForbiddenReason returns the server's own explanation for a 403, or ""
// when it sent none — letting the caller fall back to something generic rather
// than inventing a policy claim.
func InviteForbiddenReason(err error) string {
	var fe *ForbiddenError
	if errors.As(err, &fe) {
		return fe.Reason
	}
	return ""
}
