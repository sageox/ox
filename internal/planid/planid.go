// Package planid generates and validates plan identifiers.
//
// Format: "pln_" + standard UUIDv7 string (time-sortable; matches the
// ses_<UUIDv7> convention in internal/sessionid and repo_<UUIDv7> in
// internal/repotools).
//
// # Relationship to other ID types
//
// See internal/sessionid's package doc for the full roster of prefixed IDs
// in ox. pln_<UUIDv7> — THIS package — answers "which specific saved plan is
// this?". Minted once per plan (internal/plan.Save's event-log foundation),
// never regenerated for that plan.
package planid

import (
	"errors"
	"strings"

	"github.com/google/uuid"
)

const planIDPrefix = "pln_"

// GeneratePlanID generates a new prefixed plan ID using UUIDv7.
// Format: "pln_" + standard UUIDv7 string.
//
// UUIDv7 is time-sortable; the standard format preserves this property
// when sorted lexicographically.
func GeneratePlanID() string {
	uuidV7 := uuid.Must(uuid.NewV7())
	return planIDPrefix + uuidV7.String()
}

// Parse parses a plan ID back to its underlying UUID.
// Returns an error if the ID is invalid or doesn't have the correct prefix.
func Parse(id string) (uuid.UUID, error) {
	if !strings.HasPrefix(id, planIDPrefix) {
		return uuid.Nil, errors.New("invalid plan ID: missing 'pln_' prefix")
	}

	uuidStr := strings.TrimPrefix(id, planIDPrefix)
	if len(uuidStr) == 0 {
		return uuid.Nil, errors.New("invalid plan ID: empty UUID portion")
	}

	return uuid.Parse(uuidStr)
}

// IsPlanID validates the format of a plan ID.
// Returns true only for the canonical encoding: "pln_" + lowercase
// hyphenated UUID. uuid.Parse also accepts braces, urn:uuid: prefixes,
// undashed hex, and uppercase — all of which would produce an id that
// doesn't byte-match the ID stored in events.jsonl/meta.json, so
// non-canonical encodings are rejected via a round-trip check.
func IsPlanID(id string) bool {
	parsed, err := Parse(id)
	if err != nil {
		return false
	}
	return planIDPrefix+parsed.String() == id
}

// Prefix returns the canonical "pln_" prefix used for plan IDs.
// Exposed so callers building or matching synthetic IDs can reuse the same
// constant without redeclaring it.
func Prefix() string {
	return planIDPrefix
}
