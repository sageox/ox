package lfs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Independent title repairs must converge through a real pull. Git exit zero
// alone misses autostash conflicts, and JSONEq alone misses duplicate keys.
func TestTitleRepairConvergesThroughPull(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git clones and autostash")
	}
	for _, tc := range []struct {
		name          string
		sortedBase    bool
		sortedRemote  bool
		different     bool
		attempts      int
		emptyDefaults bool
	}{
		{name: "normal writer"},
		{name: "historical sorted base", sortedBase: true},
		{name: "previous failed attempts", attempts: 2},
		{name: "existing empty defaults", emptyDefaults: true},
		{name: "older remote writer", sortedRemote: true},
		{name: "different remote title", different: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writer, bare := initLedgerWithRemote(t)
			const rel = "sessions/test/meta.json"
			remoteSession := filepath.Join(writer, "sessions/test")
			require.NoError(t, os.MkdirAll(remoteSession, 0o755))
			meta := &SessionMeta{Version: "1.0", SessionName: "test", SessionID: "ses_keep",
				AgentType: "claude-code", CreatedAt: time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)}
			meta.SummaryAttempts = tc.attempts
			if tc.attempts > 0 {
				meta.ValidationError = "Earlier summary failure"
			}
			writeRemote := func(sorted bool) {
				t.Helper()
				require.NoError(t, WriteSessionMetaOnly(remoteSession, meta))
				if sorted {
					data, err := os.ReadFile(filepath.Join(writer, rel))
					require.NoError(t, err)
					var fields map[string]json.RawMessage
					require.NoError(t, json.Unmarshal(data, &fields))
					data, err = json.MarshalIndent(fields, "", "  ")
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(filepath.Join(writer, rel), data, 0o644))
				}
			}
			writeRemote(tc.sortedBase)
			if tc.emptyDefaults {
				seed, err := os.ReadFile(filepath.Join(writer, rel))
				require.NoError(t, err)
				seed = append(seed[:len(seed)-1], []byte(`,"title":"","summary":"","summary_status":"","validation_error":"","summary_attempts":0}`)...)
				require.NoError(t, os.WriteFile(filepath.Join(writer, rel), seed, 0o644))
			}
			git(t, writer, "add", "--sparse", rel)
			git(t, writer, "commit", "-m", "seed metadata")
			git(t, writer, "push")
			local := t.TempDir()
			git(t, writer, "clone", bare, local)
			git(t, local, "config", "user.name", "Test")
			git(t, local, "config", "user.email", "test@test.local")
			localSession := filepath.Join(local, "sessions/test")
			writeTestSummary(t, localSession, "Recovered title")
			require.Empty(t, RecoverEmptyTitleMeta(localSession, false).Error)
			repaired, err := os.ReadFile(filepath.Join(local, rel))
			require.NoError(t, err)
			meta.Title, meta.Summary, meta.SummaryStatus = "Recovered title", "Recovered title", "ok"
			meta.SummaryAttempts = 0
			if tc.different {
				meta.Title = "Customer changed this title"
			}
			writeRemote(tc.sortedRemote)
			git(t, writer, "commit", "-am", "independent remote repair")
			git(t, writer, "push")
			git(t, local, "pull", "--rebase", "--autostash", "--quiet")
			conflicts := git(t, local, "ls-files", "--unmerged")
			if tc.sortedRemote || tc.different {
				require.NotEmpty(t, conflicts)
				err = gitutil.WithRepoLock(context.Background(), local, func() error {
					_, resolveErr := gitutil.ResolveAutostashConflicts(context.Background(), local, []string{"sessions/"}, nil)
					return resolveErr
				})
				require.NotEmpty(t, git(t, local, "stash", "list"), "retain recovery backup")
				if tc.different {
					require.Error(t, err)
					assert.NotEmpty(t, git(t, local, "ls-files", "--unmerged"))
					return
				}
				require.NoError(t, err)
			} else {
				require.Empty(t, conflicts, "agreeing repairs must merge without recovery")
				remote, err := os.ReadFile(filepath.Join(writer, rel))
				require.NoError(t, err)
				assert.Equal(t, string(remote), string(repaired), "repair must match the normal writer")
			}
			assert.Empty(t, git(t, local, "ls-files", "--unmerged"))
			data, err := os.ReadFile(filepath.Join(local, rel))
			require.NoError(t, err)
			// Decode each top-level member separately so duplicate keys cannot hide.
			decoder := json.NewDecoder(strings.NewReader(string(data)))
			_, err = decoder.Token()
			require.NoError(t, err)
			seen := make(map[string]bool)
			for decoder.More() {
				key, err := decoder.Token()
				require.NoError(t, err)
				require.False(t, seen[key.(string)], "duplicate metadata key: %s", key)
				seen[key.(string)] = true
				var value json.RawMessage
				require.NoError(t, decoder.Decode(&value))
			}
			got, err := ReadSessionMeta(localSession)
			require.NoError(t, err)
			assert.Equal(t, meta, got)
		})
	}
}

