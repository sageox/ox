package planid

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGeneratePlanID verifies prefix + UUID-shaped suffix.
// Failure prevented: drift in ID format would silently break server-side
// parsing of newly-generated IDs.
func TestGeneratePlanID(t *testing.T) {
	id := GeneratePlanID()

	assert.True(t, strings.HasPrefix(id, "pln_"), "expected plan ID to have 'pln_' prefix, got: %s", id)
	assert.Greater(t, len(id), len("pln_"), "expected plan ID to have content after prefix, got: %s", id)

	uuidPart := strings.TrimPrefix(id, "pln_")
	assert.Len(t, uuidPart, 36, "expected UUID part to be 36 chars: %s", uuidPart)
}

// TestGeneratePlanID_Uniqueness ensures the generator never repeats.
// Failure prevented: collision between two plans would silently merge them
// in any consumer keying on plan ID.
func TestGeneratePlanID_Uniqueness(t *testing.T) {
	ids := make(map[string]bool)
	const count = 1000

	for i := 0; i < count; i++ {
		id := GeneratePlanID()
		assert.False(t, ids[id], "duplicate plan ID generated: %s", id)
		ids[id] = true
	}

	assert.Len(t, ids, count)
}

// TestGeneratePlanID_TimeSortable verifies UUIDv7 ordering is preserved
// when IDs are compared as strings.
// Failure prevented: a downstream switch to UUIDv4 would silently break
// any "list plans newest first" optimization that relies on lexical sort.
func TestGeneratePlanID_TimeSortable(t *testing.T) {
	id1 := GeneratePlanID()
	id2 := GeneratePlanID()
	id3 := GeneratePlanID()

	assert.LessOrEqual(t, id1, id2, "expected first ID to be <= second ID for time sorting")
	assert.LessOrEqual(t, id2, id3, "expected second ID to be <= third ID for time sorting")
}

// TestParse_RoundTrip round-trips a generated ID back to a UUIDv7.
func TestParse_RoundTrip(t *testing.T) {
	original := GeneratePlanID()

	parsed, err := Parse(original)
	require.NoError(t, err, "failed to parse valid plan ID")

	assert.NotEqual(t, uuid.Nil, parsed, "expected non-nil UUID from parsed plan ID")
	assert.Equal(t, uuid.Version(7), parsed.Version(), "expected UUID version 7")

	reEncoded := planIDPrefix + parsed.String()
	assert.Equal(t, original, reEncoded, "round trip failed")
}

// TestParse_Table exercises the validation surface: a valid mint parses, the
// pln_ prefix is required, and sibling prefixes (ses_/repo_/oxsid_) or
// garbage are all rejected rather than silently accepted.
func TestParse_Table(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{"valid generated id", GeneratePlanID(), false},
		{"bare uuid missing prefix", "5e6238b7-9403-4ee4-b5ec-8a6d37a5de14", true},
		{"empty string", "", true},
		{"prefix only, no uuid", "pln_", true},
		{"wrong prefix ses_", "ses_5e6238b7-9403-4ee4-b5ec-8a6d37a5de14", true},
		{"wrong prefix repo_", "repo_5e6238b7-9403-4ee4-b5ec-8a6d37a5de14", true},
		{"wrong prefix oxsid_", "oxsid_01JEYQ9Z8X9Y2K3N4P5Q6R7S8T", true},
		{"no separator before uuid", "pln5e6238b7-9403-4ee4-b5ec-8a6d37a5de14", true},
		{"garbage after prefix", "pln_not-a-valid-uuid", true},
		{"symbols after prefix", "pln_!!!invalid", true},
		{"truncated uuid", "pln_5e6238b7-9403-4ee4-b5ec", true},
		{"whitespace uuid", "pln_ spaces ", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.id)
			if tt.wantErr {
				assert.Error(t, err, "id=%q", tt.id)
			} else {
				assert.NoError(t, err, "id=%q", tt.id)
			}
		})
	}
}

// TestIsPlanID_Table mirrors TestParse_Table but through the boolean
// validator, plus the non-canonical-encoding cases (uppercase, etc.) that
// Parse alone would accept but IsPlanID must reject via round-trip.
func TestIsPlanID_Table(t *testing.T) {
	valid := GeneratePlanID()
	bareUUID := strings.TrimPrefix(valid, planIDPrefix)

	tests := []struct {
		name string
		id   string
		want bool
	}{
		{"freshly generated id", valid, true},
		{"missing prefix", bareUUID, false},
		{"empty string", "", false},
		{"prefix only", "pln_", false},
		{"ses_ prefix rejected", "ses_" + bareUUID, false},
		{"repo_ prefix rejected", "repo_" + bareUUID, false},
		{"oxsid_ prefix rejected", "oxsid_01JEYQ9Z8X9Y2K3N4P5Q6R7S8T", false},
		{"garbage rejected", "pln_garbage", false},
		{"uppercase uuid rejected (non-canonical)", "pln_" + strings.ToUpper(bareUUID), false},
		{"braced uuid rejected (non-canonical)", "pln_{" + bareUUID + "}", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsPlanID(tt.id), "id=%q", tt.id)
		})
	}
}

// TestPrefix verifies the exported prefix matches the package constant.
// Failure prevented: a future refactor that changes one but not the other
// would split the canonical prefix across two values.
func TestPrefix(t *testing.T) {
	assert.Equal(t, "pln_", Prefix())
	assert.Equal(t, planIDPrefix, Prefix())
}

func BenchmarkGeneratePlanID(b *testing.B) {
	for i := 0; i < b.N; i++ {
		GeneratePlanID()
	}
}

func BenchmarkParsePlanID(b *testing.B) {
	id := GeneratePlanID()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = Parse(id)
	}
}
