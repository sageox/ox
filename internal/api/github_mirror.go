package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/sageox/ox/internal/githubmirror"
	"github.com/sageox/ox/internal/logger"
	"github.com/sageox/ox/internal/useragent"
)

// gitHubMirrorItemsPath is the relay endpoint; %s is a team id or slug.
const gitHubMirrorItemsPath = "/api/v1/teams/%s/github-mirror/items"

// gitHubMirrorRelayTimeout replaces the RepoClient's 10s default for this one
// call. A full batch is 50 items, each with a description and every human
// comment, and the server scans every segment before it answers — more than
// ten seconds on a slow link or a busy scanner, well within what the daemon
// can wait for in the background.
const gitHubMirrorRelayTimeout = 30 * time.Second

// maxGitHubMirrorResponseBytes caps the response read. A relay receipt is one
// short result per item and an error envelope is smaller still; 1 MiB is
// generous headroom while still refusing an unbounded body from a hostile or
// broken server.
const maxGitHubMirrorResponseBytes = 1 << 20

var (
	// ErrGitHubMirrorNotEnabled: 404 with an empty body — the mirror is not
	// enabled for this account.
	ErrGitHubMirrorNotEnabled = errors.New("the GitHub mirror is not enabled for your account")
	// ErrGitHubMirrorNotAMember: 404 with the nested error envelope.
	ErrGitHubMirrorNotAMember = errors.New("no such team, or you are not a member of it")
	// ErrGitHubMirrorUnsupported: 404 with a flat envelope or plain text —
	// this server has no mirror route.
	ErrGitHubMirrorUnsupported = errors.New("this SageOx server does not support the GitHub mirror")
	// ErrGitHubMirrorTooLarge: 413.
	ErrGitHubMirrorTooLarge = errors.New("relay batch is too large")
	// ErrGitHubMirrorValidation is the class of every 400; the concrete error
	// is *GitHubMirrorValidationError.
	ErrGitHubMirrorValidation = errors.New("the server rejected the relay batch")
	// ErrGitHubMirrorBusy is the class of 429 and 503; the concrete error is
	// *GitHubMirrorBusyError.
	ErrGitHubMirrorBusy = errors.New("the GitHub mirror is busy")
)

// GitHubMirrorValidationError is a 400 with the server's per-field messages.
// Fields is nil when the server sent no per-field details.
//
// It satisfies errors.Is(err, ErrGitHubMirrorValidation).
type GitHubMirrorValidationError struct {
	Message string
	Fields  map[string]string
}

func (e *GitHubMirrorValidationError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = ErrGitHubMirrorValidation.Error()
	}
	if len(e.Fields) == 0 {
		return msg
	}
	// Sorted so the rendering is stable: map iteration order would otherwise
	// shuffle the fields between runs and make the message untestable.
	parts := make([]string, 0, len(e.Fields))
	for _, k := range slices.Sorted(maps.Keys(e.Fields)) {
		parts = append(parts, k+": "+e.Fields[k])
	}
	return msg + " (" + strings.Join(parts, "; ") + ")"
}

func (e *GitHubMirrorValidationError) Is(target error) bool {
	return target == ErrGitHubMirrorValidation
}

// GitHubMirrorBusyError is a 429 or 503. RetryAfter is 0 when the server
// sent no usable Retry-After, and the caller then applies its own backoff.
//
// It satisfies errors.Is(err, ErrGitHubMirrorBusy).
type GitHubMirrorBusyError struct {
	Status     int
	Message    string
	RetryAfter time.Duration
}

func (e *GitHubMirrorBusyError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = ErrGitHubMirrorBusy.Error()
	}
	// The status tells a rate limit (429) from an unavailable store (503),
	// which a reader of a log line needs even when the server's wording is
	// identical for both.
	var detail []string
	if e.Status != 0 {
		detail = append(detail, fmt.Sprintf("HTTP %d", e.Status))
	}
	if e.RetryAfter > 0 {
		detail = append(detail, "retry after "+e.RetryAfter.String())
	}
	if len(detail) == 0 {
		return msg
	}
	return msg + " (" + strings.Join(detail, ", ") + ")"
}

func (e *GitHubMirrorBusyError) Is(t error) bool { return t == ErrGitHubMirrorBusy }

// parseGitHubMirrorError maps a non-2xx relay response onto this endpoint's
// errors. It is the endpoint's OWN mapper: the same 404 status means three
// different things here, and only the body tells them apart.
//
//	404, empty body                 -> ErrGitHubMirrorNotEnabled   (the mirror is off for this account)
//	404, nested {error:{code,msg}}  -> ErrGitHubMirrorNotAMember   (the membership layer's answer)
//	404, flat {error:"…"} or text   -> ErrGitHubMirrorUnsupported  (no mirror route on this server)
//
// The nested envelope is the membership layer's; the router's own not-found
// answer is a FLAT JSON envelope ({"error":"route not registered"}) or plain
// text. Reading "any JSON 404" as a membership answer would tell a daemon
// talking to a server without the route that the user is not on the team.
//
// Every other status is decided on the status alone, with the body supplying
// the server's wording and, for 400, its structured details.
func parseGitHubMirrorError(status int, body []byte, header http.Header) error {
	se := parseServerError(body)

	switch status {
	case http.StatusBadRequest:
		return &GitHubMirrorValidationError{
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
			return ErrGitHubMirrorNotEnabled
		}
		if se.nested {
			return ErrGitHubMirrorNotAMember
		}
		return ErrGitHubMirrorUnsupported

	case http.StatusRequestEntityTooLarge:
		if msg := se.message(); msg != "" {
			return fmt.Errorf("%w: %s", ErrGitHubMirrorTooLarge, msg)
		}
		return ErrGitHubMirrorTooLarge

	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return &GitHubMirrorBusyError{
			Status:     status,
			Message:    se.message(),
			RetryAfter: parseRetryAfter(header),
		}
	}

	// 415, 500, and anything else: no special handling, just the server's
	// wording (or its raw body) behind the status. A 3xx lands here too, since
	// the client refuses to follow redirects.
	if msg := se.message(); msg != "" {
		return fmt.Errorf("HTTP %d: %s", status, msg)
	}
	if raw := strings.TrimSpace(string(body)); raw != "" {
		return fmt.Errorf("HTTP %d: %s", status, raw)
	}
	return fmt.Errorf("HTTP %d", status)
}