// Upgrading must repair legacy error summaries without losing session identity,
// content references, extension fields, diagnostics, or customer-authored text.
func TestRecoverEmptyTitleMeta_PreservesLegacyData(t *testing.T) {
	const diagnostic = "Summary generation failed: legacy timeout"
	for _, tc := range []struct {
		name       string
		title      string
		summary    string
		status     string
		diagnostic string
		recovery   string
		draft      bool
		dryRun     bool
		skip       bool
	}{
		{name: "recover legacy error", summary: diagnostic, recovery: "Recovered title"},
		{name: "bound legacy error retries", summary: diagnostic},
		{name: "preserve both diagnostics", summary: diagnostic, diagnostic: "Earlier validation failure"},
		{name: "preserve valid summary", summary: "Customer-written summary", recovery: "Recovered title"},
		{name: "healthy title", title: "Customer-written title", summary: "Customer-written summary", recovery: "Stale title", skip: true},
		{name: "terminal metadata", summary: diagnostic, status: "unrecoverable", skip: true},
		{name: "live draft", draft: true, skip: true},
		{name: "dry run", summary: diagnostic, recovery: "Recovered title", dryRun: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			original := map[string]any{
				"version": "1.0", "session_name": "legacy-session", "session_id": "ses_preserve",
				"created_at": "2026-04-01T00:00:00Z", "title": tc.title, "summary": tc.summary,
				"summary_status": tc.status, "validation_error": tc.diagnostic, "draft": tc.draft,
				"files":           map[string]any{"raw.jsonl": map[string]any{"oid": "sha256:abc", "size": 123, "future_storage_field": "keep"}},
				"future_metadata": map[string]any{"large_id": json.Number("9007199254740993"), "value": "keep"},
			}
			if tc.draft {
				delete(original, "files")
			}
			// Write the legacy shape directly: today's writer rejects the very
			// error strings that an older CLI persisted.
			before, err := json.MarshalIndent(original, "", "  ")
			require.NoError(t, err)
			metaPath := filepath.Join(dir, "meta.json")
			require.NoError(t, os.WriteFile(metaPath, before, 0o600))
			writeTestSummary(t, dir, tc.recovery)
			summaryBefore, err := os.ReadFile(filepath.Join(dir, "summary.json"))
			require.NoError(t, err)
			raw := []byte("version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize 123\n")
			require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), raw, 0o600))

			out := RecoverEmptyTitleMeta(dir, tc.dryRun)
			require.Empty(t, out.Error)
			assert.Equal(t, tc.skip, out.Skipped)
			after, err := os.ReadFile(metaPath)
			require.NoError(t, err)
			if tc.skip || tc.dryRun {
				assert.Equal(t, before, after, "ineligible and dry-run metadata must stay byte-identical")
			} else {
				meta, err := ReadSessionMeta(dir)
				require.NoError(t, err)
				require.NoError(t, meta.Validate())
				if tc.recovery != "" {
					assert.Equal(t, tc.recovery, meta.Title)
					assert.True(t, out.RecoveredFromJSON)
				} else {
					assert.True(t, out.BumpedAttempts)
					assert.Equal(t, 1, meta.SummaryAttempts)
				}
				if tc.summary == diagnostic {
					assert.Contains(t, meta.ValidationError, diagnostic, "moving an error out of display fields must preserve it")
					assert.Contains(t, meta.ValidationError, tc.diagnostic)
				} else {
					assert.Equal(t, tc.summary, meta.Summary)
				}
				var oldFields, newFields map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(before, &oldFields))
				require.NoError(t, json.Unmarshal(after, &newFields))
				for _, field := range []string{"title", "summary", "summary_status", "summary_attempts", "validation_error"} {
					delete(oldFields, field)
					delete(newFields, field)
				}
				assert.Equal(t, oldFields, newFields, "repair must preserve all unowned fields, including unknown nested fields")
				info, err := os.Stat(metaPath)
				require.NoError(t, err)
				assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

				for range MaxSummaryAttempts {
					require.Empty(t, RecoverEmptyTitleMeta(dir, false).Error)
				}
				stable, err := os.ReadFile(metaPath)
				require.NoError(t, err)
				assert.True(t, RecoverEmptyTitleMeta(dir, false).Skipped)
				again, err := os.ReadFile(metaPath)
				require.NoError(t, err)
				assert.Equal(t, stable, again, "repeated upgrade repairs must converge to a no-op")
			}
			summaryAfter, err := os.ReadFile(filepath.Join(dir, "summary.json"))
			require.NoError(t, err)
			assert.Equal(t, summaryBefore, summaryAfter)
			rawAfter, err := os.ReadFile(filepath.Join(dir, "raw.jsonl"))
			require.NoError(t, err)
			assert.Equal(t, raw, rawAfter, "repair must never rewrite content or LFS stubs")
		})
	}
}

