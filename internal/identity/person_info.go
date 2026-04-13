package identity

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sageox/ox/internal/auth"
)

// PersonInfo holds a privacy-aware display identity for session rendering.
// DisplayName is safe for inclusion in shared ledgers (no full PII).
// Email is preserved for internal/audit use only.
type PersonInfo struct {
	DisplayName string // "port8080" or "FirstName L." — team-recognizable, public-safe
	Email       string // original email, preserved for internal/audit use
}

// NewPersonInfo creates a PersonInfo with privacy-aware display name derivation.
//
// Priority for DisplayName:
//  1. configDisplayName — explicit user setting from config.yaml (e.g., "port8080")
//  2. name — parsed into "FirstName L." format
//  3. email local part — split on delimiters, same format
//  4. gitUsername — split on delimiters, same format
//  5. All empty — "Anonymous"
func NewPersonInfo(email, name, gitUsername, configDisplayName string) *PersonInfo {
	displayName := strings.TrimSpace(configDisplayName)
	if displayName == "" {
		displayName = deriveDisplayName(email, name, gitUsername)
	}

	return &PersonInfo{
		DisplayName: displayName,
		Email:       email,
	}
}

// NewPersonInfoFromAuth creates a PersonInfo from auth.UserInfo and a config display name.
func NewPersonInfoFromAuth(info auth.UserInfo, configDisplayName string) *PersonInfo {
	return NewPersonInfo(info.Email, info.Name, "", configDisplayName)
}

// String returns the DisplayName.
func (p *PersonInfo) String() string {
	if p == nil {
		return "Anonymous"
	}
	return p.DisplayName
}

// deriveDisplayName auto-derives a privacy-aware name from available fields.
func deriveDisplayName(email, name, gitUsername string) string {
	if name != "" {
		if d := formatNameAsDisplay(name); d != "" {
			return d
		}
	}

	if email != "" {
		localPart := extractLocalPart(email)
		if localPart != "" {
			if d := formatIdentifierAsDisplay(localPart); d != "" {
				return d
			}
		}
	}

	if gitUsername != "" {
		if d := formatIdentifierAsDisplay(gitUsername); d != "" {
			return d
		}
	}

	return "Anonymous"
}

// formatNameAsDisplay converts a full name to "FirstName L." format.
// "Person A" -> "Person A."
// "Person" -> "Person"
func formatNameAsDisplay(name string) string {
	parts := strings.Fields(strings.TrimSpace(name))
	if len(parts) == 0 {
		return ""
	}

	first := capitalize(parts[0])
	if len(parts) == 1 {
		return first
	}

	// use first letter of last name as initial
	last := parts[len(parts)-1]
	r, _ := utf8.DecodeRuneInString(last)
	if r == utf8.RuneError {
		return first
	}

	return first + " " + string(unicode.ToUpper(r)) + "."
}

// formatIdentifierAsDisplay converts an identifier (email local part, username)
// to display format by splitting on delimiters and applying name formatting.
// "person.name" -> "Person N."
// "personn" -> "Personn"
func formatIdentifierAsDisplay(id string) string {
	parts := splitIdentifier(id)
	if len(parts) == 0 {
		return ""
	}

	// rejoin as a name and format
	name := strings.Join(parts, " ")
	return formatNameAsDisplay(name)
}

// extractLocalPart returns the part before @ in an email, or the whole string if no @.
func extractLocalPart(email string) string {
	if idx := strings.Index(email, "@"); idx >= 0 {
		return email[:idx]
	}
	return email
}

// splitIdentifier splits a string on common delimiters (., -, _).
func splitIdentifier(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == '.' || r == '-' || r == '_'
	})
}

// FirstNameFromSlug extracts and capitalizes the first name from a principal slug.
// "ryan-snodgrass" → "Ryan", "ryan" → "Ryan", "" → "".
func FirstNameFromSlug(slug string) string {
	parts := splitIdentifier(slug)
	if len(parts) == 0 {
		return ""
	}
	return capitalize(parts[0])
}

// capitalize uppercases the first rune, leaving the rest unchanged.
func capitalize(s string) string {
	if s == "" {
		return ""
	}
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}