// gitHubMirrorItemsURL builds the relay URL for a team reference.
//
// teamRef is escaped as a single path segment: an unescaped `?` or `#` would
// truncate the path, land on a different route, and be reported as "this
// server does not support the GitHub mirror" instead of "bad team name".
func (c *RepoClient) gitHubMirrorItemsURL(teamRef string) string {
	return strings.TrimSuffix(c.baseURL, "/") + fmt.Sprintf(gitHubMirrorItemsPath, url.PathEscape(teamRef))
}

// gitHubMirrorClient returns a per-call copy of the client tuned for one
// relay: a longer timeout, and no redirects. Go's default policy rewrites a
// redirected POST into a GET and drops the body, so a canonicalizing redirect
// in front of the API would turn "relay these items" into a plain GET of some
// other resource — which can answer 200 with a body that happens to decode,
// making the daemon record items as relayed when no handler ever saw them.
// Surfacing the 3xx as an error is the honest outcome.
func (c *RepoClient) gitHubMirrorClient() *http.Client {
	clone := *c.httpClient
	clone.Timeout = gitHubMirrorRelayTimeout
	clone.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &clone
}

// RelayGitHubMirrorItems sends one batch (at most githubmirror.MaxBatchItems)
// to the mirror API and returns the per-item results. teamRef may be a team id
// or a slug.
//
// Outcomes, in the order a caller should check them:
//
//   - *GitHubMirrorValidationError (errors.Is ErrGitHubMirrorValidation): 400.
//     Record the batch as rejected; retrying the same bytes cannot succeed.
//   - ErrUnauthorized: 401 — not signed in.
//   - ErrGitHubMirrorNotEnabled, ErrGitHubMirrorNotAMember,
//     ErrGitHubMirrorUnsupported: the three 404s, told apart by the body.
//   - ErrGitHubMirrorTooLarge: 413 — halve the batch and retry.
//   - *GitHubMirrorBusyError (errors.Is ErrGitHubMirrorBusy): 429 or 503.
//   - ErrVersionUnsupported: the server refuses this CLI version.
//   - ErrInvalidTeamRef: teamRef could not be placed in a URL path safely.
//
// Only HTTP 200 is a success. Any other 2xx means the request reached
// something that was not the relay handler, and decoding its body would forge
// a "relayed" outcome for items no server stored.
//
// Neither the request nor the response body is ever logged: the items are
// teammates' GitHub text, which is not ours to copy into a log file.
func (c *RepoClient) RelayGitHubMirrorItems(ctx context.Context, teamRef string, req githubmirror.RelayRequest) (*githubmirror.RelayResponse, error) {
	if err := validateTeamRef(teamRef); err != nil {
		return nil, err
	}

	// json.Marshal escapes <, >, and & as \u003c, \u003e, and \u0026 — six
	// bytes for one. PR descriptions are tag- and ampersand-dense markdown, so
	// that expansion could push a full batch over the server's body cap even
	// though the text itself fits. The server decodes either form identically.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(req); err != nil {
		return nil, fmt.Errorf("failed to encode request: %w", err)
	}

	reqURL := c.gitHubMirrorItemsURL(teamRef)
	logger.LogHTTPRequest(http.MethodPost, reqURL)
	start := time.Now()

	// useragent.NewRequest sets the User-Agent; it must not be overridden.
	httpReq, err := useragent.NewRequest(ctx, http.MethodPost, reqURL, bytes.NewReader(buf.Bytes()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.authToken))
	}

	resp, err := c.gitHubMirrorClient().Do(httpReq)
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
	// broken server cannot make the daemon buffer an unbounded response.
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxGitHubMirrorResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}
	if len(respBody) > maxGitHubMirrorResponseBytes {
		return nil, fmt.Errorf("HTTP %d: response body exceeds %d bytes", resp.StatusCode, maxGitHubMirrorResponseBytes)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseGitHubMirrorError(resp.StatusCode, respBody, resp.Header)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: expected 200 OK from GitHub mirror relay", resp.StatusCode)
	}

	var result githubmirror.RelayResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	// `{}` and `null` both unmarshal cleanly into a zero response. Without a
	// repo status the caller cannot tell "enabled" from "refused", and a zero
	// value must never be read as either.
	if result.RepoStatus == "" {
		return nil, fmt.Errorf("malformed receipt: missing repo_status")
	}
	return &result, nil
}
