package auth

import (
	"encoding/json"
	"fmt"
)

// redactedBody returns a log-safe representation of an HTTP response body
// from an auth-flow endpoint (device-code, token, JWT exchange, user-info).
//
// Authentication endpoints can echo back credential-bearing fields in error
// payloads — refresh_token, session_token, partial JWTs, server-internal IDs.
// Several call sites pass these bodies through `string(body)` into either
// returned error messages (which propagate to user-visible CLI output and
// support-bundle logs) or `slog.Debug("...", "body", ...)` (which lands in
// verbose logs that users frequently share when troubleshooting).
//
// The compromise: parse the body as JSON and emit ONLY a small allowlist of
// fields that legitimate OAuth/auth servers use for error reporting. If the
// body isn't JSON, fall back to a length+shape summary that never contains
// credential bytes. Either way the caller still gets actionable debug info
// (the error code, the error description, the body size) without exposing
// any token material that may have been included by a misbehaving server.
//
// The allowlist matches RFC 6749 §5.2 error-response field names plus the
// common SageOx envelope fields we use elsewhere. Tokens, JWT payloads, and
// server-internal session IDs are not in the allowlist by design.
func redactedBody(body []byte, contentType string) string {
	if len(body) == 0 {
		return "(empty body)"
	}
	// Try to extract just the error fields. We don't trust the server to omit
	// credential material from an error envelope, so allowlist explicitly.
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err == nil {
		safe := map[string]any{}
		for _, k := range []string{"error", "error_description", "error_uri", "code", "message", "status"} {
			if v, ok := parsed[k]; ok {
				safe[k] = v
			}
		}
		// Some servers wrap errors in {"data": {...}} or {"error": {...}} —
		// surface that we saw it without descending into the value.
		if _, ok := parsed["data"]; ok {
			safe["_envelope"] = "data"
		}
		if len(safe) > 0 {
			b, mErr := json.Marshal(safe)
			if mErr == nil {
				return string(b)
			}
		}
		return fmt.Sprintf("(json body, %d bytes, no allowlisted error fields)", len(body))
	}
	// Non-JSON: report shape only.
	return fmt.Sprintf("(non-json body, %d bytes, content-type=%q)", len(body), contentType)
}
