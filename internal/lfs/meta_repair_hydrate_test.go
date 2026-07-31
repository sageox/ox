package lfs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- GH #710: hydration failures must be diagnosable, and exactly one is terminal ---

// eligibleMeta writes the meta.json shape ResetInlineSummaryEligible
// targets: a failed summary whose validation_error mentions a short title.
func eligibleMeta(t *testing.T, sessionDir string) {
	t.Helper()
	meta := NewSessionMeta("2026-05-01T20-04-testuser-OxHYD", "testuser", "a1", "claude-code", time.Now().UTC()).Build()
	meta.SummaryStatus = "failed_validation"
	meta.ValidationError = "content validation failed: title too short (0 chars, minimum 3)"
	meta.SummaryAttempts = 2
	require.NoError(t, WriteSessionMetaOnly(sessionDir, meta))
}

func TestHydrateRawToCacheErr_NoManifestIsTerminal(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]FileRef
	}{
		{"no raw.jsonl entry", map[string]FileRef{}},
		{"raw.jsonl with empty OID", map[string]FileRef{"raw.jsonl": {Size: 10}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessionDir := t.TempDir()
			meta := NewSessionMeta("s", "u", "a", "claude-code", time.Now().UTC()).Build()
			meta.Files = tt.files
			require.NoError(t, WriteSessionMetaOnly(sessionDir, meta))

			_, err := HydrateRawToCacheErr(&Client{}, sessionDir, t.TempDir())

			require.Error(t, err)
			assert.ErrorIs(t, err, ErrNoLFSManifest,
				"a transcript that was never uploaded is permanently unrecoverable — this is "+
					"the ONE failure callers may treat as terminal")
		})
	}
}

func TestHydrateRawToCacheErr_OtherFailuresAreRetryable(t *testing.T) {
	t.Run("nil client", func(t *testing.T) {
		_, err := HydrateRawToCacheErr(nil, t.TempDir(), t.TempDir())
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrNoLFSManifest,
			"being unauthenticated is transient — condemning the session would lose it")
	})

	t.Run("unreadable meta", func(t *testing.T) {
		_, err := HydrateRawToCacheErr(&Client{}, t.TempDir(), t.TempDir())
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrNoLFSManifest,
			"a missing meta.json is a different problem, not proof the content is gone")
	})
}

// TestHydrateRawToCache_WrapperStillReturnsEmptyString pins the
// compatibility contract for the pre-existing callers.
func TestHydrateRawToCache_WrapperStillReturnsEmptyString(t *testing.T) {
	assert.Empty(t, HydrateRawToCache(nil, t.TempDir(), t.TempDir()))
}

// TestHydrateRawToCacheErr_UsesCacheWhenAlreadyHydrated also pins the
// cache-only invariant: hydration never touches the git-tracked file.
func TestHydrateRawToCacheErr_UsesCacheWhenAlreadyHydrated(t *testing.T) {
	ledgerPath := t.TempDir()
	sessionName := "2026-05-01T20-04-testuser-OxCACHE"
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	meta := NewSessionMeta(sessionName, "u", "a", "claude-code", time.Now().UTC()).Build()
	meta.Files = map[string]FileRef{"raw.jsonl": {OID: "sha256:abc", Size: 10}}
	require.NoError(t, WriteSessionMetaOnly(sessionDir, meta))

	// in-place stays a pointer; the real bytes live in the cache
	rawPath := filepath.Join(sessionDir, "raw.jsonl")
	require.NoError(t, WritePointerFile(rawPath, FileRef{OID: "sha256:abc", Size: 10}))

	cacheDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", sessionName)
	require.NoError(t, os.MkdirAll(cacheDir, 0o755))
	cachePath := filepath.Join(cacheDir, "raw.jsonl")
	require.NoError(t, os.WriteFile(cachePath, []byte("real transcript bytes\n"), 0o644))

	// nil client proves no network call was needed
	got, err := HydrateRawToCacheErr(nil, sessionDir, ledgerPath)
	require.NoError(t, err)
	assert.Equal(t, cachePath, got)

	assert.True(t, IsPointerFile(rawPath),
		"CACHE-ONLY: the git-tracked file must stay a pointer, or the next commit "+
			"breaks LFS linkage and every future push fails")
}

