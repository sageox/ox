package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sageox/ox/internal/logger"
	"github.com/sageox/ox/internal/useragent"
)

// bulletinPostsPath is the publish endpoint for a team's bulletin board. The
// %s accepts either a team id (team_xxx) or a team slug — the server resolves
// both.
const bulletinPostsPath = "/api/v1/teams/%s/bulletin/posts"

// Bulletin post formats. These are the only two values the server accepts in
// BulletinPublishRequest.Format.
const (
	BulletinFormatMarkdown = "markdown"
	BulletinFormatHTML     = "html"
)

// BulletinBoardGeneral is the only board that exists in phase 1. An empty
// Board on the request means the same thing; the server fills in "general".
const BulletinBoardGeneral = "general"

// bulletinPublishTimeout replaces the RepoClient's 10s default for this one
// call. A post may carry up to 1 MiB of content, and 1 MiB on a slow link
// needs more than ten seconds before the server has even started to answer.
const bulletinPublishTimeout = 60 * time.Second

// maxBulletinResponseBytes caps the response read. A publish receipt is a few
// hundred bytes and an error envelope is smaller still; 1 MiB is generous
// headroom while still refusing an unbounded body from a hostile or broken
// server.
const maxBulletinResponseBytes = 1 << 20

// BulletinPublishRequest is the body of POST /api/v1/teams/{team_ref}/bulletin/posts.
//
// The server requires EXACTLY these six keys and rejects unknown fields, so
// no field carries omitempty: a blank Board is legal (it means "general") and
// must still be sent as "board": "". The content is the file's bytes as-is —
// the server hashes exactly what it receives, and the caller compares that
// hash against a local SHA-256 of the same bytes.
type BulletinPublishRequest struct {
	Board   string `json:"board"`
	Slug    string `json:"slug"`
	Title   string `json:"title"`
	Format  string `json:"format"`
	Content string `json:"content"`
	TTL     string `json:"ttl"`
}

// BulletinPublishResult is the 201 body: the receipt for a post the server has
// committed to the team's Team Context. Path is relative to the Team Context
// root and names the stored body file; ContentSHA256 is the SHA-256 of exactly
// the submitted content bytes, which the caller is expected to verify.
type BulletinPublishResult struct {
	TeamID        string    `json:"team_id"`
	Board         string    `json:"board"`
	Path          string    `json:"path"`
	Slug          string    `json:"slug"`
	Title         string    `json:"title"`
	Format        string    `json:"format"`
	ContentSHA256 string    `json:"content_sha256"`
	ContentBytes  int       `json:"content_bytes"`
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	CommitID      string    `json:"commit_id"`
}