// Invalid historical JSON must remain available for deliberate recovery.
func TestRecoverEmptyTitleMeta_PreservesCorruptMetadata(t *testing.T) {
	for _, content := range []string{"{\n<<<<<<< HEAD\n\"title\": \"A\"\n=======\n\"title\": \"B\"\n>>>>>>> other\n}", `{"title":`, `null`} {
		dir := t.TempDir()
		path := filepath.Join(dir, "meta.json")
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
		writeTestSummary(t, dir, "Available title")
		for _, dryRun := range []bool{true, false, false} {
			require.NotEmpty(t, RecoverEmptyTitleMeta(dir, dryRun).Error)
		}
		after, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, content, string(after))
	}
}

// Repair must not replace a metadata symlink or rewrite its target on upgrade.
func TestRecoverEmptyTitleMeta_PreservesSymlink(t *testing.T) {
	targetDir := t.TempDir()
	writeTestMeta(t, targetDir, &SessionMeta{})
	target := filepath.Join(targetDir, "meta.json")
	before, err := os.ReadFile(target)
	require.NoError(t, err)
	dir := t.TempDir()
	link := filepath.Join(dir, "meta.json")
	require.NoError(t, os.Symlink(target, link))
	writeTestSummary(t, dir, "Recovered title")
	for _, dryRun := range []bool{true, false} {
		assert.Contains(t, RecoverEmptyTitleMeta(dir, dryRun).Error, "non-regular")
	}
	gotTarget, err := os.Readlink(link)
	require.NoError(t, err)
	assert.Equal(t, target, gotTarget)
	after, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

// Recovery after an earlier cleanup pass must not discard the moved diagnostic.
func TestRecoverEmptyTitleMeta_DelayedRecoveryKeepsDiagnostic(t *testing.T) {
	dir := t.TempDir()
	const diagnostic = "Summary generation failed: legacy timeout"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"),
		[]byte(`{"summary":"`+diagnostic+`","validation_error":"Earlier failure"}`), 0o600))
	require.Empty(t, RecoverEmptyTitleMeta(dir, false).Error)
	writeTestSummary(t, dir, "Recovered title")
	out := RecoverEmptyTitleMeta(dir, false)
	require.Empty(t, out.Error)
	assert.True(t, out.RecoveredFromJSON)
	meta, err := ReadSessionMeta(dir)
	require.NoError(t, err)
	assert.Equal(t, "Recovered title", meta.Title)
	assert.Contains(t, meta.ValidationError, diagnostic)
	assert.Contains(t, meta.ValidationError, "Earlier failure")
}