// TestResetInlineSummaryEligible_UnhydratablePointerDoesNotReset is the
// test that proves the unbounded loop is dead.
//
// A doctor autofix runs this daily. Pre-fix it cleared summary_status /
// summary_attempts / validation_error BEFORE attempting hydration, so a
// session whose transcript could not be fetched had its 3-attempt retry
// budget re-armed every 24 hours — burning LLM calls and emitting a fresh
// "finalize session" commit each time, forever.
func TestResetInlineSummaryEligible_UnhydratablePointerDoesNotReset(t *testing.T) {
	ledgerPath := t.TempDir()
	sessionDir := filepath.Join(ledgerPath, "sessions", "2026-05-01T20-04-testuser-OxHYD")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	eligibleMeta(t, sessionDir)
	require.NoError(t, WritePointerFile(filepath.Join(sessionDir, "raw.jsonl"),
		FileRef{OID: "sha256:abc", Size: 10}))

	before, err := os.ReadFile(filepath.Join(sessionDir, "meta.json"))
	require.NoError(t, err)

	// nil client — hydration is impossible on this pass
	reset := ResetInlineSummaryEligible(sessionDir, false, nil, ledgerPath)

	assert.False(t, reset, "must not reset a session it cannot make summarizable")

	after, err := os.ReadFile(filepath.Join(sessionDir, "meta.json"))
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after),
		"meta.json must be byte-identical — clearing the retry budget here is what "+
			"re-armed the loop every 24 hours")

	assert.NoFileExists(t, filepath.Join(sessionDir, ".needs-summary"),
		"a .needs-summary marker pointing at a pointer file is a lie that sends the "+
			"daemon straight back into the failing path")
}

// TestResetInlineSummaryEligible_HydratedPointerDoesReset is the other
// half: once the content IS available in the cache, the reset must
// happen, or the fix would simply disable recovery.
func TestResetInlineSummaryEligible_HydratedPointerDoesReset(t *testing.T) {
	ledgerPath := t.TempDir()
	sessionName := "2026-05-01T20-04-testuser-OxHYD"
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	eligibleMeta(t, sessionDir)
	require.NoError(t, WritePointerFile(filepath.Join(sessionDir, "raw.jsonl"),
		FileRef{OID: "sha256:abc", Size: 10}))

	cacheDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", sessionName)
	require.NoError(t, os.MkdirAll(cacheDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "raw.jsonl"),
		[]byte("header\nreal content\n"), 0o644))

	reset := ResetInlineSummaryEligible(sessionDir, false, nil, ledgerPath)

	assert.True(t, reset, "with content available in the cache, the session is recoverable")

	meta, err := ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.Empty(t, meta.SummaryStatus)
	assert.Zero(t, meta.SummaryAttempts)
	assert.Empty(t, meta.ValidationError)

	assert.FileExists(t, filepath.Join(cacheDir, ".needs-summary"),
		"the marker belongs beside the hydrated copy the daemon will actually read")
}

// TestResetInlineSummaryEligible_RealContentStillResets covers the
// non-pointer path, which the pointer gating must not disturb.
func TestResetInlineSummaryEligible_RealContentStillResets(t *testing.T) {
	ledgerPath := t.TempDir()
	sessionDir := filepath.Join(ledgerPath, "sessions", "2026-05-01T20-04-testuser-OxHYD")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	eligibleMeta(t, sessionDir)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"),
		[]byte("header\nreal content\n"), 0o644))

	assert.True(t, ResetInlineSummaryEligible(sessionDir, false, nil, ledgerPath))

	meta, err := ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.Empty(t, meta.SummaryStatus)
	assert.Zero(t, meta.SummaryAttempts)
}

// TestResetInlineSummaryEligible_IneligibleUntouched — the filter itself.
func TestResetInlineSummaryEligible_IneligibleUntouched(t *testing.T) {
	sessionDir := t.TempDir()
	meta := NewSessionMeta("s", "u", "a", "claude-code", time.Now().UTC()).Build()
	meta.SummaryStatus = "ok"
	meta.Title = "A good title"
	require.NoError(t, WriteSessionMetaOnly(sessionDir, meta))

	assert.False(t, ResetInlineSummaryEligible(sessionDir, false, nil, t.TempDir()),
		"a healthy session must never be reset")
}

