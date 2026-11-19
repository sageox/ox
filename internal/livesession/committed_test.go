package livesession_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/session"
)

// recordingOf is the recording that owns e's stream.
func (e *env) recordingOf() *session.RecordingState {
	return &session.RecordingState{SessionID: e.st.SessionID, SessionPath: e.dir}
}

// replaceWithDirectory puts a directory where the file at path was, until
// restore puts the file back. Reading or writing a directory as a file fails on
// every platform and for every user; permission bits do not (Windows ignores
// them, and so does root).
func replaceWithDirectory(t *testing.T, path string) (restore func()) {
	t.Helper()
	aside := path + ".aside"
	if err := os.Rename(path, aside); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(aside, path); err != nil {
			t.Fatal(err)
		}
	}
}

// unreadableStreamState makes the stream's state unreadable until restore.
func unreadableStreamState(t *testing.T, dir string) (restore func()) {
	t.Helper()
	path := filepath.Join(dir, "stream-state.json")
	restore = replaceWithDirectory(t, path)
	if _, err := os.ReadFile(path); err == nil {
		t.Fatal("precondition: the stream state can still be read")
	}
	return restore
}

// unwritableSpool makes the spool refuse an append until restore.
func unwritableSpool(t *testing.T, dir string) (restore func()) {
	t.Helper()
	path := filepath.Join(dir, "spool.jsonl")
	restore = replaceWithDirectory(t, path)
	if f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0); err == nil {
		_ = f.Close()
		t.Fatal("precondition: the spool can still be appended to")
	}
	return restore
}

// The step every capture path takes after a commit hands the stream exactly
// the committed bytes and reports them spooled, so the caller asks for a drain.
func TestSpoolCommittedSpoolsExactlyTheBatch(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.spool(lines(0, 1))
	before := e.spoolBytes()
	batch := lines(1, 3)

	spooled, err := livesession.SpoolCommitted(t.Context(), "", e.recordingOf(), session.AppendedBatch{Bytes: batch})

	if err != nil || !spooled {
		t.Fatalf("spooled=%v err=%v, want the batch spooled", spooled, err)
	}
	if got := e.spoolBytes()[len(before):]; !bytes.Equal(got, batch) {
		t.Fatalf("spool gained %q, want exactly the committed batch", got)
	}
}

// Batches the stream must not carry are left out quietly: never spooled, never
// declared, and no drain is asked for.
func TestSpoolCommittedKeepsBatchesOutOfStreamsTheyMustNotReach(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		arrange func(t *testing.T, e *env, rec *session.RecordingState)
		paused  bool
	}{
		{name: "the stream belongs to the previous recording in a reused folder (C4)", arrange: func(_ *testing.T, _ *env, rec *session.RecordingState) {
			rec.SessionID = "ses_01934f3e-6b2a-7c3d-8e4f-5a6b7c8d9e99"
		}},
		{name: "the session is held on this machine (C1)", arrange: func(t *testing.T, e *env, _ *session.RecordingState) {
			if err := session.WriteHoldMarker(e.dir, session.HoldManualPublishing, "test"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "the recording switched to manual publishing (C1)", arrange: func(_ *testing.T, _ *env, rec *session.RecordingState) {
			rec.PublishingMode = config.SessionPublishingManual
		}},
		{name: "a paused batch, even when the stream state cannot be read", paused: true, arrange: func(t *testing.T, e *env, _ *session.RecordingState) {
			unreadableStreamState(t, e.dir)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			e.spool(lines(0, 1))
			before := e.spoolBytes()
			rec := e.recordingOf()
			tt.arrange(t, e, rec)

			spooled, err := livesession.SpoolCommitted(t.Context(), "", rec, session.AppendedBatch{Bytes: lines(1, 2), Suspended: tt.paused})

			if err != nil || spooled {
				t.Fatalf("spooled=%v err=%v, want the batch left out quietly", spooled, err)
			}
			if _, err := os.Stat(filepath.Join(e.dir, "spool-lost")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("a batch the stream must not carry was declared lost: %v", err)
			}
			if got := e.spoolBytes(); !bytes.Equal(got, before) {
				t.Fatalf("spool gained %q", got[len(before):])
			}
		})
	}
}

// A session switched to manual publishing in its project's configuration
// stops streaming too (C1). Not parallel: the mode comes from the environment.
func TestSpoolCommittedHoldsASessionItsProjectPublishesManually(t *testing.T) {
	t.Setenv(config.EnvSessionPublishing, config.SessionPublishingManual)
	e := newEnv(t)
	e.spool(lines(0, 1))
	before := e.spoolBytes()

	spooled, err := livesession.SpoolCommitted(t.Context(), t.TempDir(), e.recordingOf(), session.AppendedBatch{Bytes: lines(1, 2)})

	if err != nil || spooled || !bytes.Equal(e.spoolBytes(), before) {
		t.Fatalf("spooled=%v err=%v, want a manually published session held", spooled, err)
	}
}

// A session that never prepared a stream has nothing to feed or declare.
func TestSpoolCommittedIgnoresASessionThatDoesNotStream(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	rec := &session.RecordingState{SessionID: "ses_01934f3e-6b2a-7c3d-8e4f-5a6b7c8d9e01", SessionPath: dir}

	spooled, err := livesession.SpoolCommitted(t.Context(), "", rec, session.AppendedBatch{Bytes: lines(0, 1)})

	if err != nil || spooled {
		t.Fatalf("spooled=%v err=%v, want nothing done", spooled, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("wrote %d files into a session that does not stream", len(entries))
	}
}

// Failure prevented (finding 5, F3): a capture committed a batch and moved its
// cursor, then the batch could not reach the stream, and nothing said so. The
// stream carried on without it and the close claimed it whole. Whatever stops
// the batch after the commit, the next write must carry one spool_write_failed
// gap of exactly the batch's size: never none, and never two.
func TestSpoolCommittedDeclaresABatchTheStreamCouldNotTake(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// fail makes the stream refuse the next batch, and returns what undoes it
		fail func(t *testing.T, e *env) (restore func())
	}{
		{name: "the stream state cannot be read", fail: func(t *testing.T, e *env) func() {
			return unreadableStreamState(t, e.dir)
		}},
		{name: "the spool cannot be written", fail: func(t *testing.T, e *env) func() {
			return unwritableSpool(t, e.dir)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			e.spool(lines(0, 1))
			batch := lines(1, 3)
			restore := tt.fail(t, e)

			spooled, err := livesession.SpoolCommitted(t.Context(), "", e.recordingOf(), session.AppendedBatch{Bytes: batch})

			restore()
			if err == nil || spooled {
				t.Fatalf("spooled=%v err=%v, want the batch reported lost", spooled, err)
			}
			e.spoolDeclaresLoss(lines(3, 4), int64(len(batch)))
		})
	}
}