// A repair must wait for a cooperating writer and reread its latest metadata.
func TestRecoverEmptyTitleMeta_CoordinatesWithConcurrentWriter(t *testing.T) {
	dir := t.TempDir()
	writeTestMeta(t, dir, &SessionMeta{})
	writeTestSummary(t, dir, "Stale recovery title")
	done := make(chan MetaRepairOutcome, 1)
	err := fileutil.WithFileLock(context.Background(), filepath.Join(dir, "meta.json"), func() error {
		started := make(chan struct{})
		go func() {
			close(started)
			done <- RecoverEmptyTitleMeta(dir, false)
		}()
		<-started
		select {
		case <-done:
			t.Error("repair bypassed the metadata lock")
		case <-time.After(100 * time.Millisecond):
		}
		return WriteSessionMetaOnly(dir, &SessionMeta{Title: "Concurrent customer edit", SessionID: "ses_preserve"})
	})
	require.NoError(t, err)
	select {
	case out := <-done:
		require.Empty(t, out.Error)
		assert.True(t, out.Skipped)
	case <-time.After(time.Second):
		t.Fatal("repair did not finish after releasing the metadata lock")
	}
	meta, err := ReadSessionMeta(dir)
	require.NoError(t, err)
	assert.Equal(t, "Concurrent customer edit", meta.Title)
	assert.Equal(t, "ses_preserve", meta.SessionID)
}

// writeTestMeta is a small helper used by the recovery tests. It
// writes a minimal SessionMeta to <dir>/meta.json so each test reads
// like a setup + invariant check rather than 30 lines of boilerplate.
func writeTestMeta(t *testing.T, dir string, m *SessionMeta) {
	t.Helper()
	if m.SessionName == "" {
		m.SessionName = filepath.Base(dir)
	}
	if m.Files == nil {
		m.Files = make(map[string]FileRef)
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	if m.Version == "" {
		m.Version = "1.0"
	}
	require.NoError(t, WriteSessionMetaOnly(dir, m))
}

// writeTestSummary writes summary.json with a single Title field —
// enough for readSummaryJSONTitle's recovery path. Other fields are
// irrelevant to the helper.
func writeTestSummary(t *testing.T, dir, title string) {
	t.Helper()
	body := map[string]string{"title": title}
	b, err := json.Marshal(body)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "summary.json"), b, 0644))
}

// TestRecoverEmptyTitleMeta_HealthyMetaSkipped is the idempotency
// floor: a meta with a real title MUST NOT be touched, even if its
// summary.json says something different. We do not second-guess a
// title that the UI is already rendering.
//
// Failure prevented: a future "improvement" treats summary.json as
// the source of truth and clobbers user-edited or LLM-tuned titles
// with stale recovery values.
func TestRecoverEmptyTitleMeta_HealthyMetaSkipped(t *testing.T) {
	dir := t.TempDir()
	writeTestMeta(t, dir, &SessionMeta{Title: "Real Title", Summary: "real"})
	writeTestSummary(t, dir, "Different Title From summary.json")

	out := RecoverEmptyTitleMeta(dir, false)
	assert.True(t, out.Skipped, "healthy meta must be skipped")

	got, err := ReadSessionMeta(dir)
	require.NoError(t, err)
	assert.Equal(t, "Real Title", got.Title, "must not overwrite a healthy title")
}

// TestRecoverEmptyTitleMeta_UnrecoverableTerminalSkipped covers the
// terminal state. After MaxSummaryAttempts the daemon stamps
// SummaryStatus=unrecoverable; subsequent autofix passes must NOT
// re-engage and re-bump the counter, otherwise terminal becomes a
// misnomer.
func TestRecoverEmptyTitleMeta_UnrecoverableTerminalSkipped(t *testing.T) {
	dir := t.TempDir()
	writeTestMeta(t, dir, &SessionMeta{Title: "", SummaryStatus: "unrecoverable", SummaryAttempts: MaxSummaryAttempts})

	out := RecoverEmptyTitleMeta(dir, false)
	assert.True(t, out.Skipped, "unrecoverable meta is terminal; must be skipped")
	assert.False(t, out.BumpedAttempts, "must not bump attempt counter past terminal")
}

