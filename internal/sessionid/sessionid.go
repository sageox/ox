// Package sessionid generates and validates session-recording identifiers.
//
// Format: "ses_" + standard UUIDv7 string (time-sortable; matches the
// repo_<UUIDv7> convention in internal/repotools).
//
// # Relationship to other ID types
//
//   - ses_<UUIDv7>  — THIS package. One specific recording. Unique per
//     recording, populated at recording creation, never regenerated.
//   - oxsid_<ULID>  — internal/auth. Server session bound to one
//     "ox agent prime" call (24h lifetime). Reused across every
//     recording produced during that window. NOT a recording identifier.
//   - Ox<4-char>    — internal/agentinstance. Agent identifier within
//     a project. Stable across all of that agent's recordings. NOT a
//     recording identifier.
//   - repo_<UUIDv7> — internal/repotools. Repository identifier.
//
// All four are ID-like and easy to confuse. ses_ is the only one that
// answers the question "which specific recording is this?".
package sessionid

import (
	"errors"
	"strings"

	"github.com/google/uuid"
)

const sessionIDPrefix = "ses_"

// GenerateSessionID generates a new prefixed session ID using UUIDv7.
// Format: "ses_" + standard UUIDv7 string.
//
// UUIDv7 is time-sortable; the standard format preserves this property
// when sorted lexicographically.
func GenerateSessionID() string {
	uuidV7 := uuid.Must(uuid.NewV7())
	return sessionIDPrefix + uuidV7.String()
}

// ParseSessionID parses a session ID back to its underlying UUID.
// Returns an error if the ID is invalid or doesn't have the correct prefix.
func ParseSessionID(id string) (uuid.UUID, error) {
	if !strings.HasPrefix(id, sessionIDPrefix) {
		return uuid.Nil, errors.New("invalid session ID: missing 'ses_' prefix")
	}

	uuidStr := strings.TrimPrefix(id, sessionIDPrefix)
	if len(uuidStr) == 0 {
		return uuid.Nil, errors.New("invalid session ID: empty UUID portion")
	}

	return uuid.Parse(uuidStr)
}

// IsValidSessionID validates the format of a session ID.
// Returns true if the ID has the correct prefix and can be parsed as a valid UUID.
func IsValidSessionID(id string) bool {
	_, err := ParseSessionID(id)
	return err == nil
}

// Prefix returns the canonical "ses_" prefix used for session IDs.
// Exposed so callers building synthetic IDs (e.g., the legacy fallback
// in lfs.SessionMeta.EffectiveSessionID) can reuse the same constant
// without redeclaring it.
func Prefix() string {
	return sessionIDPrefix
}
