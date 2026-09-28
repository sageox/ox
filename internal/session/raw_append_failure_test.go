package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/require"
)

const credentialCommand = `{"command":["/bin/bash","-lc","aws configure export-credentials"]}`

// --- A. Append journal: recovery must refuse what it cannot prove ---

// TestRecoverRawAppendRefusesUnprovableJournals covers every journal state
// recovery cannot reason about. Each one must be an error rather than a silent
// "nothing to recover": a swallowed failure here either replays a batch twice
// or leaves unacknowledged bytes behind a cursor that claims they are durable.
// Failure prevented: duplicated or torn transcript content after a crash.
func TestRecoverRawAppendRefusesUnprovableJournals(t *testing.T) {
	journal := func(c rawAppendCheckpoint) string {
		b, err := json.Marshal(c)
		require.NoError(t, err)
		return string(b)
	}
	tests := []struct {
		name      string
		journal   string
		raw       *string // nil leaves raw.jsonl absent
		persisted int64
		wantErr   string
	}{
		{name: "corrupt journal", journal: "{not json", persisted: 0, wantErr: "invalid character"},
		{name: "negative raw size", journal: journal(rawAppendCheckpoint{RawSize: -1, OldOffset: 0, NewOffset: 10}), wantErr: "invalid capture checkpoint"},
		{name: "cursor does not advance", journal: journal(rawAppendCheckpoint{RawSize: 0, OldOffset: 10, NewOffset: 10}), persisted: 10, wantErr: "invalid capture checkpoint"},
		{name: "uncommitted batch but transcript is gone", journal: journal(rawAppendCheckpoint{RawSize: 4, OldOffset: 0, NewOffset: 10}), persisted: 0, wantErr: "raw.jsonl"},
		{name: "uncommitted batch but transcript shrank", journal: journal(rawAppendCheckpoint{RawSize: 40, OldOffset: 0, NewOffset: 10}), raw: strPtr("short\n"), persisted: 0, wantErr: "truncated"},
		{name: "committed batch but transcript is gone", journal: journal(rawAppendCheckpoint{RawSize: 4, OldOffset: 0, NewOffset: 10}), persisted: 10, wantErr: "raw.jsonl"},
		// The writer can produce neither of these. Each one, taken at its word,
		// costs transcript: the first blesses a file that shrank as "committed",
		// the second matches a corrupt cursor and truncates to the journal's size.
		{name: "sealed smaller than it started", journal: journal(rawAppendCheckpoint{RawSize: 40, OldOffset: 0, NewOffset: 10, FinalSize: int64Ptr(6)}), raw: strPtr("short\n"), persisted: 10, wantErr: "invalid capture checkpoint"},
		{name: "negative starting cursor", journal: journal(rawAppendCheckpoint{RawSize: 0, OldOffset: -5, NewOffset: 10}), raw: strPtr("short\n"), persisted: -5, wantErr: "invalid capture checkpoint"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := filepath.Join(t.TempDir(), "raw.jsonl")
			if tt.raw != nil {
				require.NoError(t, os.WriteFile(raw, []byte(*tt.raw), 0600))
			}
			require.NoError(t, os.WriteFile(raw+".append.json", []byte(tt.journal), 0600))

			err := RecoverRawAppend(raw, tt.persisted)

			require.ErrorContains(t, err, tt.wantErr)
			require.FileExists(t, raw+".append.json", "a journal recovery could not act on must survive for the next attempt")
			if tt.raw != nil {
				after, readErr := os.ReadFile(raw)
				require.NoError(t, readErr)
				require.Equal(t, *tt.raw, string(after), "a refused journal must not cost any transcript")
			}
		})
	}
}

// TestRecoverRawAppendSurfacesAnUnreadableJournal distinguishes "no journal"
// from "could not look": a directory where the journal file belongs fails the
// read on every platform, and must not read as "nothing pending".
func TestRecoverRawAppendSurfacesAnUnreadableJournal(t *testing.T) {
	raw := filepath.Join(t.TempDir(), "raw.jsonl")
	require.NoError(t, os.Mkdir(raw+".append.json", 0700))

	require.Error(t, RecoverRawAppend(raw, 0))
}