// BulletinDuplicate describes the post that already holds the submitted
// content, as reported in a 409's details. The timestamps are optional on the
// wire and are kept as strings so an absent value stays visibly absent rather
// than becoming the zero time.
type BulletinDuplicate struct {
	Path      string `json:"path"`
	State     string `json:"state"`
	CreatedAt string `json:"created_at,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

// Bulletin-specific sentinels. Callers branch on these with errors.Is to pick
// an exit code and a next step; every one of them is a normal, expected answer
// rather than a failure worth a stack trace.
var (
	// ErrBulletinNotEnabled is a 404 with an EMPTY body: the server evaluates
	// the bulletin feature per person, and this person is outside the pilot.
	// Do not retry — nothing on this machine can change the answer.
	ErrBulletinNotEnabled = errors.New("the bulletin board is not enabled for your account")

	// ErrBulletinNotAMember is a 404 carrying the NESTED JSON error envelope
	// ({"error":{"code":"NOT_FOUND","message":"team not found"}}), which is
	// how the membership layer answers. The server deliberately answers 404
	// rather than 403 for a team the caller is not a member of, so team
	// existence cannot be probed. Preserve that ambiguity when rendering:
	// never claim the team does or does not exist.
	ErrBulletinNotAMember = errors.New("no such team, or you are not a member of it")

	// ErrBulletinUnsupported is a 404 whose body is the router's own answer
	// rather than the membership layer's: on the live server that is the
	// FLAT JSON envelope {"error":"route not registered"}; on a deployment
	// with a default router it is plain text. Either way the route is not
	// registered, i.e. this deployment has no bulletin board. Do not retry
	// against this endpoint.
	ErrBulletinUnsupported = errors.New("this SageOx server does not support bulletin posts")

	// ErrBulletinTooLarge is a 413: the request exceeded the server's 2 MiB
	// body cap or the content exceeded its 1 MiB cap.
	ErrBulletinTooLarge = errors.New("post content is too large")

	// ErrBulletinValidation is the class of every 400. The concrete error is
	// a *BulletinValidationError carrying the server's per-field messages.
	ErrBulletinValidation = errors.New("the server rejected the post")

	// ErrBulletinDuplicate is the class of every 409. The concrete error is a
	// *BulletinDuplicateError naming the post that already holds this
	// content. Identical content is one post per board, whatever the slug or
	// format.
	ErrBulletinDuplicate = errors.New("this board already holds a post with identical content")

	// ErrBulletinUnavailable is the class of every 503: the team's Team
	// Context store cannot be reached right now. Retrying the SAME request is
	// safe — an identical retry is one post, because identical content is a
	// duplicate. The concrete error is a *BulletinUnavailableError carrying
	// the server's Retry-After hint.
	ErrBulletinUnavailable = errors.New("the team's Team Context is temporarily unavailable")
)

// BulletinValidationError is a 400: the server refused the post on its own
// input rules. Fields maps the wire field name (board, slug, title, format,
// content, ttl) to the server's message for it, and is nil when the server
// sent no per-field details — which happens when the body itself was not a
// valid request (malformed JSON, an unknown key).
//
// It satisfies errors.Is(err, ErrBulletinValidation).
type BulletinValidationError struct {
	Message string
	Fields  map[string]string
}

func (e *BulletinValidationError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = ErrBulletinValidation.Error()
	}
	if len(e.Fields) == 0 {
		return msg
	}
	// Sorted so the rendering is stable: map iteration order would otherwise
	// shuffle the fields between runs and make the message untestable.
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+": "+e.Fields[k])
	}
	return msg + " (" + strings.Join(parts, "; ") + ")"
}

func (e *BulletinValidationError) Is(target error) bool { return target == ErrBulletinValidation }

// BulletinDuplicateError is a 409: the board already holds a post whose
// content hashes identically. Duplicate names that post; its timestamps may be
// empty when the server omitted them.
//
// It satisfies errors.Is(err, ErrBulletinDuplicate).
type BulletinDuplicateError struct {
	Message   string
	Duplicate BulletinDuplicate
}

func (e *BulletinDuplicateError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = ErrBulletinDuplicate.Error()
	}
	if e.Duplicate.Path != "" {
		msg += " (existing post: " + e.Duplicate.Path + ")"
	}
	return msg
}

func (e *BulletinDuplicateError) Is(target error) bool { return target == ErrBulletinDuplicate }

// BulletinUnavailableError is a 503: the team's Team Context store is
// unavailable. RetryAfter is the server's Retry-After header parsed as whole
// seconds, or 0 when the header was absent or not an integer — the caller
// then falls back to its own backoff.
//
// It satisfies errors.Is(err, ErrBulletinUnavailable).
type BulletinUnavailableError struct {
	Message    string
	RetryAfter time.Duration
}

func (e *BulletinUnavailableError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = ErrBulletinUnavailable.Error()
	}
	if e.RetryAfter > 0 {
		msg += fmt.Sprintf(" (retry after %s)", e.RetryAfter)
	}
	return msg
}

func (e *BulletinUnavailableError) Is(target error) bool { return target == ErrBulletinUnavailable }

// nestedErrorDetails pulls `error.details` out of the nested envelope
// ({"error":{"code","message","details":...}}) as raw JSON. It returns nil
// for any body that does not carry that key — the flat envelope, a body with
// no details, or something that is not JSON — so callers can try a shape
// against it and simply get nothing.
func nestedErrorDetails(body []byte) json.RawMessage {
	var probe struct {
		Error struct {
			Details json.RawMessage `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(probe.Error.Details), []byte("null")) {
		return nil
	}
	return probe.Error.Details
}

// decodeValidationFields turns a 400's details into a field -> message map.
// The server sends a flat map of strings; anything else (absent, null, a
// non-object, a non-string value) is tolerated rather than failing the whole
// decode, because the message alone is still worth showing.
func decodeValidationFields(body []byte) map[string]string {
	details := nestedErrorDetails(body)
	if details == nil {
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(details, &raw); err != nil || len(raw) == 0 {
		return nil
	}
	fields := make(map[string]string, len(raw))
	for k, v := range raw {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			fields[k] = s
			continue
		}
		// A non-string value is unexpected but not worth dropping; the
		// compact JSON text is still a usable message.
		fields[k] = strings.TrimSpace(string(v))
	}
	return fields
}

// decodeDuplicate turns a 409's details into the existing post's description.
// A missing or malformed details object yields the zero value: the sentinel
// still tells the caller what happened, just without a path to point at.
func decodeDuplicate(body []byte) BulletinDuplicate {
	var dup BulletinDuplicate
	if details := nestedErrorDetails(body); details != nil {
		_ = json.Unmarshal(details, &dup)
	}
	return dup
}

