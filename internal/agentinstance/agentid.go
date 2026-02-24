package agentinstance

import (
	"crypto/rand"
	"fmt"
	"strings"
)

// Design decision: Ox<4-char> agent_id format
// Rationale:
// - "Ox" prefix identifies agent context in output, distinguishes from other IDs
// - 6 chars total saves ~40 tokens vs full oxsid (32 chars) per command invocation
// - 62^4 = 14.7M combinations is collision-resistant within a single project
// - Short enough to type/copy while still being identifiable
// See: docs/plan/drifting-exploring-quill.md for full analysis

const (
	agentIDPrefix = "Ox"
	agentIDLength = 6
	suffixLength  = 4
	maxRetries    = 10
	charset       = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

// GenerateAgentID generates a new unique agent ID checking for collisions
// against existing IDs in the store.
//
// The ID format is Ox<4-char> where chars are [0-9A-Za-z].
// This provides 62^4 = 14,776,336 possible combinations.
//
// Examples:
//   - GenerateAgentID([]string{}) -> "OxA1b2"
//   - GenerateAgentID([]string{"OxA1b2"}) -> "OxC3d4" (different ID)
//   - GenerateAgentID([]string{"OxA1b2", ...}) -> error if can't find unique ID after 10 retries
func GenerateAgentID(existingIDs []string) (string, error) {
	existingSet := make(map[string]bool, len(existingIDs))
	for _, id := range existingIDs {
		existingSet[id] = true
	}

	for attempt := 0; attempt < maxRetries; attempt++ {
		suffix, err := generateRandomSuffix(suffixLength)
		if err != nil {
			return "", fmt.Errorf("failed to generate random suffix: %w", err)
		}

		agentID := agentIDPrefix + suffix

		if !existingSet[agentID] {
			return agentID, nil
		}
	}

	return "", fmt.Errorf("failed to generate unique agent ID after %d attempts", maxRetries)
}

// generateRandomSuffix creates a random string of the specified length
// using cryptographically secure randomness from the charset [0-9A-Za-z].
func generateRandomSuffix(length int) (string, error) {
	charsetLen := len(charset)
	result := make([]byte, length)

	// generate random bytes
	randomBytes := make([]byte, length)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}

	// map random bytes to charset
	for i := 0; i < length; i++ {
		result[i] = charset[int(randomBytes[i])%charsetLen]
	}

	return string(result), nil
}

// IsValidAgentID checks if a string is a valid agent ID format.
//
// Valid format requirements:
//   - Must start with "Ox"
//   - Must be exactly 6 characters total
//   - Suffix (4 chars) must be alphanumeric [0-9A-Za-z]
//
// Examples:
//   - IsValidAgentID("OxA1b2") -> true
//   - IsValidAgentID("Ox1234") -> true
//   - IsValidAgentID("OxZzZz") -> true
//   - IsValidAgentID("ox1234") -> false (wrong prefix)
//   - IsValidAgentID("OX1234") -> false (wrong prefix)
//   - IsValidAgentID("Ox123") -> false (too short)
//   - IsValidAgentID("Ox12345") -> false (too long)
//   - IsValidAgentID("Ox12#4") -> false (invalid character)
//   - IsValidAgentID("") -> false
//   - IsValidAgentID("random") -> false
func IsValidAgentID(id string) bool {
	if len(id) != agentIDLength {
		return false
	}

	if !strings.HasPrefix(id, agentIDPrefix) {
		return false
	}

	suffix := id[len(agentIDPrefix):]
	for _, ch := range suffix {
		if !isAlphanumeric(ch) {
			return false
		}
	}

	return true
}

// isAlphanumeric checks if a rune is in the valid charset [0-9A-Za-z].
func isAlphanumeric(ch rune) bool {
	return (ch >= '0' && ch <= '9') ||
		(ch >= 'A' && ch <= 'Z') ||
		(ch >= 'a' && ch <= 'z')
}

// ClassifyBadID returns a diagnostic message explaining why id is not a valid
// agent ID. Returns empty string if id is valid or doesn't match a known
// wrong-format pattern (caller should use generic error).
func ClassifyBadID(id string) string {
	if IsValidAgentID(id) {
		return ""
	}
	if looksLikeUUID(id) {
		return fmt.Sprintf("%q looks like a UUID, not an ox agent ID\nRun 'ox agent prime' to get your Ox<4-char> agent ID", id)
	}
	if strings.HasPrefix(id, "oxsid_") {
		return fmt.Sprintf("%q is a server session ID, not an agent ID\nRun 'ox agent prime' to get your Ox<4-char> agent ID", id)
	}
	if len(id) >= 2 && strings.EqualFold(id[:2], "ox") {
		return fmt.Sprintf("invalid agent ID format %q — expected Ox<4-char> (e.g., OxA1b2)\nRun 'ox agent prime' to get your agent ID", id)
	}
	return ""
}

// looksLikeUUID checks for standard 8-4-4-4-12 UUID structure.
func looksLikeUUID(id string) bool {
	if len(id) != 36 {
		return false
	}
	return id[8] == '-' && id[13] == '-' && id[18] == '-' && id[23] == '-'
}

// ParseAgentID extracts the 4-char suffix from an agent ID.
// Returns error if format is invalid.
//
// Examples:
//   - ParseAgentID("OxA1b2") -> "A1b2", nil
//   - ParseAgentID("Ox1234") -> "1234", nil
//   - ParseAgentID("ox1234") -> "", error (invalid prefix)
//   - ParseAgentID("Ox123") -> "", error (wrong length)
//   - ParseAgentID("") -> "", error (invalid format)
func ParseAgentID(id string) (string, error) {
	if !IsValidAgentID(id) {
		return "", fmt.Errorf("invalid agent ID format: %q (expected format: Ox<4-char>)", id)
	}

	suffix := id[len(agentIDPrefix):]
	return suffix, nil
}