// TestSealAppendRefusesToCommitABrokenBatch pins the step that lets the cursor
// advance. Sealing must fail when the journal is missing or corrupt, when the
// transcript shrank underneath it, or when the file can no longer be synced.
// Failure prevented: a cursor committed over bytes that never reached disk.
func TestSealAppendRefusesToCommitABrokenBatch(t *testing.T) {
	open := func(t *testing.T) (*RawWriter, string) {
		raw := filepath.Join(t.TempDir(), "raw.jsonl")
		require.NoError(t, os.WriteFile(raw, []byte("{\"type\":\"header\"}\n"), 0600))
		w, err := NewRawWriter(raw, "")
		require.NoError(t, err)
		t.Cleanup(func() { _ = w.Close() })
		return w, raw
	}
	t.Run("no journal", func(t *testing.T) {
		w, _ := open(t)
		require.Error(t, w.SealAppend())
	})
	t.Run("corrupt journal", func(t *testing.T) {
		w, raw := open(t)
		require.NoError(t, os.WriteFile(raw+".append.json", []byte("{not json"), 0600))
		require.Error(t, w.SealAppend())
	})
	t.Run("transcript shrank after the batch began", func(t *testing.T) {
		w, raw := open(t)
		require.NoError(t, w.BeginAppend(0, 10))
		require.NoError(t, os.Truncate(raw, 0))
		require.ErrorContains(t, w.SealAppend(), "truncated")
	})
	t.Run("file already closed", func(t *testing.T) {
		w, _ := open(t)
		require.NoError(t, w.BeginAppend(0, 10))
		require.NoError(t, w.file.Close())
		require.Error(t, w.SealAppend())
		require.Error(t, w.BeginAppend(0, 10), "a closed file cannot journal a rollback point either")
	})
}

// TestAppendRecordingBatchLeavesStateUntouchedWhenItCannotStart verifies the
// transaction aborts before any bytes or cursor move when its preconditions
// fail, so the same batch is safely retryable.
func TestAppendRecordingBatchLeavesStateUntouchedWhenItCannotStart(t *testing.T) {
	setup := func(t *testing.T, state *RecordingState) (*RawWriter, string, string) {
		dir := t.TempDir()
		raw := filepath.Join(dir, "raw.jsonl")
		statePath := filepath.Join(dir, ".recording.json")
		state.SessionPath = dir
		require.NoError(t, fileutil.AtomicWriteJSON(statePath, state, 0600))
		w, err := NewRawWriter(raw, "")
		require.NoError(t, err)
		t.Cleanup(func() { _ = w.Close() })
		return w, raw, statePath
	}
	assertUntouched := func(t *testing.T, raw, statePath string, stateBefore []byte) {
		rawAfter, err := os.ReadFile(raw)
		require.NoError(t, err)
		require.Empty(t, rawAfter, "no transcript bytes may be written")
		stateAfter, err := os.ReadFile(statePath)
		require.NoError(t, err)
		require.Equal(t, stateBefore, stateAfter, "the cursor must not move")
	}
	t.Run("unsupported redaction checkpoint", func(t *testing.T) {
		w, raw, statePath := setup(t, &RecordingState{CommandRedactionVersion: 2})
		before, err := os.ReadFile(statePath)
		require.NoError(t, err)
		require.ErrorContains(t, w.AppendRecordingBatch(statePath, []Entry{{Type: EntryTypeUser, Content: "x"}}, 10), "unsupported command redaction checkpoint")
		assertUntouched(t, raw, statePath, before)
	})
	t.Run("rollback point cannot be journaled", func(t *testing.T) {
		w, raw, statePath := setup(t, &RecordingState{})
		before, err := os.ReadFile(statePath)
		require.NoError(t, err)
		require.NoError(t, w.file.Close())
		require.Error(t, w.AppendRecordingBatch(statePath, []Entry{{Type: EntryTypeUser, Content: "x"}}, 10))
		assertUntouched(t, raw, statePath, before)
	})
}

// --- B. Redaction checkpoint: never trust a forged or corrupt one ---

