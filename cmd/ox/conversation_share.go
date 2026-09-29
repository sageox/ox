package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/conversation/read"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/spf13/cobra"
)

// Share links (https://<host>.sageox.ai/s/{token}) carry an opaque token, so
// resolving one to a recording takes one authenticated API call. That step
// lives here, in the command layer, because internal/conversation/read is
// pure and offline by contract ("never pulls, works logged out").
//
// Trust rule: the link only ever contributes its token and its host. The
// token is sent to an endpoint the user is already logged in to whose host
// equals the link's host — never to a host taken from the link alone — so a
// crafted link cannot make ox hand a credential to anyone new.

// shareLookupTimeout bounds the one network call a share link costs. A
// variable so tests can exercise the timeout path quickly.
var shareLookupTimeout = 5 * time.Second

// shareLookupBaseURL maps the logged-in endpoint the lookup is authorized for
// to the base URL the request is sent to. Identity in production; tests point
// it at an httptest server while the endpoint and its stored credential stay
// real.
var shareLookupBaseURL = func(ep string) string { return ep }

// shareRecordingEntity is the ShareTarget.EntityType of a recorded discussion.
const shareRecordingEntity = "recording"

// resolveConversationIDArg returns raw unchanged unless it is a share link,
// in which case it resolves the link online and returns an equivalent
// recording short link on the same host (so the reader's environment-mismatch
// hint still applies). Every failure is a typed error for the envelope.
func resolveConversationIDArg(ctx context.Context, raw string) (string, *read.Error) {
	host, token, ok := read.ParseShareLink(raw)
	if !ok {
		// Not a share link (or a malformed one): ParseID owns the verdict.
		return raw, nil
	}

	ep := loggedInEndpointForHost(host)
	if ep == "" {
		return "", shareUnresolvable(raw, fmt.Sprintf("you are not logged in to %s, so the share link cannot be looked up; run `ox login` for that environment and retry", host))
	}
	stored, err := auth.EnsureValidTokenForEndpoint(ep, 300)
	if err != nil || stored == nil || stored.AccessToken == "" {
		return "", shareUnresolvable(raw, fmt.Sprintf("your login for %s is missing or expired, so the share link cannot be looked up; run `ox login` and retry", host))
	}

	ctx, cancel := context.WithTimeout(ctx, shareLookupTimeout)
	defer cancel()
	client := api.NewRepoClientWithEndpoint(shareLookupBaseURL(ep)).
		WithAuthToken(stored.AccessToken).
		WithTimeout(shareLookupTimeout)
	target, err := client.GetShareTarget(ctx, token)
	if err != nil {
		return "", shareUnresolvable(raw, shareLookupFailureReason(host, err))
	}

	if target.EntityType != shareRecordingEntity {
		return "", &read.Error{
			Code: read.ErrCodeShareLinkNotDiscussion,
			Message: fmt.Sprintf("%q is a share link for a %s, not a discussion",
				truncateShareArg(raw), cli.SanitizeTerminalText(truncateShareArg(target.EntityType))),
		}
	}
	// The server's answer is untrusted input too: it must be a bare rec_/cnv_
	// id (never a URI or a link, which ParseID would also accept) and pass
	// the same strict validation as a pasted one.
	entityID := target.EntityID
	if !strings.HasPrefix(entityID, "rec_") && !strings.HasPrefix(entityID, "cnv_") {
		return "", shareUnresolvable(raw, "the server returned a recording id ox does not recognize")
	}
	id, idErr := read.ParseID(entityID)
	if idErr != nil {
		return "", shareUnresolvable(raw, "the server returned a recording id ox does not recognize")
	}
	return "https://" + host + "/c/" + id.RecordingID, nil
}

// conversationContext returns the command's context, or Background when the
// command was executed without one.
func conversationContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

// loggedInEndpointForHost returns the logged-in endpoint whose normalized
// host equals host, or "" when the user holds no credential for it.
func loggedInEndpointForHost(host string) string {
	for _, ep := range auth.GetLoggedInEndpoints() {
		if endpoint.NormalizeSlug(ep) == host {
			return ep
		}
	}
	return ""
}

// shareLookupFailureReason turns a lookup error into the user-facing reason.
func shareLookupFailureReason(host string, err error) string {
	var netErr net.Error
	switch {
	case errors.Is(err, api.ErrShareNotFound):
		return "the share was not found — it may be revoked or expired, or not shared with your team"
	case errors.Is(err, api.ErrShareLookupUnsupported):
		return fmt.Sprintf("%s doesn't support share lookups yet", host)
	case errors.Is(err, api.ErrUnauthorized):
		return fmt.Sprintf("%s rejected your login; run `ox login` and retry", host)
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return fmt.Sprintf("the share lookup on %s timed out", host)
	case errors.Is(err, api.ErrShareLookupUnavailable):
		return fmt.Sprintf("the share lookup on %s could not be reached", host)
	default:
		return fmt.Sprintf("the share lookup on %s failed", host)
	}
}

// shareUnresolvable builds the share_link_unresolvable error with the reason
// and the always-available fallback.
func shareUnresolvable(raw, reason string) *read.Error {
	return &read.Error{
		Code: read.ErrCodeShareLinkUnresolvable,
		Message: fmt.Sprintf("%q is a share link ox could not resolve: %s. Open it in a browser and paste the recording page URL (…/recordings/rec_…) or the rec_ id instead",
			truncateShareArg(raw), reason),
	}
}

// truncateShareArg bounds untrusted input reproduced in error messages.
func truncateShareArg(s string) string {
	const max = 80
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