// TestRecoverEmptyTitleMeta_RecoversFromSummaryJSON is the happy
// repair path. Empty meta.title + clean summary.json → meta is
// updated, status flips to ok, attempt counter clears.
func TestRecoverEmptyTitleMeta_RecoversFromSummaryJSON(t *testing.T) {
	dir := t.TempDir()
	writeTestMeta(t, dir, &SessionMeta{
		Title:           "",
		SummaryStatus:   "failed_validation",
		ValidationError: "content validation failed: title too short",
		SummaryAttempts: 2,
	})
	writeTestSummary(t, dir, "Recovered Title From summary.json")

	out := RecoverEmptyTitleMeta(dir, false)
	assert.True(t, out.RecoveredFromJSON, "must flag recovery so caller can log/emit")
	assert.False(t, out.Skipped)

	got, err := ReadSessionMeta(dir)
	require.NoError(t, err)
	assert.Equal(t, "Recovered Title From summary.json", got.Title)
	assert.Equal(t, "ok", got.SummaryStatus, "status must transition failed_validation → ok")
	assert.Equal(t, "content validation failed: title too short", got.ValidationError,
		"automatic title repair must retain historical diagnostics")
	assert.Equal(t, 0, got.SummaryAttempts, "attempt counter must reset on success")
}

// TestRecoverEmptyTitleMeta_BumpsAttemptsWithoutSummary covers the
// degenerate case the user actually has on disk: meta.title is empty
// and summary.json is also empty (the daemon's failure stub). We can't
// recover anything, but we must still make progress toward the
// terminal state so the autofix scheduler eventually stops trying.
func TestRecoverEmptyTitleMeta_BumpsAttemptsWithoutSummary(t *testing.T) {
	dir := t.TempDir()
	writeTestMeta(t, dir, &SessionMeta{Title: "", SummaryStatus: "failed_validation", SummaryAttempts: 0})
	writeTestSummary(t, dir, "") // empty title in summary.json too

	out := RecoverEmptyTitleMeta(dir, false)
	assert.True(t, out.BumpedAttempts, "no recovery available → must bump attempts")
	assert.False(t, out.FlippedTerminal, "should not flip terminal on the first bump")

	got, err := ReadSessionMeta(dir)
	require.NoError(t, err)
	assert.Equal(t, 1, got.SummaryAttempts, "attempts must increment exactly once")
	assert.Equal(t, "failed_validation", got.SummaryStatus, "status stays failed_validation until cap")
}

// TestRecoverEmptyTitleMeta_FlipsToUnrecoverableAtCap is the cap
// behavior. After MaxSummaryAttempts bumps the status flips to
// unrecoverable, breaking the autofix loop on the next pass.
//
// Failure prevented: an unbounded autofix loop on a session whose
// raw.jsonl is corrupt, prompt is too large for the model, or
// summary.json is permanently empty. Without the cap, the daemon
// would re-finalize this session every 30 minutes forever.
func TestRecoverEmptyTitleMeta_FlipsToUnrecoverableAtCap(t *testing.T) {
	dir := t.TempDir()
	writeTestMeta(t, dir, &SessionMeta{Title: "", SummaryStatus: "failed_validation", SummaryAttempts: MaxSummaryAttempts - 1})

	out := RecoverEmptyTitleMeta(dir, false)
	assert.True(t, out.FlippedTerminal, "the bump that hits the cap must flip to terminal")

	got, err := ReadSessionMeta(dir)
	require.NoError(t, err)
	assert.Equal(t, "unrecoverable", got.SummaryStatus)
	assert.Equal(t, MaxSummaryAttempts, got.SummaryAttempts)

	// Idempotency floor: a second call on the now-terminal meta must
	// be a no-op.
	out2 := RecoverEmptyTitleMeta(dir, false)
	assert.True(t, out2.Skipped, "terminal state must short-circuit subsequent calls")
}

// TestRecoverEmptyTitleMeta_LeakySummaryRejected ensures that a
// summary.json whose title is itself a known validator-leak string
// does NOT get promoted into meta — that would silently re-introduce
// the ox-qqka leak the producer-side fixes already closed.
func TestRecoverEmptyTitleMeta_LeakySummaryRejected(t *testing.T) {
	dir := t.TempDir()
	writeTestMeta(t, dir, &SessionMeta{Title: "", SummaryStatus: "failed_validation"})
	writeTestSummary(t, dir, "Summary failed content validation: title too short")

	out := RecoverEmptyTitleMeta(dir, false)
	assert.False(t, out.RecoveredFromJSON, "must not promote a leaky title")
	assert.True(t, out.BumpedAttempts, "should fall through to the bump path")
}