// parseRetryAfter reads Retry-After as whole seconds. The header is the
// server's to choose; anything that is not a non-negative integer (absent,
// blank, an HTTP-date) yields 0 and the caller uses its own backoff.
func parseRetryAfter(header http.Header) time.Duration {
	raw := strings.TrimSpace(header.Get("Retry-After"))
	if raw == "" {
		return 0
	}
	secs, err := strconv.Atoi(raw)
	if err != nil || secs < 0 || secs > maxRetryAfterSeconds {
		// Out of range covers an epoch-milliseconds timestamp a misconfigured
		// proxy might put here: multiplied into nanoseconds it would wrap
		// negative and read as "retry at once".
		return 0
	}
	return time.Duration(secs) * time.Second
}

// maxRetryAfterSeconds is the largest Retry-After that survives conversion
// to a time.Duration without overflow. Anything larger is not a number of
// seconds anyone meant.
const maxRetryAfterSeconds = int(math.MaxInt64 / int64(time.Second))

// parseBulletinError maps a non-2xx publish response onto this endpoint's
// errors. It is the endpoint's OWN mapper — the invite mapper shares the
// dual-envelope parser but not the outcomes: the same 404 status means three
// different things here, and only the body tells them apart.
//
//	404, empty body                 -> ErrBulletinNotEnabled   (this person is outside the pilot)
//	404, nested {error:{code,msg}}  -> ErrBulletinNotAMember   (the membership layer's answer)
//	404, flat {error:"…"} or text   -> ErrBulletinUnsupported  (no bulletin route on this server)
//
// Captured from the live server on 2026-09-21: outside the pilot answers with
// no body at all; a team the caller cannot reach answers
// {"error":{"code":"NOT_FOUND","message":"team not found"}}; an unrouted path
// answers {"error":"route not registered"} with a JSON content type.
//
// Every other status is decided on the status alone, with the body supplying
// the server's wording and, for 400 and 409, its structured details.
func parseBulletinError(status int, body []byte, header http.Header) error {
	se := parseServerError(body)

	switch status {
	case http.StatusBadRequest:
		return &BulletinValidationError{
			Message: se.message(),
			Fields:  decodeValidationFields(body),
		}

	case http.StatusUnauthorized:
		return ErrUnauthorized

	case http.StatusForbidden:
		// Not part of this endpoint's contract, but if a proxy or a future
		// server answers 403, carry its wording through verbatim rather than
		// inventing a reason.
		return &ForbiddenError{Reason: se.message()}

	case http.StatusNotFound:
		if len(bytes.TrimSpace(body)) == 0 {
			return ErrBulletinNotEnabled
		}
		// Only the NESTED envelope is the membership answer. The router's
		// own not-found answer on the live server is a FLAT JSON envelope
		// ({"error":"route not registered"}), not plain text, so "any JSON
		// means not a member" would report a server without the route as a
		// membership problem — the opposite of the right next step.
		if se.nested {
			return ErrBulletinNotAMember
		}
		return ErrBulletinUnsupported

	case http.StatusConflict:
		return &BulletinDuplicateError{
			Message:   se.message(),
			Duplicate: decodeDuplicate(body),
		}

	case http.StatusRequestEntityTooLarge:
		if msg := se.message(); msg != "" {
			return fmt.Errorf("%w: %s", ErrBulletinTooLarge, msg)
		}
		return ErrBulletinTooLarge

	case http.StatusServiceUnavailable:
		return &BulletinUnavailableError{
			Message:    se.message(),
			RetryAfter: parseRetryAfter(header),
		}
	}

	// 415, 500, and anything else: no special handling, just the server's
	// wording (or its raw body) behind the status.
	if msg := se.message(); msg != "" {
		return fmt.Errorf("HTTP %d: %s", status, msg)
	}
	if raw := strings.TrimSpace(string(body)); raw != "" {
		return fmt.Errorf("HTTP %d: %s", status, raw)
	}
	return fmt.Errorf("HTTP %d", status)
}

// bulletinPostsURL builds the publish URL for a team reference.
//
// teamRef is escaped as a single path segment. It reaches here verbatim from
// `--team` or the repo config, and an unescaped `?` or `#` would silently
// truncate the path — the request would land on a different route, 404 with
// no JSON body, and be reported as "this server does not support bulletin
// posts" instead of "bad team name".
func (c *RepoClient) bulletinPostsURL(teamRef string) string {
	return strings.TrimSuffix(c.baseURL, "/") + fmt.Sprintf(bulletinPostsPath, url.PathEscape(teamRef))
}

