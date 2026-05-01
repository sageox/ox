package sessionid

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGenerateSessionID verifies prefix + UUID-shaped suffix.
// Failure prevented: drift in ID format would silently break server-side
// parsing of newly-generated IDs.
func TestGenerateSessionID(t *testing.T) {
	id := GenerateSessionID()

	assert.True(t, strings.HasPrefix(id, "ses_"), "expected session ID to have 'ses_' prefix, got: %s", id)
	assert.Greater(t, len(id), len("ses_"), "expected session ID to have content after prefix, got: %s", id)

	uuidPart := strings.TrimPrefix(id, "ses_")
	assert.Len(t, uuidPart, 36, "expected UUID part to be 36 chars: %s", uuidPart)
}

// TestGenerateSessionID_Uniqueness ensures the generator never repeats.
// Failure prevented: collision between two recordings would silently
// merge them in any consumer keying on session ID.
func TestGenerateSessionID_Uniqueness(t *testing.T) {
	ids := make(map[string]bool)
	const count = 1000

	for i := 0; i < count; i++ {
		id := GenerateSessionID()
		assert.False(t, ids[id], "duplicate session ID generated: %s", id)
		ids[id] = true
	}

	assert.Len(t, ids, count)
}

// TestGenerateSessionID_TimeSortable verifies UUIDv7 ordering is preserved
// when IDs are compared as strings.
// Failure prevented: a downstream switch to UUIDv4 would silently break
// any "list sessions newest first" optimization that relies on lexical sort.
func TestGenerateSessionID_TimeSortable(t *testing.T) {
	id1 := GenerateSessionID()
	id2 := GenerateSessionID()
	id3 := GenerateSessionID()

	assert.LessOrEqual(t, id1, id2, "expected first ID to be <= second ID for time sorting")
	assert.LessOrEqual(t, id2, id3, "expected second ID to be <= third ID for time sorting")
}

// TestGenerateSessionID_StringSortMatchesTimeSort confirms a sequence of IDs
// generated in order remains lexically ordered.
// Failure prevented: any tool listing meta.json files in directory order
// expecting chronological output would silently misorder.
func TestGenerateSessionID_StringSortMatchesTimeSort(t *testing.T) {
	ids := make([]string, 100)
	for i := range ids {
		ids[i] = GenerateSessionID()
	}

	for i := 1; i < len(ids); i++ {
		assert.LessOrEqual(t, ids[i-1], ids[i],
			"IDs not in chronological string-sort order at index %d: %s > %s", i, ids[i-1], ids[i])
	}
}

// TestParseSessionID_Valid round-trips a generated ID back to a UUIDv7.
func TestParseSessionID_Valid(t *testing.T) {
	id := GenerateSessionID()

	parsedUUID, err := ParseSessionID(id)
	require.NoError(t, err, "failed to parse valid session ID")

	assert.NotEqual(t, uuid.Nil, parsedUUID, "expected non-nil UUID from parsed session ID")
	assert.Equal(t, uuid.Version(7), parsedUUID.Version(), "expected UUID version 7")
}

func TestParseSessionID_RoundTrip(t *testing.T) {
	originalID := GenerateSessionID()

	parsedUUID, err := ParseSessionID(originalID)
	require.NoError(t, err, "failed to parse session ID")

	reEncodedID := sessionIDPrefix + parsedUUID.String()
	assert.Equal(t, originalID, reEncodedID, "round trip failed")
}

func TestParseSessionID_InvalidPrefix(t *testing.T) {
	invalidIDs := []string{
		"invalid_5e6238b7-9403-4ee4-b5ec-8a6d37a5de14",
		"5e6238b7-9403-4ee4-b5ec-8a6d37a5de14",
		"ses5e6238b7-9403-4ee4-b5ec-8a6d37a5de14",
		"",
		"ses_",
		// repo_-prefixed IDs and oxsid_-prefixed IDs are common confusion
		// vectors; validate parser rejects them as not-a-session-ID.
		"repo_5e6238b7-9403-4ee4-b5ec-8a6d37a5de14",
		"oxsid_01JEYQ9Z8X9Y2K3N4P5Q6R7S8T",
	}

	for _, id := range invalidIDs {
		_, err := ParseSessionID(id)
		assert.Error(t, err, "expected error for invalid session ID: %s", id)
	}
}

func TestParseSessionID_InvalidUUID(t *testing.T) {
	invalidIDs := []string{
		"ses_not-a-valid-uuid",
		"ses_!!!invalid",
		"ses_@#$%",
		"ses_ spaces ",
		"ses_5e6238b7-9403-4ee4-b5ec", // truncated
	}

	for _, id := range invalidIDs {
		_, err := ParseSessionID(id)
		assert.Error(t, err, "expected error for invalid UUID format: %s", id)
	}
}

func TestIsValidSessionID_Valid(t *testing.T) {
	id := GenerateSessionID()
	assert.True(t, IsValidSessionID(id), "expected valid session ID to return true: %s", id)
}

func TestIsValidSessionID_Invalid(t *testing.T) {
	invalidIDs := []string{
		"invalid_5e6238b7-9403-4ee4-b5ec-8a6d37a5de14",
		"5e6238b7-9403-4ee4-b5ec-8a6d37a5de14",
		"",
		"ses_",
		"ses_!!!invalid",
		"ses_not-a-uuid",
		"repo_5e6238b7-9403-4ee4-b5ec-8a6d37a5de14",
		"oxsid_01JEYQ9Z8X9Y2K3N4P5Q6R7S8T",
	}

	for _, id := range invalidIDs {
		assert.False(t, IsValidSessionID(id), "expected invalid session ID to return false: %s", id)
	}
}

// TestPrefix verifies the exported prefix matches the package constant.
// Failure prevented: a future refactor that changes one but not the other
// would split the canonical prefix across two values.
func TestPrefix(t *testing.T) {
	assert.Equal(t, "ses_", Prefix())
	assert.Equal(t, sessionIDPrefix, Prefix())
}

func BenchmarkGenerateSessionID(b *testing.B) {
	for i := 0; i < b.N; i++ {
		GenerateSessionID()
	}
}

func BenchmarkParseSessionID(b *testing.B) {
	id := GenerateSessionID()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = ParseSessionID(id)
	}
}

func BenchmarkIsValidSessionID(b *testing.B) {
	id := GenerateSessionID()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		IsValidSessionID(id)
	}
}
