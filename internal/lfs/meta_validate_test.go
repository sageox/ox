package lfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateUserVisible_RejectsKnownLeakStrings is the schema-level
// guard from ox-4ggw. The session_finalize bug (ox-qqka) shipped because
// no test asserted the cross-layer invariant "user-visible fields never
// carry a validator/error string". This test encodes that invariant.
//
// Each entry below is a string that DID, at some point, land in
// meta.summary on a real ledger. If a future producer-side fix
// regresses, the writer's Validate() call rejects the leak before it
// reaches consumers.
//
// Failure prevented: any future writer (in this repo or a third-party
// tool that uses internal/lfs) that copies an internal error message
// into a user-visible field will get a hard error at write time.
func TestValidateUserVisible_RejectsKnownLeakStrings(t *testing.T) {
	leakyStrings := []string{
		"Summary failed content validation: title too short",
		"Summary failed richness validation: missing key_actions",
		"Summary generation failed: LLM output was not valid JSON",
		"5 user messages, 10 assistant responses",
		"42 user messages, 38 assistant responses",
	}

	t.Run("rejects when in Summary", func(t *testing.T) {
		for _, leak := range leakyStrings {
			t.Run(leak, func(t *testing.T) {
				meta := &SessionMeta{Summary: leak}
				err := ValidateUserVisible(meta)
				require.Error(t, err, "writer must reject Summary=%q", leak)
				assert.Contains(t, err.Error(), "user-visible fields must not")
			})
		}
	})

	t.Run("rejects when in Title", func(t *testing.T) {
		for _, leak := range leakyStrings {
			t.Run(leak, func(t *testing.T) {
				meta := &SessionMeta{Title: leak}
				err := ValidateUserVisible(meta)
				require.Error(t, err, "writer must reject Title=%q", leak)
			})
		}
	})

	t.Run("does not reject when in ValidationError (ops field)", func(t *testing.T) {
		// ValidationError is the LEGITIMATE home for these strings.
		// The whole point of ox-qqka is that the diagnostic goes here,
		// not into Title/Summary. Validate() must NEVER complain about
		// the ops field — that would defeat the structured-signal design.
		for _, leak := range leakyStrings {
			t.Run(leak, func(t *testing.T) {
				meta := &SessionMeta{ValidationError: leak}
				err := ValidateUserVisible(meta)
				assert.NoError(t, err, "ValidationError is the legitimate home; must not be rejected")
			})
		}
	})
}

// TestValidateUserVisible_AcceptsLegitimateContent: empty fields, real
// titles, real summaries — none are rejected. Without this positive
// control, a too-eager Validate() that rejects everything would still
// pass the negative tests.
func TestValidateUserVisible_AcceptsLegitimateContent(t *testing.T) {
	cases := []*SessionMeta{
		{},                                   // empty session, summary not yet generated
		{Title: "Fix lint issues in api-go"}, // real title
		{Title: "x", Summary: "Implemented OAuth2"},    // real summary
		{Summary: "Refactored the manifest pipeline."}, // legit prose that mentions "validation" in a normal way
		{Title: "validation passed"},                   // word "validation" alone is fine
	}
	for i, m := range cases {
		err := ValidateUserVisible(m)
		assert.NoError(t, err, "case %d (%+v) must pass validation", i, m)
	}
}

// TestValidateUserVisible_NilMeta: nil is reported as nil so callers
// can chain without a separate nil-check. (nil-meta rejection lives in
// WriteSessionMeta where the structural check happens first.)
func TestValidateUserVisible_NilMeta(t *testing.T) {
	assert.NoError(t, ValidateUserVisible(nil))
}

// TestWriteSessionMetaOnly_RejectsLeakyMeta is the cross-layer
// integration assertion: even if a producer-side fix regresses, the
// writer is the last line of defense. A meta with a leaky Summary
// MUST fail to persist.
//
// Failure prevented: a future caller (or third-party tool) bypasses
// the producer-side guard and copies an internal error into Summary.
// Pre-this-test, the leak would silently persist; post-this-test, the
// writer hard-errors and the bug is caught at write time.
func TestWriteSessionMetaOnly_RejectsLeakyMeta(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	meta := &SessionMeta{
		Version:     "1.0",
		SessionName: "session",
		AgentID:     "Ox",
		AgentType:   "claude-code",
		Summary:     "Summary failed content validation: title too short",
	}

	err := WriteSessionMetaOnly(sessionDir, meta)
	require.Error(t, err, "writer must refuse to persist a leaky meta.summary")
	assert.Contains(t, err.Error(), "invariant")

	// And the file must NOT have been written — atomic write means tmp
	// or the final file should both be absent.
	_, statErr := os.Stat(filepath.Join(sessionDir, "meta.json"))
	assert.True(t, os.IsNotExist(statErr), "meta.json must not exist after rejected write")
}

