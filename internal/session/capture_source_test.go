package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/pkg/codexhistory"
	"github.com/sageox/ox/pkg/sessionprovenance"
	"github.com/stretchr/testify/require"
)

// codexSourceHeader returns a minimal, valid Codex native-history file
// consisting of just the session_meta header line: enough for
// codexhistory.Stream to produce a NativeID + non-zero Size + StartedAt.
func codexSourceHeader(id string) string {
	return fmt.Sprintf(`{"timestamp":"2026-09-15T10:00:00Z","type":"session_meta","payload":{"id":"%s","cwd":"/project","source":"cli"}}`, id) + "\n"
}

func writeCodexSourceFile(t *testing.T, id string) (path string, size int64) {
	t.Helper()
	header := codexSourceHeader(id)
	path = filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(header), 0600))
	info, err := os.Stat(path)
	require.NoError(t, err)
	return path, info.Size()
}

// initLedgerWithOrigin creates a bare "remote" and a working clone tracking
// it, matching what CheckCapturePublication expects: `git fetch origin` and
// `git merge-base --is-ancestor @{upstream} HEAD` must both succeed.
func initLedgerWithOrigin(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	bare := filepath.Join(t.TempDir(), "remote.git")
	repo := filepath.Join(t.TempDir(), "ledger")
	runGitCmd(t, "", "init", "--bare", "--quiet", bare)
	runGitCmd(t, "", "clone", "--quiet", bare, repo)
	runGitCmd(t, repo, "config", "user.email", "test@test.local")
	runGitCmd(t, repo, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README"), []byte("x"), 0600))
	runGitCmd(t, repo, "add", "README")
	runGitCmd(t, repo, "commit", "-m", "init", "--no-verify", "--quiet")
	runGitCmd(t, repo, "push", "--quiet", "-u", "origin", "HEAD")
	return repo
}

func runGitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, string(out))
}

func TestCaptureSourceRangesRespectNativePauseBoundaries(t *testing.T) {
	state := &RecordingState{StartOffset: 10, Lifecycle: []LifecycleEvent{{Action: LifecycleActionPause, Offset: 20, SourceOffsetKnown: true}, {Action: LifecycleActionResume, Offset: 30, SourceOffsetKnown: true}, {Action: LifecycleActionPause, Offset: 40, SourceOffsetKnown: true}}}
	spans, err := captureSourceRanges(state, 50)
	require.NoError(t, err)
	require.Equal(t, []sessionprovenance.Range{{Start: 10, End: 20}, {Start: 30, End: 40}}, spans)
	state.Lifecycle[0].SourceOffsetKnown = false
	_, err = captureSourceRanges(state, 50)
	require.ErrorContains(t, err, "legacy pause")
}
func TestRecordSourceCoverageNeverErasesExclusionsOrDuplicates(t *testing.T) {
	ledger := t.TempDir()
	id := "01a0a62a-f6d2-7a62-9213-fd142782db91"
	require.NoError(t, ExcludeNativeSession(ledger, id, "paused", 20, 30))
	source := &sessionprovenance.Source{Version: 1, Agent: "codex", NativeSessionID: id, Generation: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Ranges: []sessionprovenance.Range{{Start: 0, End: 20}, {Start: 30, End: 50}}}
	oid := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	_, err := RecordSourceCoverage(ledger, "session", oid, source)
	require.NoError(t, err)
	before, err := ReadSourceRecord(ledger, id)
	require.NoError(t, err)
	_, err = RecordSourceCoverage(ledger, "session", oid, source)
	require.NoError(t, err)
	after, err := ReadSourceRecord(ledger, id)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Len(t, after.Coverage, 2)
	require.True(t, after.Excludes(20, 21))
	_, err = RecordSourceCoverage(ledger, "duplicate", oid, source)
	require.ErrorContains(t, err, "conflicting")
	source.Ranges = []sessionprovenance.Range{{Start: 0, End: 50}}
	_, err = RecordSourceCoverage(ledger, "resurrection", oid, source)
	require.ErrorContains(t, err, "excluded")
}

