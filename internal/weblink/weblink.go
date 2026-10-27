// Package weblink builds sageox.ai web links for ox artifacts — conversations
// (/c/<id>) and plans (/plan/<pln_id>). Every builder returns "" when it
// cannot produce a trustworthy link: a missing endpoint, an id of the wrong
// type, or an id that is not an opaque artifact id. Callers render nothing
// rather than a guessed link that would 404.
package weblink

import (
	"net/url"
	"slices"
	"strings"
)

// sageoxHost is the host family an already-absolute conversation link must
// belong to: sageox.ai itself or a subdomain of it (test.sageox.ai, ...).
const sageoxHost = "sageox.ai"

// conversationPrefixes are the id types the /c/ route resolves.
var conversationPrefixes = []string{"cnv_", "rec_", "ses_"}

// ArtifactURL maps a flag value — either a full http(s) URL or a bare id — to a
// web URL. A full URL passes through; a bare id becomes {ep}{pathPrefix}{id},
// tolerating a missing type prefix so both "pln_abc" and "abc" resolve. A bare
// id that names a DIFFERENT type (e.g. a "ses_" id passed to a --plan slot) is
// rejected rather than resolved into a plausible-but-wrong 404 URL.
func ArtifactURL(ep, raw, pathPrefix, idPrefix string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	if p := IDTypePrefix(raw); p != "" && p != idPrefix {
		return ""
	}
	if ep == "" {
		return ""
	}
	return ep + pathPrefix + url.PathEscape(raw)
}

// IDTypePrefix returns the leading "<type>_" token of a bare id (e.g. "pln_" for
// "pln_4d8e2f"), or "" when the id carries no such prefix ("4d8e2f"). Only a
// lowercase-letter run followed by an underscore counts, so an id whose body
// merely contains underscores is treated as prefix-less.
func IDTypePrefix(raw string) string {
	i := strings.IndexByte(raw, '_')
	if i <= 0 {
		return ""
	}
	for _, r := range raw[:i] {
		if r < 'a' || r > 'z' {
			return ""
		}
	}
	return raw[:i+1]
}

// Conversation returns the /c/ link for a cnv_, rec_ or ses_ id, or passes an
// already-absolute sageox.ai /c/ link through. "" for anything else — a
// prefix-less id, a slug, a path, a foreign host, or no endpoint.
func Conversation(endpoint, id string) string {
	id = strings.TrimSpace(id)
	if isAbsolute(id) {
		if isSageoxLink(id, "/c/") {
			return id
		}
		return ""
	}
	p := IDTypePrefix(id)
	if !slices.Contains(conversationPrefixes, p) || !isOpaqueID(id[len(p):]) {
		return ""
	}
	return ArtifactURL(cleanEndpoint(endpoint), id, "/c/", p)
}

// Plan returns the /plan/ link for a pln_ id. "" for any other id shape or
// when the endpoint is unknown.
func Plan(endpoint, id string) string {
	id = strings.TrimSpace(id)
	if IDTypePrefix(id) != "pln_" || !isOpaqueID(id[len("pln_"):]) {
		return ""
	}
	return ArtifactURL(cleanEndpoint(endpoint), id, "/plan/", "pln_")
}

// cleanEndpoint accepts only an http(s) endpoint with no whitespace or
// control characters, trimmed of a trailing slash; anything else yields "".
func cleanEndpoint(ep string) string {
	ep = strings.TrimRight(strings.TrimSpace(ep), "/")
	if !isAbsolute(ep) || strings.ContainsFunc(ep, isUnsafe) {
		return ""
	}
	return ep
}

func isAbsolute(s string) bool {
	return strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")
}

// isSageoxLink reports whether raw is an http(s) link on the sageox.ai host
// family whose path starts with pathPrefix and names something after it.
func isSageoxLink(raw, pathPrefix string) bool {
	if strings.ContainsFunc(raw, isUnsafe) {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host != sageoxHost && !strings.HasSuffix(host, "."+sageoxHost) {
		return false
	}
	return strings.HasPrefix(u.Path, pathPrefix) && len(u.Path) > len(pathPrefix)
}

// isOpaqueID reports whether body is a non-empty run of id characters
// (letters, digits, '-'), the shape every minted artifact id body has.
func isOpaqueID(body string) bool {
	if body == "" {
		return false
	}
	for _, r := range body {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}

func isUnsafe(r rune) bool { return r <= ' ' || r == 0x7f }
