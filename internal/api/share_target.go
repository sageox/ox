package api

import (
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

// shareTargetPath resolves a share link's opaque token to the entity it
// shares.
//
// Contract: GET /api/v1/shares/{token}/target (authenticated). 200 returns a
// ShareTarget. Every denial — unknown, revoked, or expired token, or a caller
// who is not a member of the owning team — is the same 404 with a JSON body
// {"error":"share not found"}, so a caller cannot probe which tokens exist.
// 401 means the credential was rejected. The lookup never consumes a share
// use.
const shareTargetPath = "/api/v1/shares/%s/target"

// maxShareTargetBodyBytes caps what the client will read from the lookup. A
// ShareTarget is a few hundred bytes; the cap only keeps a misbehaving server
// from making the CLI buffer an arbitrarily large body.
const maxShareTargetBodyBytes = 64 << 10

// shareNotFoundMessage is the error text the lookup's documented 404 carries.
// A 404 without it is a server that does not have the route at all.
const shareNotFoundMessage = "share not found"

var (
	// ErrShareNotFound: the server has the lookup and answered with its
	// documented denial — the share is unknown, revoked, expired, or not
	// shared with the caller's team. The server deliberately does not say
	// which.
	ErrShareNotFound = errors.New("share not found")
	// ErrShareLookupUnsupported: the server does not serve the lookup (a 404
	// without the documented body, 405, or 501) — an older deployment.
	ErrShareLookupUnsupported = errors.New("share lookup not supported by this server")
	// ErrShareLookupUnavailable: transport failure, timeout, or 5xx.
	ErrShareLookupUnavailable = errors.New("share lookup unavailable")
)

// ShareTarget is the entity a share link points at.
type ShareTarget struct {
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
	OwnerType  string `json:"owner_type"`
	OwnerID    string `json:"owner_id"`
	// RedirectPath is the web path the share opens; optional.
	RedirectPath string `json:"redirect_path,omitempty"`
}

// GetShareTarget resolves a share token with GET /api/v1/shares/{token}/target.
//
// Errors: ErrShareNotFound (documented 404), ErrShareLookupUnsupported (route
// missing), ErrUnauthorized (401), ErrShareLookupUnavailable (transport,
// timeout, 5xx), ErrVersionUnsupported (426); anything else is a plain error.
// Redirects are never followed: the credential goes to this client's endpoint
// and nowhere else.
func (c *RepoClient) GetShareTarget(ctx context.Context, token string) (*ShareTarget, error) {
	// The token lands in the request path. Callers validate its grammar
	// strictly; this is the backstop against dot-segments and separators a
	// normalizing proxy could rewrite into a different route.
	if token == "" || token == "." || token == ".." || strings.ContainsAny(token, "/\\?#%") {
		return nil, fmt.Errorf("invalid share token")
	}

	reqURL := strings.TrimSuffix(c.baseURL, "/") + fmt.Sprintf(shareTargetPath, url.PathEscape(token))
	// Log the route shape, never the token: a share token is a bearer
	// capability for whoever holds the link.
	logURL := strings.TrimSuffix(c.baseURL, "/") + fmt.Sprintf(shareTargetPath, "{token}")

	logger.LogHTTPRequest("GET", logURL)
	start := time.Now()

	httpReq, err := useragent.NewRequest(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if c.authToken != "" {
		httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.authToken))
	}

	hc := *c.httpClient
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	resp, err := hc.Do(httpReq)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("GET", logURL, err, duration)
		return nil, fmt.Errorf("%w: %w", ErrShareLookupUnavailable, err)
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse("GET", logURL, resp.StatusCode, duration)

	if CheckVersionResponse(resp) {
		return nil, ErrVersionUnsupported
	}

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxShareTargetBodyBytes+1))

	switch {
	case resp.StatusCode == http.StatusOK:
		if readErr != nil {
			return nil, fmt.Errorf("%w: read response: %w", ErrShareLookupUnavailable, readErr)
		}
		if int64(len(body)) > maxShareTargetBodyBytes {
			return nil, fmt.Errorf("share lookup response too large (exceeds %d bytes)", maxShareTargetBodyBytes)
		}
		var out ShareTarget
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, fmt.Errorf("decode share lookup response: %w", err)
		}
		return &out, nil
	case resp.StatusCode == http.StatusNotFound:
		if isShareNotFoundBody(body) {
			return nil, ErrShareNotFound
		}
		return nil, ErrShareLookupUnsupported
	case resp.StatusCode == http.StatusMethodNotAllowed, resp.StatusCode == http.StatusNotImplemented:
		return nil, ErrShareLookupUnsupported
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, ErrUnauthorized
	case resp.StatusCode >= 500:
		return nil, fmt.Errorf("%w: server returned %d", ErrShareLookupUnavailable, resp.StatusCode)
	default:
		return nil, fmt.Errorf("share lookup failed: HTTP %d", resp.StatusCode)
	}
}

// isShareNotFoundBody reports whether a 404 body is the lookup's documented
// denial rather than a generic not-found from a server without the route.
func isShareNotFoundBody(body []byte) bool {
	var e struct {
		Error string `json:"error"`
	}
	if len(body) == 0 || json.Unmarshal(body, &e) != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(e.Error), shareNotFoundMessage)
}
