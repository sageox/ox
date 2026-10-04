package read

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/sageox/ox/internal/conversation/uri"
	"github.com/sageox/ox/internal/endpoint"
)

// Id prefixes (D16). cnv_ and rec_ share one UUID by literal prefix swap;
// INDEX.json keys by recording_id, so everything normalizes to rec_
// internally.
const (
	prefixConversation = "cnv_"
	prefixRecording    = "rec_"
	prefixTopic        = "tp_"
)

// ID is a validated, normalized conversation identifier.
type ID struct {
	// RecordingID is the rec_-prefixed form — the internal lookup key.
	RecordingID string
	// ConversationID is the cnv_-prefixed twin (same UUID).
	ConversationID string
	// Address is non-nil when the input was a sageox:// citation URI; its
	// selectors are carried through to the transcript query (D16).
	Address *uri.Address
	// LinkHost is the normalized sageox.ai host (e.g. "test.sageox.ai") when
	// the input was a pasted https link; empty for every other form. A later
	// not-found uses it to say the link came from a different environment
	// than the one this checkout syncs.
	LinkHost string
}

// sageoxHost is the only link host family ParseID accepts: sageox.ai itself
// or a subdomain of it (test.sageox.ai, ...), after endpoint normalization.
const sageoxHost = "sageox.ai"

// acceptedIDForms names every accepted input form, for error messages.
const acceptedIDForms = "cnv_<uuidv7>, rec_<uuidv7>, a sageox:// citation URI, or a sageox.ai recording link (https://sageox.ai/c/rec_…, …/team/<team>/media/recordings/rec_…, …/kb/<kb>/recordings/rec_…)"

// ParseID validates raw as one of the accepted id forms — cnv_<uuidv7>,
// rec_<uuidv7>, a full sageox:// citation URI, or a pasted sageox.ai
// recording link — and nothing else (D16: no folder names, no UUID
// prefixes). Ids arrive inside untrusted content, so validation is strict
// before any use: a link only contributes the path segment that names the
// recording, and that segment passes the same rec_/cnv_ validation as a bare
// id.
func ParseID(raw string) (*ID, *Error) {
	// Pasted input routinely carries a trailing newline or surrounding
	// spaces; no valid id form contains whitespace, so trimming is safe.
	raw = strings.TrimSpace(raw)
	switch {
	case hasLinkScheme(raw):
		return parseLink(raw)
	case strings.HasPrefix(raw, uri.Scheme):
		addr, err := uri.Parse(raw)
		if err != nil {
			return nil, newError(ErrCodeInvalidID, fmt.Sprintf("invalid citation URI: %v", err))
		}
		u := strings.TrimPrefix(addr.Conversation, prefixConversation)
		return &ID{
			RecordingID:    prefixRecording + u,
			ConversationID: addr.Conversation,
			Address:        addr,
		}, nil
	case strings.HasPrefix(raw, prefixConversation):
		u := raw[len(prefixConversation):]
		if !isUUIDv7(u) {
			return nil, newError(ErrCodeInvalidID, fmt.Sprintf("%q is not a valid cnv_ UUIDv7 id", truncateID(raw)))
		}
		return &ID{RecordingID: prefixRecording + u, ConversationID: raw}, nil
	case strings.HasPrefix(raw, prefixRecording):
		u := raw[len(prefixRecording):]
		if !isUUIDv7(u) {
			return nil, newError(ErrCodeInvalidID, fmt.Sprintf("%q is not a valid rec_ UUIDv7 id", truncateID(raw)))
		}
		return &ID{RecordingID: raw, ConversationID: prefixConversation + u}, nil
	default:
		return nil, newError(ErrCodeInvalidID,
			fmt.Sprintf("%q is not an accepted id; use %s", truncateID(raw), acceptedIDForms))
	}
}

// hasLinkScheme reports whether raw is spelled as an http(s) URL.
func hasLinkScheme(raw string) bool {
	lower := strings.ToLower(raw)
	return strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://")
}