// TestIsLeakySummaryString_MatchesOnlyKnownShapes pins the public
// helper used by the retro-cleanup tool (ox-l4mj) and by the render-
// time mitigation in cmd/ox/session_list.go. The set of strings flagged
// must match exactly what the writer rejects — otherwise the cleanup
// tool would clear strings the writer accepts (or vice versa) and the
// system would oscillate between accepting and erasing the same value.
func TestIsLeakySummaryString_MatchesOnlyKnownShapes(t *testing.T) {
	leaky := []string{
		"Summary failed content validation",
		"Summary failed content validation: details",
		"Summary failed richness validation: missing actions",
		"Summary generation failed: bad json",
		"5 user messages, 10 assistant responses",
		"1 user message, 1 assistant response",
	}
	for _, s := range leaky {
		assert.True(t, IsLeakySummaryString(s), "should flag: %q", s)
	}

	notLeaky := []string{
		"",
		"Real session about validation logic",
		"Failed to bootstrap CI",         // common normal phrase, no leak prefix
		"Summary",                        // bare word
		"5 user messages were excellent", // not the stats-stub shape
		"Summary failure: this is fine",  // doesn't match exact prefix
	}
	for _, s := range notLeaky {
		assert.False(t, IsLeakySummaryString(s), "should NOT flag: %q", s)
	}
}

// TestValidateUserVisible_RejectsPaddedSentinels: a leaky string with
// leading/trailing whitespace must still be rejected. Pre-fix, the
// detector used `strings.HasPrefix` against the raw value, which a
// caller could trivially bypass by writing
// "  Summary failed content validation: x" — every render-time
// mitigation already trims, so the UI would hide the leak, but the
// on-disk meta.json would still carry the disguised diagnostic.
//
// Failure prevented: a future producer (or third-party tool) writes a
// whitespace-padded sentinel and the writer accepts it. Both the
// writer-side guard and the retro-cleanup tool now share the same
// trim-then-match invariant via leakySummaryReason.
func TestValidateUserVisible_RejectsPaddedSentinels(t *testing.T) {
	padded := []string{
		"  Summary failed content validation: title too short",   // leading spaces
		"\tSummary generation failed: bad json",                  // leading tab
		"Summary failed richness validation: missing actions  ",  // trailing spaces
		" \n Summary failed content validation: leading newline", // mixed whitespace
		"  5 user messages, 10 assistant responses",              // padded stats stub
	}
	for _, leak := range padded {
		t.Run(leak, func(t *testing.T) {
			assert.True(t, IsLeakySummaryString(leak),
				"detector must catch %q despite leading/trailing whitespace", leak)

			err := ValidateUserVisible(&SessionMeta{Summary: leak})
			require.Error(t, err, "writer-side guard must reject padded leak in Summary")

			err = ValidateUserVisible(&SessionMeta{Title: leak})
			require.Error(t, err, "writer-side guard must reject padded leak in Title")
		})
	}
}

// TestSessionMeta_Validate_RoundTripsThroughWriter is the integration
// proof that meta.Validate() == ValidateUserVisible(meta) and is wired
// through both writer entry points. The user-visible invariant is the
// same regardless of which writer the caller picks.
func TestSessionMeta_Validate_RoundTripsThroughWriter(t *testing.T) {
	bad := &SessionMeta{
		Version:     "1.0",
		SessionName: "s",
		AgentID:     "Ox",
		AgentType:   "claude-code",
		Title:       "Summary generation failed: nope",
	}

	// Direct method
	err := bad.Validate()
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "user-visible"))

	// Same invariant must trip the writer
	err = WriteSessionMetaOnly(t.TempDir(), bad)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "invariant"))
}