// TestCaptureSourceRangesRejectsInvalidPauseResumeOrdering prevents a
// corrupted or hand-edited lifecycle timeline (double pause, resume with no
// matching pause) from producing bogus source ranges that would either
// double-count or omit real capture coverage.
func TestCaptureSourceRangesRejectsInvalidPauseResumeOrdering(t *testing.T) {
	cases := []struct {
		name      string
		lifecycle []LifecycleEvent
		wantErr   string
	}{
		{"resume_without_pause", []LifecycleEvent{{Action: LifecycleActionResume, Offset: 20, SourceOffsetKnown: true}}, "invalid resume timeline"},
		{"double_pause", []LifecycleEvent{{Action: LifecycleActionPause, Offset: 20, SourceOffsetKnown: true}, {Action: LifecycleActionPause, Offset: 30, SourceOffsetKnown: true}}, "invalid pause timeline"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := &RecordingState{StartOffset: 10, Lifecycle: tc.lifecycle}
			_, err := captureSourceRanges(state, 50)
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

// TestCaptureSourceRangesRejectsFullyPausedCapture prevents a capture source
// receipt with zero eligible byte ranges from being written — a receipt with
// no ranges would let a future publication check treat the whole capture as
// implicitly covered.
func TestCaptureSourceRangesRejectsFullyPausedCapture(t *testing.T) {
	state := &RecordingState{StartOffset: 10, Lifecycle: []LifecycleEvent{{Action: LifecycleActionPause, Offset: 10, SourceOffsetKnown: true}}}
	_, err := captureSourceRanges(state, 50)
	require.ErrorContains(t, err, "no eligible source ranges")
}

// TestSaveCaptureSourceSkipsNonCodexAndUnidentifiableCaptures prevents
// SaveCaptureSource from ever writing a provenance receipt for adapters
// other than Codex, or for a legacy opaque session handle that predates the
// native-identity contract — a receipt for either would be uncheckable.
func TestSaveCaptureSourceSkipsNonCodexAndUnidentifiableCaptures(t *testing.T) {
	cases := []*RecordingState{
		{AdapterName: "claude-code", AgentSessionID: "01a0a62a-f6d2-7a62-9213-fd142782db91"},
		{AdapterName: "codex", AgentSessionID: ""},
		{AdapterName: "codex", AgentSessionID: "legacy-opaque-handle"},
	}
	for _, state := range cases {
		require.NoError(t, SaveCaptureSource(state))
	}
}

// TestSaveCaptureSourceErrorsWhenSourceFileUnreadable prevents a capture
// whose native source file vanished (or was never valid Codex history) from
// silently producing no error and no receipt — the caller needs to know
// capture provenance could not be established.
func TestSaveCaptureSourceErrorsWhenSourceFileUnreadable(t *testing.T) {
	state := &RecordingState{
		AdapterName:    "codex",
		AgentSessionID: "01a0a62a-f6d2-7a62-9213-fd142782db92",
		SessionFile:    filepath.Join(t.TempDir(), "missing.jsonl"),
		SourceOffset:   10,
	}
	require.ErrorContains(t, SaveCaptureSource(state), "capture provenance")
}

// TestSaveCaptureSourceRejectsNativeIdentityMismatch prevents a capture from
// being attributed to the wrong native Codex session when the source file's
// actual header identity differs from what the recording state believes it
// is capturing (e.g. the file was replaced underneath the recording).
func TestSaveCaptureSourceRejectsNativeIdentityMismatch(t *testing.T) {
	fileID := "01a0a62a-f6d2-7a62-9213-fd142782db93"
	path, size := writeCodexSourceFile(t, fileID)
	state := &RecordingState{
		AdapterName:    "codex",
		AgentSessionID: "01a0a62a-f6d2-7a62-9213-fd142782db94",
		SessionFile:    path,
		SourceOffset:   size,
	}
	require.ErrorContains(t, SaveCaptureSource(state), "native identity changed")
}

// TestSaveCaptureSourceRejectsIncompleteCoverage prevents a capture receipt
// from claiming coverage of bytes the recording never actually observed —
// end<=start would claim an empty/negative span, and end>size would claim
// bytes beyond what was hashed into the snapshot digest.
func TestSaveCaptureSourceRejectsIncompleteCoverage(t *testing.T) {
	id := "01a0a62a-f6d2-7a62-9213-fd142782db95"
	path, size := writeCodexSourceFile(t, id)
	for name, tc := range map[string]struct{ start, end int64 }{
		"end_before_start": {10, 5},
		"end_equals_start": {5, 5},
		"end_beyond_size":  {0, size + 1000},
	} {
		t.Run(name, func(t *testing.T) {
			state := &RecordingState{AdapterName: "codex", AgentSessionID: id, SessionFile: path, StartOffset: tc.start, SourceOffset: tc.end}
			require.ErrorContains(t, SaveCaptureSource(state), "coverage is incomplete")
		})
	}
}

// TestSaveCaptureSourceWritesReadableProvenanceReceipt is the load-bearing
// happy path: a completed Codex capture must produce a `.capture-source.json`
// receipt that ReadCaptureSource can read back, with ranges matching the
// actual captured span. Without this, RecordSourceCoverage/CheckCapturePublication
// have nothing to reconcile against and Codex capture provenance is unverifiable.
func TestSaveCaptureSourceWritesReadableProvenanceReceipt(t *testing.T) {
	id := "01a0a62a-f6d2-7a62-9213-fd142782db96"
	path, size := writeCodexSourceFile(t, id)
	sessionDir := t.TempDir()
	state := &RecordingState{
		AdapterName:    "codex",
		AgentSessionID: id,
		SessionFile:    path,
		SessionPath:    sessionDir,
		StartOffset:    0,
		SourceOffset:   size,
		StartedAt:      time.Now(),
	}
	require.NoError(t, SaveCaptureSource(state))
	source, err := ReadCaptureSource(sessionDir)
	require.NoError(t, err)
	require.NotNil(t, source)
	require.Equal(t, id, source.NativeSessionID)
	require.Equal(t, codexhistory.ParserVersion, source.ParserVersion)
	require.Equal(t, []sessionprovenance.Range{{Start: 0, End: size}}, source.Ranges)
}

// TestReadCaptureSourceReturnsNilForMissingFile lets callers on the common
// path (no receipt yet, e.g. a non-Codex or pre-upgrade recording) treat
// absence as "nothing to reconcile" rather than an error.
func TestReadCaptureSourceReturnsNilForMissingFile(t *testing.T) {
	source, err := ReadCaptureSource(t.TempDir())
	require.NoError(t, err)
	require.Nil(t, source)
}

// TestReadCaptureSourceRejectsMalformedOrDowngradeIncompatibleReceipts
// prevents a corrupted or future-versioned receipt from being silently
// trusted, which could let capture coverage reconciliation operate on
// garbage or on a shape this binary doesn't understand.
func TestReadCaptureSourceRejectsMalformedOrDowngradeIncompatibleReceipts(t *testing.T) {
	cases := map[string]string{
		"invalid_json":      "{not json",
		"wrong_version":     `{"version":2,"agent":"codex","native_session_id":"01a0a62a-f6d2-7a62-9213-fd142782db97"}`,
		"wrong_agent":       `{"version":1,"agent":"claude-code","native_session_id":"01a0a62a-f6d2-7a62-9213-fd142782db97"}`,
		"invalid_native_id": `{"version":1,"agent":"codex","native_session_id":"not-a-uuid"}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".capture-source.json"), []byte(content), 0600))
			_, err := ReadCaptureSource(dir)
			require.Error(t, err)
		})
	}
}

// TestCheckCapturePublicationRefusesPendingLocalDeletionOrExclusion prevents
// a capture from being uploaded or copied out while the user has a pending
// local deletion/exclusion request in flight for the same session directory
// — publishing first would race the user's own intent to remove the content.
func TestCheckCapturePublicationRefusesPendingLocalDeletionOrExclusion(t *testing.T) {
	for _, marker := range []string{".deletion-pending.json", ".capture-exclusion-pending.json"} {
		t.Run(marker, func(t *testing.T) {
			dir := t.TempDir()
			rawPath := filepath.Join(dir, "raw.jsonl")
			require.NoError(t, os.WriteFile(rawPath, []byte("{}"), 0600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, marker), []byte("{}"), 0600))
			err := CheckCapturePublication(context.Background(), "unused-ledger", "session", rawPath, nil)
			require.ErrorContains(t, err, "pending local deletion")
		})
	}
}

// TestCheckCapturePublicationNilSourceNeverTouchesGit proves a capture with
// no native-source receipt (e.g. a non-Codex recording) skips the git
// reconciliation path entirely — passing a nonexistent ledger path here
// would fail loudly if the function tried to git-fetch it.
func TestCheckCapturePublicationNilSourceNeverTouchesGit(t *testing.T) {
	dir := t.TempDir()
	rawPath := filepath.Join(dir, "raw.jsonl")
	require.NoError(t, os.WriteFile(rawPath, []byte("{}"), 0600))
	notARepo := filepath.Join(t.TempDir(), "not-a-repo")
	require.NoError(t, CheckCapturePublication(context.Background(), notARepo, "session", rawPath, nil))
}

// TestCheckCapturePublicationValidatesSourceShapeAndRanges prevents a
// malformed capture-source receipt (wrong version/agent, empty generation,
// no ranges, or a negative/empty range) from ever being reconciled — each of
// these shapes indicates either a corrupted receipt or an attempt to publish
// coverage that was never actually captured.
func TestCheckCapturePublicationValidatesSourceShapeAndRanges(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git clone")
	}
	ledger := initLedgerWithOrigin(t)
	dir := t.TempDir()
	rawPath := filepath.Join(dir, "raw.jsonl")
	require.NoError(t, os.WriteFile(rawPath, []byte("raw-content"), 0600))
	id := "01a0a62a-f6d2-7a62-9213-fd142782db98"
	gen := strings.Repeat("a", 64)

	cases := []struct {
		name    string
		ranges  []sessionprovenance.Range
		mutate  func(*sessionprovenance.Source)
		wantErr string
	}{
		{"wrong_version", []sessionprovenance.Range{{Start: 0, End: 10}}, func(s *sessionprovenance.Source) { s.Version = 2 }, "invalid capture source"},
		{"wrong_agent", []sessionprovenance.Range{{Start: 0, End: 10}}, func(s *sessionprovenance.Source) { s.Agent = "claude-code" }, "invalid capture source"},
		{"empty_generation", []sessionprovenance.Range{{Start: 0, End: 10}}, func(s *sessionprovenance.Source) { s.Generation = "" }, "invalid capture source"},
		{"no_ranges", nil, func(s *sessionprovenance.Source) {}, "invalid capture source"},
		{"negative_start", []sessionprovenance.Range{{Start: -1, End: 10}}, func(s *sessionprovenance.Source) {}, "invalid source range"},
		{"end_not_after_start", []sessionprovenance.Range{{Start: 10, End: 10}}, func(s *sessionprovenance.Source) {}, "invalid source range"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := &sessionprovenance.Source{Version: 1, Agent: "codex", NativeSessionID: id, Generation: gen, Ranges: tc.ranges}
			tc.mutate(source)
			err := CheckCapturePublication(context.Background(), ledger, "session", rawPath, source)
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

// TestCheckCapturePublicationAllowsFreshCoverageWithNoPriorRecord proves a
// brand-new native session (nothing yet written to data/session-sources) is
// allowed to publish — the common case for a session's first upload.
func TestCheckCapturePublicationAllowsFreshCoverageWithNoPriorRecord(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git clone")
	}
	ledger := initLedgerWithOrigin(t)
	dir := t.TempDir()
	rawPath := filepath.Join(dir, "raw.jsonl")
	require.NoError(t, os.WriteFile(rawPath, []byte("raw-content"), 0600))
	id := "01a0a62a-f6d2-7a62-9213-fd142782db99"
	source := &sessionprovenance.Source{Version: 1, Agent: "codex", NativeSessionID: id, Generation: strings.Repeat("b", 64), Ranges: []sessionprovenance.Range{{Start: 0, End: 5}}}
	require.NoError(t, CheckCapturePublication(context.Background(), ledger, "session", rawPath, source))
}

// TestCheckCapturePublicationRejectsGenerationConflict prevents publishing
// coverage captured against a stale native-session generation (the source
// file was truncated/replaced since a prior record was written) — accepting
// it would misattribute new bytes to an old, no-longer-valid history.
func TestCheckCapturePublicationRejectsGenerationConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git clone")
	}
	ledger := initLedgerWithOrigin(t)
	dir := t.TempDir()
	rawPath := filepath.Join(dir, "raw.jsonl")
	require.NoError(t, os.WriteFile(rawPath, []byte("raw-content"), 0600))
	id := "01a0a62a-f6d2-7a62-9213-fd142782db9a"
	require.NoError(t, WriteSourceRecord(ledger, &sessionprovenance.Record{Version: 1, Agent: "codex", NativeSessionID: id, Generation: strings.Repeat("a", 64)}))
	source := &sessionprovenance.Source{Version: 1, Agent: "codex", NativeSessionID: id, Generation: strings.Repeat("b", 64), Ranges: []sessionprovenance.Range{{Start: 0, End: 5}}}
	require.ErrorContains(t, CheckCapturePublication(context.Background(), ledger, "session", rawPath, source), "generation conflict")
}

// TestCheckCapturePublicationRejectsExcludedRange prevents a session that
// the user explicitly excluded (e.g. deleted or paused-and-discarded) from
// being resurrected by a later publish that happens to cover the same bytes.
func TestCheckCapturePublicationRejectsExcludedRange(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git clone")
	}
	ledger := initLedgerWithOrigin(t)
	dir := t.TempDir()
	rawPath := filepath.Join(dir, "raw.jsonl")
	require.NoError(t, os.WriteFile(rawPath, []byte("raw-content"), 0600))
	id := "01a0a62a-f6d2-7a62-9213-fd142782db9c"
	require.NoError(t, ExcludeNativeSession(ledger, id, "paused", 0, 20))
	source := &sessionprovenance.Source{Version: 1, Agent: "codex", NativeSessionID: id, Generation: strings.Repeat("a", 64), Ranges: []sessionprovenance.Range{{Start: 5, End: 15}}}
	require.ErrorContains(t, CheckCapturePublication(context.Background(), ledger, "session", rawPath, source), "excluded")
}

// TestCheckCapturePublicationRejectsConflictingCoverage prevents two
// different session names from both claiming the same native-history byte
// range — that would mean the same Codex turns end up duplicated (or
// contradictorily attributed) across two ox sessions.
func TestCheckCapturePublicationRejectsConflictingCoverage(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git clone")
	}
	ledger := initLedgerWithOrigin(t)
	dir := t.TempDir()
	rawPath := filepath.Join(dir, "raw.jsonl")
	require.NoError(t, os.WriteFile(rawPath, []byte("raw-content"), 0600))
	id := "01a0a62a-f6d2-7a62-9213-fd142782db9d"
	gen := strings.Repeat("a", 64)
	_, err := RecordSourceCoverage(ledger, "session-one", strings.Repeat("1", 64), &sessionprovenance.Source{Version: 1, Agent: "codex", NativeSessionID: id, Generation: gen, Ranges: []sessionprovenance.Range{{Start: 0, End: 10}}})
	require.NoError(t, err)
	source := &sessionprovenance.Source{Version: 1, Agent: "codex", NativeSessionID: id, Generation: gen, Ranges: []sessionprovenance.Range{{Start: 0, End: 10}}}
	require.ErrorContains(t, CheckCapturePublication(context.Background(), ledger, "session-two", rawPath, source), "conflicting source coverage")
}

// TestCheckCapturePublicationRejectsContentMismatch prevents publishing when
// the raw.jsonl bytes on disk no longer match what was recorded as coverage
// for this exact session/range — the file was edited or corrupted between
// recording and publish, and uploading it would silently drift from the
// receipt that supposedly proves its provenance.
func TestCheckCapturePublicationRejectsContentMismatch(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git clone")
	}
	ledger := initLedgerWithOrigin(t)
	dir := t.TempDir()
	rawPath := filepath.Join(dir, "raw.jsonl")
	require.NoError(t, os.WriteFile(rawPath, []byte("actual-content"), 0600))
	id := "01a0a62a-f6d2-7a62-9213-fd142782db9e"
	gen := strings.Repeat("a", 64)
	recordedOID := strings.Repeat("1", 64) // does not match sha256("actual-content")
	_, err := RecordSourceCoverage(ledger, "session-a", recordedOID, &sessionprovenance.Source{Version: 1, Agent: "codex", NativeSessionID: id, Generation: gen, Ranges: []sessionprovenance.Range{{Start: 0, End: 10}}})
	require.NoError(t, err)
	source := &sessionprovenance.Source{Version: 1, Agent: "codex", NativeSessionID: id, Generation: gen, Ranges: []sessionprovenance.Range{{Start: 0, End: 10}}}
	require.ErrorContains(t, CheckCapturePublication(context.Background(), ledger, "session-a", rawPath, source), "conflicting source content")
}

// TestCheckCapturePublicationAcceptsMatchingContent covers both raw-content
// forms CheckCapturePublication must hash-verify: a plain (not-yet-uploaded)
// file and an already-uploaded LFS pointer file. Both must succeed when the
// OID genuinely matches the recorded coverage.
func TestCheckCapturePublicationAcceptsMatchingContent(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git clone")
	}
	ledger := initLedgerWithOrigin(t)
	gen := strings.Repeat("a", 64)

	t.Run("plain_file", func(t *testing.T) {
		dir := t.TempDir()
		rawPath := filepath.Join(dir, "raw.jsonl")
		content := "hello-world"
		require.NoError(t, os.WriteFile(rawPath, []byte(content), 0600))
		sum := sha256.Sum256([]byte(content))
		oid := hex.EncodeToString(sum[:])
		id := "01a0a62a-f6d2-7a62-9213-fd142782db9f"
		_, err := RecordSourceCoverage(ledger, "session-a", oid, &sessionprovenance.Source{Version: 1, Agent: "codex", NativeSessionID: id, Generation: gen, Ranges: []sessionprovenance.Range{{Start: 0, End: 10}}})
		require.NoError(t, err)
		source := &sessionprovenance.Source{Version: 1, Agent: "codex", NativeSessionID: id, Generation: gen, Ranges: []sessionprovenance.Range{{Start: 0, End: 10}}}
		require.NoError(t, CheckCapturePublication(context.Background(), ledger, "session-a", rawPath, source))
	})

	t.Run("lfs_pointer_file", func(t *testing.T) {
		dir := t.TempDir()
		rawPath := filepath.Join(dir, "raw.jsonl")
		oid := strings.Repeat("2", 64)
		require.NoError(t, os.WriteFile(rawPath, []byte(lfs.FormatPointer("sha256:"+oid, 123)), 0600))
		id := "01a0a62b-f6d2-7a62-9213-fd142782db91"
		_, err := RecordSourceCoverage(ledger, "session-a", oid, &sessionprovenance.Source{Version: 1, Agent: "codex", NativeSessionID: id, Generation: gen, Ranges: []sessionprovenance.Range{{Start: 0, End: 10}}})
		require.NoError(t, err)
		source := &sessionprovenance.Source{Version: 1, Agent: "codex", NativeSessionID: id, Generation: gen, Ranges: []sessionprovenance.Range{{Start: 0, End: 10}}}
		require.NoError(t, CheckCapturePublication(context.Background(), ledger, "session-a", rawPath, source))
	})
}

// TestRecordSourceCoverageNilSourceIsNoop prevents a nil source (a capture
// with no provenance receipt, e.g. non-Codex) from causing
// RecordSourceCoverage to error or write anything.
func TestRecordSourceCoverageNilSourceIsNoop(t *testing.T) {
	oid, err := RecordSourceCoverage(t.TempDir(), "session", "abc", nil)
	require.NoError(t, err)
	require.Empty(t, oid)
}

// TestRecordSourceCoverageStripsSha256Prefix prevents the "sha256:" FileRef
// convention from leaking into the provenance record's RawOID field, which
// must stay in canonical bare-hex form for CheckCapturePublication's own
// bare-hex comparisons to ever match.
func TestRecordSourceCoverageStripsSha256Prefix(t *testing.T) {
	ledger := t.TempDir()
	id := "01a0a62b-f6d2-7a62-9213-fd142782db92"
	gen := strings.Repeat("a", 64)
	oidHex := strings.Repeat("3", 64)
	_, err := RecordSourceCoverage(ledger, "session", "sha256:"+oidHex, &sessionprovenance.Source{Version: 1, Agent: "codex", NativeSessionID: id, Generation: gen, Ranges: []sessionprovenance.Range{{Start: 0, End: 5}}})
	require.NoError(t, err)
	record, err := ReadSourceRecord(ledger, id)
	require.NoError(t, err)
	require.Equal(t, oidHex, record.Coverage[0].RawOID)
}