// parseLink extracts the recording id from a pasted sageox.ai link. Only the
// host and path are consulted — query and fragment are ignored — and the
// extracted segment must itself be a strict rec_/cnv_ id. Accepted paths:
//
//	/c/{rec_|cnv_}                                 short link
//	/team/{team}/media/recordings/{rec_}[/...]     recording page (+ tabs)
//	/kb/{kb}/recordings/{rec_}[/...]               knowledge-base recording
//
// A /s/{token} share link is recognized but cannot be resolved locally: the
// token is opaque, so it gets its own typed code rather than invalid_id. The
// command layer may resolve one online first (see ParseShareLink); this
// package never touches the network.
func parseLink(raw string) (*ID, *Error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return nil, newError(ErrCodeInvalidID, fmt.Sprintf("%q is not a readable link; use %s", truncateID(raw), acceptedIDForms))
	}
	host := linkHost(u)
	if host != sageoxHost && !strings.HasSuffix(host, "."+sageoxHost) {
		return nil, newError(ErrCodeInvalidID,
			fmt.Sprintf("%q is not a sageox.ai link; use %s", truncateID(raw), acceptedIDForms))
	}

	segs := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })
	var candidate string
	switch {
	case len(segs) >= 1 && segs[0] == "s":
		if len(segs) != 2 || !isShareToken(segs[1]) {
			return nil, newError(ErrCodeInvalidID,
				fmt.Sprintf("%q is not a valid share link; use %s", truncateID(raw), acceptedIDForms))
		}
		return nil, newError(ErrCodeShareLinkUnresolvable,
			fmt.Sprintf("%q is a share link, which cannot be resolved offline; open it in a browser and paste the recording page URL (…/recordings/rec_…) or the rec_ id instead", truncateID(raw)))
	case len(segs) == 2 && segs[0] == "c":
		candidate = segs[1]
	case len(segs) >= 5 && segs[0] == "team" && segs[2] == "media" && segs[3] == "recordings":
		candidate = segs[4]
	case len(segs) >= 4 && segs[0] == "kb" && segs[2] == "recordings":
		candidate = segs[3]
	default:
		return nil, newError(ErrCodeInvalidID,
			fmt.Sprintf("%q is not a recording link; use %s", truncateID(raw), acceptedIDForms))
	}
	if !strings.HasPrefix(candidate, prefixRecording) && !strings.HasPrefix(candidate, prefixConversation) {
		return nil, newError(ErrCodeInvalidID,
			fmt.Sprintf("%q does not name a rec_ or cnv_ recording; use %s", truncateID(raw), acceptedIDForms))
	}
	id, idErr := ParseID(candidate)
	if idErr != nil {
		return nil, idErr
	}
	id.LinkHost = host
	return id, nil
}

// Share-token bounds. The token is opaque, so the grammar is deliberately
// narrow — URL-safe characters only, bounded length — which lets it be placed
// in a request path without any escaping ambiguity and rules out dot-segments,
// separators, and percent-encoding before a single byte leaves the machine.
const (
	minShareTokenLen = 4
	maxShareTokenLen = 128
)

// isShareToken reports whether tok is a syntactically valid share token:
// 4-128 characters from [A-Za-z0-9_-].
func isShareToken(tok string) bool {
	if len(tok) < minShareTokenLen || len(tok) > maxShareTokenLen {
		return false
	}
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// ParseShareLink reports whether raw is a sageox.ai share link
// (https://<host>/s/{token}) with a strictly valid token, returning the
// normalized link host and the token. It is pure and offline: resolving the
// token to a recording is a network step that belongs to the caller, which
// must only ever send it to an endpoint the user is logged in to — never to a
// host taken from the link alone. Query and fragment are ignored.
func ParseShareLink(raw string) (host, token string, ok bool) {
	raw = strings.TrimSpace(raw)
	if !hasLinkScheme(raw) {
		return "", "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", "", false
	}
	host = linkHost(u)
	if host != sageoxHost && !strings.HasSuffix(host, "."+sageoxHost) {
		return "", "", false
	}
	segs := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })
	if len(segs) != 2 || segs[0] != "s" || !isShareToken(segs[1]) {
		return "", "", false
	}
	return host, segs[1], true
}

// linkHost returns the link's host normalized the canonical way
// (endpoint.NormalizeSlug: lowercased, port dropped, api./www./app./git.
// stripped), so app.sageox.ai and sageox.ai compare equal.
func linkHost(u *url.URL) string {
	// A trailing dot is the fully-qualified spelling of the same host
	// (sageox.ai. == sageox.ai).
	h := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if h == "" {
		return ""
	}
	return endpoint.NormalizeSlug("https://" + h)
}

// ValidateTopicID checks a tp_<uuidv7> topic id (D21: exact full ids only —
// no title or ordinal matching).
func ValidateTopicID(raw string) *Error {
	if !strings.HasPrefix(raw, prefixTopic) || !isUUIDv7(raw[len(prefixTopic):]) {
		return newError(ErrCodeInvalidID,
			fmt.Sprintf("%q is not a valid tp_ UUIDv7 topic id; copy the exact id from ox conversation topics", truncateID(raw)))
	}
	return nil
}

// isUUIDv7 validates a canonical lowercase UUIDv7 by delegating to the uri
// package's strict parser (the single validator for untrusted ids): the
// candidate is wrapped as a bare conversation URI, and the round trip must
// yield exactly that conversation with no layer, revision, or selectors —
// so a candidate smuggling '/', '@', or '#' can never pass.
func isUUIDv7(candidate string) bool {
	addr, err := uri.Parse(uri.Scheme + prefixConversation + candidate)
	if err != nil {
		return false
	}
	return addr.Conversation == prefixConversation+candidate &&
		addr.Layer == "" && addr.Revision == 0 && addr.Selectors.IsZero()
}

// truncateID bounds untrusted input reproduced in error messages.
func truncateID(s string) string {
	const max = 80
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