// TestRecoverEmptyTitleMeta_DryRunWritesNothing confirms dryRun=true
// reports the planned outcome without touching disk.
func TestRecoverEmptyTitleMeta_DryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	writeTestMeta(t, dir, &SessionMeta{Title: "", SummaryStatus: "failed_validation"})
	writeTestSummary(t, dir, "Recovered Title")

	out := RecoverEmptyTitleMeta(dir, true /*dryRun*/)
	assert.True(t, out.RecoveredFromJSON, "dry-run still reports the planned outcome")

	got, err := ReadSessionMeta(dir)
	require.NoError(t, err)
	assert.Empty(t, got.Title, "dry-run must not modify meta.json on disk")
}

// TestResetInlineSummaryEligible_ResetsFileReadBugSessions verifies that
// sessions marked unrecoverable due to the pre-0.7.2 file-read prompt bug
// ("title too short") are reset for re-summarization with the inline prompt.
//
// Failure prevented: 40+ sessions stuck permanently in "unrecoverable"
// state even after the daemon is fixed to use inline prompts.
// writeTestTranscript gives a session a readable raw.jsonl. Eligible
// sessions in production always have one (a pointer or real content);
// ResetInlineSummaryEligible refuses to clear terminal state without it,
// because a session with no transcript can never be summarized.
func writeTestTranscript(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"),
		[]byte(`{"metadata":{},"type":"header"}`+"\n"+`{"type":"user","content":"hi"}`+"\n"), 0o644))
}

func TestResetInlineSummaryEligible_ResetsFileReadBugSessions(t *testing.T) {
	dir := t.TempDir()
	writeTestMeta(t, dir, &SessionMeta{
		Title:           "",
		SummaryStatus:   "unrecoverable",
		SummaryAttempts: MaxSummaryAttempts,
		ValidationError: "content validation failed: title too short (0 chars, minimum 3)",
	})
	writeTestTranscript(t, dir)

	reset := ResetInlineSummaryEligible(dir, false, nil, "")
	assert.True(t, reset)

	got, err := ReadSessionMeta(dir)
	require.NoError(t, err)
	assert.Equal(t, "", got.SummaryStatus, "status must be cleared for re-attempt")
	assert.Equal(t, 0, got.SummaryAttempts, "attempts must be reset to zero")
	assert.Equal(t, "", got.ValidationError, "validation error must be cleared")
}

// TestResetInlineSummaryEligible_SkipsHealthySessions verifies that sessions
// with successful summaries are not touched.
func TestResetInlineSummaryEligible_SkipsHealthySessions(t *testing.T) {
	dir := t.TempDir()
	writeTestMeta(t, dir, &SessionMeta{
		Title:         "Working Session",
		SummaryStatus: "ok",
	})

	reset := ResetInlineSummaryEligible(dir, false, nil, "")
	assert.False(t, reset)
}

// TestResetInlineSummaryEligible_SkipsUnrelatedUnrecoverable verifies that
// sessions marked unrecoverable for OTHER reasons (not the file-read bug)
// are not reset.
func TestResetInlineSummaryEligible_SkipsUnrelatedUnrecoverable(t *testing.T) {
	dir := t.TempDir()
	writeTestMeta(t, dir, &SessionMeta{
		Title:           "",
		SummaryStatus:   "unrecoverable",
		SummaryAttempts: MaxSummaryAttempts,
		ValidationError: "richness validation failed: key_actions empty",
	})

	reset := ResetInlineSummaryEligible(dir, false, nil, "")
	assert.False(t, reset, "should not reset sessions that failed for other reasons")
}

// TestResetInlineSummaryEligible_DryRun verifies no disk write in dry-run mode.
func TestResetInlineSummaryEligible_DryRun(t *testing.T) {
	dir := t.TempDir()
	writeTestMeta(t, dir, &SessionMeta{
		Title:           "",
		SummaryStatus:   "unrecoverable",
		SummaryAttempts: MaxSummaryAttempts,
		ValidationError: "content validation failed: title too short (0 chars, minimum 3)",
	})
	writeTestTranscript(t, dir)

	reset := ResetInlineSummaryEligible(dir, true, nil, "")
	assert.True(t, reset)

	got, err := ReadSessionMeta(dir)
	require.NoError(t, err)
	assert.Equal(t, "unrecoverable", got.SummaryStatus, "dry-run must not modify disk")
}
