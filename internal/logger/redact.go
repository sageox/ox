package logger

import (
	"fmt"
	"log/slog"
	"regexp"
)

// redactSecrets scrubs credential material from a string before it reaches a
// log sink. It is the last-line backstop wired into every handler through
// ReplaceAttr (see Init), so no call site can leak a raw secret by logging an
// arbitrary string value — most importantly the full HTTP response bodies that
// carry the git PAT (ReposResponse.token / TeamInfo git_token) and repo_salt.
//
// It deliberately overlaps a little with internal/auth.redactedBody (auth JSON
// error-body allowlist) and internal/gitutil.SanitizeOutput (git subprocess
// output). Those redact at their own layers; this guards the slog sink itself.
// logger imports only the stdlib, so it can sit under every other package
// without an import cycle — which is exactly why the backstop lives here.
//
// Matched shapes:
//   - JSON string field with a sensitive key: "token":"…", "git_token":"…"
//   - Authorization: Bearer <token> and a bare "Bearer <token>"
//   - credentials in a URL userinfo: https://user:secret@host
//   - well-known PAT prefixes (SageOx ox[a-z]_ family — personal oxp_, team
//     oxt_, and any future family; GitLab glpat-, GitHub gh*_)
func redactSecrets(s string) string {
	if s == "" {
		return s
	}
	s = jsonSecretRe.ReplaceAllString(s, `${1}[REDACTED]${2}`)
	s = bearerRe.ReplaceAllString(s, `${1}[REDACTED]`)
	s = urlCredRe.ReplaceAllString(s, `${1}:[REDACTED]@`)
	s = patRe.ReplaceAllString(s, `[REDACTED]`)
	return s
}

var (
	// "sensitive_key": "value" — case-insensitive key, string values only.
	// Longer keys precede their substrings so the alternation matches the
	// specific field (refresh_token) rather than the generic one (token).
	jsonSecretRe = regexp.MustCompile(`(?i)("(?:access_token|refresh_token|session_token|git_token|token|password|passwd|authorization|repo_salt|client_secret|secret|api[_-]?key)"\s*:\s*")[^"]*(")`)
	// Authorization: Bearer xxxxx  /  "Bearer xxxxx"
	bearerRe = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+`)
	// scheme://user:secret@host — keep the username, redact the secret.
	urlCredRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^:/?#\s@]+):[^@/?#\s]+@`)
	// well-known personal-access-token prefixes (bare token strings).
	// ox[a-z]_ covers the whole SageOx bearer-token family generically
	// (oxp_ personal, oxt_ team — and any family minted later) instead of
	// hand-listing each one; a team token has a LARGER blast radius than a
	// PAT, so a new family must be redacted by default, not opt-in.
	//
	// The leading \b is load-bearing: without it the generalized ox[a-z]_
	// alternative matches INSIDE ordinary words, most damagingly "proxy_" —
	// so "failed to dial via proxy_endpoint_override" logged as
	// "failed to dial via pr[REDACTED]". That is the exact vocabulary of
	// network-failure lines, the ones an operator most needs intact. A real
	// token is always preceded by a non-word character (line start, space,
	// '=', quote, ':', '/'), so the boundary costs no true-positive coverage.
	patRe = regexp.MustCompile(`\b(?:ox[a-z]_|glpat-|ghp_|gho_|ghu_|ghs_|github_pat_)[A-Za-z0-9_-]{8,}`)
)

// RedactSecrets scrubs credential material from an arbitrary string. Exported for call
// sites that must sanitize a value which is NOT a log attribute — notably an error
// returned to the caller and printed to the terminal, which the slog ReplaceAttr hook
// never sees.
func RedactSecrets(s string) string { return redactSecrets(s) }

// RedactAttr is the slog ReplaceAttr hook: scrub every string-valued attribute
// (body, response, url, …) through redactSecrets, and every error value, which
// slog prints as its message — `"error", err` is the commonest way ox logs a
// failure. Other attrs pass through untouched. Exported for handlers built
// outside this package, such as the daemon's (newDaemonLogger in cmd/ox).
func RedactAttr(_ []string, a slog.Attr) slog.Attr {
	switch a.Value.Kind() {
	case slog.KindString:
		a.Value = slog.StringValue(redactSecrets(a.Value.String()))
	case slog.KindAny:
		if err, ok := a.Value.Any().(error); ok {
			// fmt, not err.Error(): slog does not recover panics in this hook, and
			// fmt prints a typed-nil error as <nil>, as slog did before this hook
			a.Value = slog.StringValue(redactSecrets(fmt.Sprint(err)))
		}
	}
	return a
}
