package gitutil

import "regexp"

// credentialPattern matches oauth2:TOKEN@ patterns in git output.
var credentialPattern = regexp.MustCompile(`oauth2:[^@]+@`)

// SanitizeOutput removes credentials from git command output.
// Replaces oauth2:TOKEN@ patterns with oauth2:***@ to prevent credential leaks in logs.
func SanitizeOutput(output string) string {
	return credentialPattern.ReplaceAllString(output, "oauth2:***@")
}
