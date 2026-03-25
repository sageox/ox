package gitutil

import (
	"regexp"
	"strings"
)

// credentialPattern matches oauth2:TOKEN@ patterns in git output.
var credentialPattern = regexp.MustCompile(`oauth2:[^@]+@`)

// keychainNoisePattern matches macOS Keychain errors that are harmless noise.
// e.g., "fatal: failed to store: -25300" (errSecItemNotFound)
var keychainNoisePattern = regexp.MustCompile(`(?m)^.*failed to store: -\d+.*\n?`)

// SanitizeOutput removes credentials and harmless noise from git command output.
func SanitizeOutput(output string) string {
	result := credentialPattern.ReplaceAllString(output, "oauth2:***@")
	result = keychainNoisePattern.ReplaceAllString(result, "")
	return strings.TrimSpace(result)
}
