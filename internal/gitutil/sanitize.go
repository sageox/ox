package gitutil

import (
	"regexp"
	"strings"
)

// credentialPattern matches oauth2:TOKEN@ patterns in git output.
var credentialPattern = regexp.MustCompile(`oauth2:[^@]+@`)

// bearerPattern matches "Bearer <token>" or "bearer <token>" in headers/logs.
var bearerPattern = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9\-._~+/]+=*`)

// xAccessTokenPattern matches x-access-token:TOKEN@ in git URLs (GitHub Apps).
var xAccessTokenPattern = regexp.MustCompile(`x-access-token:[^@]+@`)

// genericUserPassPattern matches https://user:pass@host (non-oauth2 form).
var genericUserPassPattern = regexp.MustCompile(`://[^/:@\s]+:[^/@\s]+@`)

// keychainNoisePattern matches macOS Keychain errors that are harmless noise.
var keychainNoisePattern = regexp.MustCompile(`(?m)^.*failed to store: -\d+.*\n?`)

// SanitizeOutput removes credentials and harmless noise from git command output.
func SanitizeOutput(output string) string {
	result := credentialPattern.ReplaceAllString(output, "oauth2:***@")
	result = bearerPattern.ReplaceAllString(result, "Bearer ***")
	result = xAccessTokenPattern.ReplaceAllString(result, "x-access-token:***@")
	// skip URLs already sanitized by specific patterns above
	result = genericUserPassPattern.ReplaceAllStringFunc(result, func(match string) string {
		if strings.Contains(match, ":***@") {
			return match
		}
		return "://***:***@"
	})
	result = keychainNoisePattern.ReplaceAllString(result, "")
	return strings.TrimSpace(result)
}