// TestRestoreCaptureRedactionRejectsUntrustworthyCheckpoints covers the
// checkpoint that decides whether a later tool RESULT gets redacted. It is read
// from disk, so it is validated: an unknown version, an implausible size, an
// empty call ID, or a slug no loaded rule owns are all refused rather than
// applied. Failure prevented: credential output published because a corrupt or
// tampered checkpoint dropped or remapped its pending redaction.
func TestRestoreCaptureRedactionRejectsUntrustworthyCheckpoints(t *testing.T) {
	w, err := NewRawStreamWriter(discard{}, "")
	require.NoError(t, err)
	require.NotEmpty(t, w.cmdRedactor.rules, "fixture needs at least one built-in rule to name a valid slug")
	validSlug := w.cmdRedactor.rules[0].Slug

	oversized := make(map[string]string, 4097)
	for i := 0; i < 4097; i++ {
		oversized[fmt.Sprintf("call-%d", i)] = validSlug
	}
	tests := []struct {
		name    string
		state   RecordingState
		wantErr string
	}{
		{name: "unknown version", state: RecordingState{CommandRedactionVersion: 2}, wantErr: "unsupported command redaction checkpoint"},
		{name: "oversized", state: RecordingState{CommandRedactionVersion: 1, PendingCommandRedactions: oversized}, wantErr: "oversized command redaction checkpoint"},
		{name: "empty call id", state: RecordingState{CommandRedactionVersion: 1, PendingCommandRedactions: map[string]string{"": validSlug}}, wantErr: "invalid command redaction checkpoint"},
		{name: "slug no rule owns", state: RecordingState{CommandRedactionVersion: 1, PendingCommandRedactions: map[string]string{"call": "not-a-real-rule"}}, wantErr: "invalid command redaction checkpoint"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.ErrorContains(t, w.RestoreCaptureRedaction(&tt.state, filepath.Join(t.TempDir(), "raw.jsonl")), tt.wantErr)
		})
	}

	// Negative control: a well-formed checkpoint is accepted and restored, so
	// the rejections above are about content, not about the call always failing.
	ok := RecordingState{CommandRedactionVersion: 1, PendingCommandRedactions: map[string]string{"call": validSlug}}
	require.NoError(t, w.RestoreCaptureRedaction(&ok, filepath.Join(t.TempDir(), "raw.jsonl")))
	require.Equal(t, validSlug, w.cmdRedactor.pending["call"])
}

// TestLegacyRedactionReconstructionHandlesMissingAndCorruptTranscripts covers
// recordings that predate the checkpoint: state is rebuilt by rescanning the
// transcript. A transcript that does not exist yet means nothing is pending; one
// that cannot be parsed must stop capture rather than proceed without it.
func TestLegacyRedactionReconstructionHandlesMissingAndCorruptTranscripts(t *testing.T) {
	w, err := NewRawStreamWriter(discard{}, "")
	require.NoError(t, err)

	require.NoError(t, w.RestoreCaptureRedaction(&RecordingState{}, filepath.Join(t.TempDir(), "absent.jsonl")))

	corrupt := filepath.Join(t.TempDir(), "raw.jsonl")
	require.NoError(t, os.WriteFile(corrupt, []byte("{not json\n"), 0600))
	require.ErrorContains(t, w.RestoreCaptureRedaction(&RecordingState{}, corrupt), "reconstruct command redaction")
}

// TestUnmatchedCredentialCallsAreBounded verifies the pending-redaction map
// cannot grow without limit, live or during legacy reconstruction. Past the
// bound capture fails closed instead of silently forgetting a redaction.
// Failure prevented: corrupt history exhausting memory, or the 4097th
// credential result being written unredacted.
func TestUnmatchedCredentialCallsAreBounded(t *testing.T) {
	t.Run("live capture", func(t *testing.T) {
		w, err := NewRawStreamWriter(discard{}, "")
		require.NoError(t, err)
		var writeErr error
		for i := 0; i <= 4096 && writeErr == nil; i++ {
			writeErr = w.WriteEntry(&SessionEntry{Type: EntryTypeTool, CallID: fmt.Sprintf("call-%d", i), ToolInput: credentialCommand})
		}
		require.ErrorContains(t, writeErr, "too many unmatched credential-output calls")
	})
	t.Run("legacy reconstruction", func(t *testing.T) {
		var transcript strings.Builder
		for i := 0; i <= 4096; i++ {
			line, err := json.Marshal(SessionEntry{Type: EntryTypeTool, CallID: fmt.Sprintf("call-%d", i), ToolInput: credentialCommand})
			require.NoError(t, err)
			transcript.Write(line)
			transcript.WriteByte('\n')
		}
		raw := filepath.Join(t.TempDir(), "raw.jsonl")
		require.NoError(t, os.WriteFile(raw, []byte(transcript.String()), 0600))
		w, err := NewRawStreamWriter(discard{}, "")
		require.NoError(t, err)
		require.ErrorContains(t, w.RestoreCaptureRedaction(&RecordingState{}, raw), "too many unmatched credential-output calls")
	})
}