// bulletinClient returns a per-call copy of the client tuned for one publish.
//
// Timeout is raised because a 1 MiB body on a slow link needs more than the
// 10s default. Redirects are refused for the same reason as noRedirectClient:
// Go's default policy rewrites a redirected POST into a GET and drops the
// body, so a canonicalizing redirect in front of the API would turn "publish
// this post" into a plain GET of some other resource — which can answer 200
// with a body that happens to unmarshal, making ox report a post as published
// when no handler ever saw it. Surfacing the 3xx as an error is the honest
// outcome.
func (c *RepoClient) bulletinClient() *http.Client {
	clone := *c.httpClient
	clone.Timeout = bulletinPublishTimeout
	clone.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &clone
}

// PublishBulletinPost publishes one post to a team's bulletin board and
// returns the server's receipt.
//
// teamRef may be a team id or a slug. The request is sent exactly as given —
// the caller owns local validation (slug shape, TTL range, content size and
// encoding) so that nothing invalid leaves the machine, and the caller also
// verifies the returned ContentSHA256 against the bytes it sent, since only
// it holds them.
//
// Outcomes, in the order a caller should check them:
//
//   - *BulletinValidationError (errors.Is ErrBulletinValidation): 400.
//   - ErrUnauthorized: 401 — not signed in, or a team service token was used;
//     publishing is a person's act.
//   - ErrBulletinNotEnabled, ErrBulletinNotAMember, ErrBulletinUnsupported:
//     the three 404s, told apart by the body.
//   - *BulletinDuplicateError (errors.Is ErrBulletinDuplicate): 409.
//   - ErrBulletinTooLarge: 413.
//   - *BulletinUnavailableError (errors.Is ErrBulletinUnavailable): 503;
//     retrying the identical request is safe.
//   - ErrVersionUnsupported: the server refuses this CLI version.
//   - ErrInvalidTeamRef: teamRef could not be placed in a URL path safely.
//
// The request and response bodies are never logged: the content is the
// teammate's post, which may be large and is not ours to copy into a log file.
func (c *RepoClient) PublishBulletinPost(ctx context.Context, teamRef string, req BulletinPublishRequest) (*BulletinPublishResult, error) {
	if err := validateTeamRef(teamRef); err != nil {
		return nil, err
	}

	// json.Marshal escapes <, >, and & as \u003c, \u003e, and \u0026 — six
	// bytes for one. For a tag-dense 1 MiB HTML post that expansion can
	// double the body and push it over the server's 2 MiB request cap even
	// though the content itself is within its 1 MiB limit. The server decodes
	// either form identically, so send the bytes as they are.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(req); err != nil {
		return nil, fmt.Errorf("failed to encode request: %w", err)
	}

	reqURL := c.bulletinPostsURL(teamRef)
	logger.LogHTTPRequest(http.MethodPost, reqURL)
	start := time.Now()

	// useragent.NewRequest sets the User-Agent; it must not be overridden.
	// The server records it verbatim as the publishing client.
	httpReq, err := useragent.NewRequest(ctx, http.MethodPost, reqURL, bytes.NewReader(buf.Bytes()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.authToken))
	}

	resp, err := c.bulletinClient().Do(httpReq)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError(http.MethodPost, reqURL, err, duration)
		return nil, fmt.Errorf("network error: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	logger.LogHTTPResponse(http.MethodPost, reqURL, resp.StatusCode, duration)

	if CheckVersionResponse(resp) {
		return nil, ErrVersionUnsupported
	}

	// Read one byte past the cap so an oversized body is DETECTED rather than
	// silently truncated into a confusing decode error, and so a hostile or
	// broken server cannot make the CLI buffer an unbounded response.
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxBulletinResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}
	if len(respBody) > maxBulletinResponseBytes {
		return nil, fmt.Errorf("HTTP %d: response body exceeds %d bytes", resp.StatusCode, maxBulletinResponseBytes)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseBulletinError(resp.StatusCode, respBody, resp.Header)
	}
	// Only 201 carries a receipt. Any other 2xx means the request reached
	// something that was not the publish handler, and decoding its body as a
	// receipt would forge a success.
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("HTTP %d: expected 201 Created from bulletin publish", resp.StatusCode)
	}

	var result BulletinPublishResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	// `{}` and `null` both unmarshal cleanly into a zero receipt. A receipt
	// without the path and the digest describes nothing the caller can act
	// on, and the caller's hash check would otherwise report it as a hash
	// mismatch rather than the missing receipt it is.
	if result.Path == "" || result.ContentSHA256 == "" {
		return nil, fmt.Errorf("malformed receipt: missing path or content_sha256")
	}
	return &result, nil
}