// TestHydrateRawToCacheErr_FallsBackToOnDiskPointer — the manifest is not
// the only place the OID lives. A meta.json rebuilt by one of the
// pre-GH#710 builder paths lost its Files map entirely, but the committed
// pointer file still carries the OID.
//
// ErrNoLFSManifest is the ONE error callers treat as terminal, so a false
// positive here permanently condemns a session whose transcript is sitting
// in the content store, perfectly recoverable.
func TestHydrateRawToCacheErr_FallsBackToOnDiskPointer(t *testing.T) {
	ledgerPath := t.TempDir()
	sessionName := "2026-05-01T20-04-testuser-OxPTR"
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	// meta.json with NO Files map — the pre-#710 stripped shape
	meta := NewSessionMeta(sessionName, "u", "a", "claude-code", time.Now().UTC()).Build()
	require.NoError(t, WriteSessionMetaOnly(sessionDir, meta))

	// ...but a valid pointer on disk, carrying the OID
	require.NoError(t, WritePointerFile(filepath.Join(sessionDir, "raw.jsonl"),
		FileRef{OID: "sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", Size: 4242}))

	// nil client, so this gets as far as needing the network and no further —
	// what matters is that it is NOT the terminal sentinel.
	_, err := HydrateRawToCacheErr(nil, sessionDir, ledgerPath)

	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNoLFSManifest,
		"the OID is recoverable from the pointer file — condemning this session would lose it")
	// Assert the SPECIFIC error, not merely "not the sentinel": otherwise
	// this test would pass if the function bailed for some unrelated reason
	// before it ever consulted the pointer.
	assert.Contains(t, err.Error(), "no content-store client",
		"must have got past the manifest check and reached the network step")
}

// TestHydrateRawToCacheErr_TerminalOnlyWhenNeitherSourceHasAnOID keeps the
// sentinel meaningful: with no manifest entry AND no usable pointer, the
// content really is unreferenced.
func TestHydrateRawToCacheErr_TerminalOnlyWhenNeitherSourceHasAnOID(t *testing.T) {
	ledgerPath := t.TempDir()
	sessionDir := filepath.Join(ledgerPath, "sessions", "2026-05-01T20-04-testuser-OxNON")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	meta := NewSessionMeta("s", "u", "a", "claude-code", time.Now().UTC()).Build()
	require.NoError(t, WriteSessionMetaOnly(sessionDir, meta))
	// raw.jsonl present but real content, not a pointer — nothing to hydrate from
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"),
		[]byte("header\nnot a pointer\n"), 0o644))

	_, err := HydrateRawToCacheErr(&Client{}, sessionDir, ledgerPath)

	assert.ErrorIs(t, err, ErrNoLFSManifest,
		"neither the manifest nor the on-disk file yields an OID — genuinely terminal")
}

// TestResetInlineSummaryEligible_MissingTranscriptDoesNotReset closes the
// branch the pointer gate misses. IsPointerFile is false for an ABSENT
// raw.jsonl, so an eligible session with no transcript at all used to fall
// straight through: terminal state cleared, and a .needs-summary marker
// written pointing at a file that does not exist. The next daemon pass
// then tries to summarize content that cannot be read — the same unbounded
// loop as the pointer case, one branch over.
func TestResetInlineSummaryEligible_MissingTranscriptDoesNotReset(t *testing.T) {
	ledgerPath := t.TempDir()
	sessionDir := filepath.Join(ledgerPath, "sessions", "2026-05-01T20-04-testuser-OxMISS")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	eligibleMeta(t, sessionDir)
	// deliberately NO raw.jsonl at all

	before, err := os.ReadFile(filepath.Join(sessionDir, "meta.json"))
	require.NoError(t, err)

	assert.False(t, ResetInlineSummaryEligible(sessionDir, false, nil, ledgerPath),
		"a session with no transcript can never be summarized — clearing its terminal state re-arms the loop")

	after, err := os.ReadFile(filepath.Join(sessionDir, "meta.json"))
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "meta.json must be byte-identical")
	assert.NoFileExists(t, filepath.Join(sessionDir, ".needs-summary"),
		"a marker pointing at a nonexistent transcript is a lie the daemon will act on")
}

// TestResetInlineSummaryEligible_EmptyTranscriptDoesNotReset — a
// zero-byte raw.jsonl is equally unsummarizable.
func TestResetInlineSummaryEligible_EmptyTranscriptDoesNotReset(t *testing.T) {
	ledgerPath := t.TempDir()
	sessionDir := filepath.Join(ledgerPath, "sessions", "2026-05-01T20-04-testuser-OxEMPT")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	eligibleMeta(t, sessionDir)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), nil, 0o644))

	assert.False(t, ResetInlineSummaryEligible(sessionDir, false, nil, ledgerPath))
	assert.NoFileExists(t, filepath.Join(sessionDir, ".needs-summary"))
}