// --- C. Redaction policy: a bad policy is fatal BEFORE output is touched ---

// TestMalformedRedactionPolicyFailsBeforeTruncatingOutput verifies a repository
// whose REDACT.md cannot be parsed stops the writer before it opens -- and so
// before it truncates -- the destination. Ignoring a malformed rule would
// publish content the repository owner meant to exclude.
func TestMalformedRedactionPolicyFailsBeforeTruncatingOutput(t *testing.T) {
	projectRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, ".sageox"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".sageox", "REDACT.md"), []byte("```redact\nregex \"([unclosed\" -> [REDACTED]\n```\n"), 0600))
	_, problems := NewRedactorWithCustomRules(projectRoot)
	require.NotEmpty(t, problems, "fixture must actually be an invalid policy, or this test proves nothing")

	existing := filepath.Join(t.TempDir(), "raw.jsonl")
	require.NoError(t, os.WriteFile(existing, []byte("prior content\n"), 0600))

	_, err := NewRawSnapshotWriter(existing, projectRoot)
	require.ErrorContains(t, err, "invalid redaction policy")
	_, err = NewRawStreamWriter(discard{}, projectRoot)
	require.ErrorContains(t, err, "invalid redaction policy")

	after, err := os.ReadFile(existing)
	require.NoError(t, err)
	require.Equal(t, "prior content\n", string(after), "a rejected policy must not truncate existing output")
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func strPtr(s string) *string { return &s }

func int64Ptr(v int64) *int64 { return &v }

// --- E. Recovery and carrier footers ---
//
// A carrier footer (StampRawCarrier) is the one write that legitimately lands
// on raw.jsonl between a crash and the next recovery: a SessionEnd hook or
// `session native` stamps without consulting the journal. Recovery must neither
// truncate it away nor refuse the file because of it.

const rawHeaderLine = "{\"type\":\"header\"}\n"

func stampNativeFooter(t *testing.T, raw, id string) {
	t.Helper()
	require.NoError(t, StampRawCarrier(raw, CarrierStamp{NativeSessions: []lfs.NativeSession{{ID: id}}}))
}

// TestRollbackKeepsAFooterStampedAfterTheCrash verifies the rollback drops the
// unacknowledged batch and nothing else.
// Failure prevented: a native-session footer stamped by the SessionEnd hook
// removed by the next recovery, so the daemon finalizes without the ids it
// carried and the session's /c/ page never learns its native id.
func TestRollbackKeepsAFooterStampedAfterTheCrash(t *testing.T) {
	raw := filepath.Join(t.TempDir(), "raw.jsonl")
	require.NoError(t, os.WriteFile(raw, []byte(rawHeaderLine), 0o600))
	// A hook died mid-batch: journal at the header's size, cursor never
	// committed, a torn entry on disk. Then the end hook stamped a footer.
	journal, err := json.Marshal(rawAppendCheckpoint{RawSize: int64(len(rawHeaderLine)), OldOffset: 0, NewOffset: 10})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(raw+".append.json", journal, 0o600))
	require.NoError(t, os.WriteFile(raw, []byte(rawHeaderLine+"{\"type\":\"tool\",\"content\":\"torn\"}\n{\"type\":\"tool\",\"content\":\"half"), 0o600))
	stampNativeFooter(t, raw, "native-1")

	require.NoError(t, RecoverRawAppend(raw, 0))

	after, err := os.ReadFile(raw)
	require.NoError(t, err)
	require.NotContains(t, string(after), "torn", "the unacknowledged batch must still be rolled back")
	require.NotContains(t, string(after), "half", "a torn partial entry is part of the batch")
	require.Contains(t, string(after), "native-1", "the rollback took the footer stamped after the crash")
	require.True(t, strings.HasPrefix(string(after), rawHeaderLine))
	require.NoFileExists(t, raw+".append.json")
	// What is left must still read as a session with one footer and no entries.
	stored, err := ReadSessionFromPath(raw)
	require.NoError(t, err)
	require.Empty(t, stored.Entries)
}

// TestCommittedBatchAcceptsOnlyFootersPastItsSealedSize verifies the committed
// check tolerates footers stamped after the seal and nothing else.
// Failure prevented: a committed recording refused forever ("size conflicts")
// because a footer landed before the journal was removed -- and, the other way,
// a replaced longer file accepted as the one that was sealed.
func TestCommittedBatchAcceptsOnlyFootersPastItsSealedSize(t *testing.T) {
	sealed := func(t *testing.T) (raw string) {
		t.Helper()
		raw = filepath.Join(t.TempDir(), "raw.jsonl")
		body := rawHeaderLine + "{\"type\":\"user\",\"content\":\"committed\"}\n"
		require.NoError(t, os.WriteFile(raw, []byte(body), 0o600))
		size := int64(len(body))
		journal, err := json.Marshal(rawAppendCheckpoint{RawSize: int64(len(rawHeaderLine)), OldOffset: 0, NewOffset: 10, FinalSize: &size})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(raw+".append.json", journal, 0o600))
		return raw
	}
	t.Run("footer stamped after the seal", func(t *testing.T) {
		raw := sealed(t)
		stampNativeFooter(t, raw, "native-2")
		require.NoError(t, RecoverRawAppend(raw, 10))
		require.NoFileExists(t, raw+".append.json")
		after, err := os.ReadFile(raw)
		require.NoError(t, err)
		require.Contains(t, string(after), "committed")
		require.Contains(t, string(after), "native-2")
	})
	t.Run("other content past the seal is refused", func(t *testing.T) {
		raw := sealed(t)
		f, err := os.OpenFile(raw, os.O_WRONLY|os.O_APPEND, 0o600)
		require.NoError(t, err)
		_, err = f.WriteString("{\"type\":\"user\",\"content\":\"not sealed\"}\n")
		require.NoError(t, err)
		require.NoError(t, f.Close())
		require.ErrorContains(t, RecoverRawAppend(raw, 10), "conflicts with pending batch")
		require.FileExists(t, raw+".append.json")
	})
}

// TestJournalRollbackCannotTakeAConcurrentFooter verifies a rollback cannot run
// while a stamp is mid-write: both hold the append lock.
// Failure prevented: a footer half-written when the truncate lands, leaving a
// torn line the next stamp "repairs" into garbage.
func TestJournalRollbackCannotTakeAConcurrentFooter(t *testing.T) {
	raw := filepath.Join(t.TempDir(), "raw.jsonl")
	require.NoError(t, os.WriteFile(raw, []byte(rawHeaderLine+"{\"type\":\"tool\",\"content\":\"torn\"}\n"), 0o600))
	journal, err := json.Marshal(rawAppendCheckpoint{RawSize: int64(len(rawHeaderLine)), OldOffset: 0, NewOffset: 10})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(raw+".append.json", journal, 0o600))

	held := make(chan struct{})
	release := make(chan struct{})
	stampDone := make(chan error, 1)
	go func() {
		stampDone <- withRawAppendLock(raw, func() error {
			close(held)
			<-release
			return stampRawCarrier(raw, CarrierStamp{NativeSessions: []lfs.NativeSession{{ID: "native-3"}}})
		})
	}()
	<-held

	recovered := make(chan error, 1)
	go func() { recovered <- RecoverRawAppend(raw, 0) }()
	select {
	case <-recovered:
		t.Fatal("recovery ran while a carrier stamp held the append lock")
	case <-time.After(300 * time.Millisecond):
	}

	close(release)
	require.NoError(t, <-stampDone)
	require.NoError(t, <-recovered)
	after, err := os.ReadFile(raw)
	require.NoError(t, err)
	require.NotContains(t, string(after), "torn")
	require.Contains(t, string(after), "native-3")
}
