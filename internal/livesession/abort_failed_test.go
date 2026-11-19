package livesession_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/livesession"
)

// failAbort makes the next Abort fail: SageOx is unreachable and the
// pending-deletes queue cannot be written, so the delete is neither confirmed
// nor recorded and the caller keeps the session (C2).
func failAbort(t *testing.T, e *env) {
	t.Helper()
	e.srv.SetDown(true)
	if err := os.MkdirAll(filepath.Join(e.cfg.DataDir, "live-pending-deletes.json"), 0o700); err != nil {
		t.Fatalf("block the queue: %v", err)
	}
}

// Failure prevented (C2/C10 seam): an abort whose delete was neither confirmed
// nor recorded returned an error and the recording was restored, but its
// delete-requested marker stayed. ReadState then said deleted, so the session
// went on recording with nothing spooled, drained or closed ever again.
func TestAFailedAbortLeavesTheSessionStreaming(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 2))
	e.mustDrain()
	failAbort(t, e)

	if _, err := livesession.Abort(t.Context(), e.cfg, e.dir, testBudget); err == nil {
		t.Fatal("precondition: the abort must fail")
	}

	if st := e.state(); st.Status != livesession.StatusStreaming {
		t.Fatalf("after a failed abort the session reads %s, want streaming", st.Status)
	}
	e.srv.SetDown(false)
	after := lines(2, 3)
	if _, err := livesession.Spool(t.Context(), e.dir, after, livesession.SpoolOptions{}); err != nil {
		t.Fatalf("the restored session refused a batch: %v", err)
	}
	e.mustDrain()
	if !bytes.HasSuffix(e.stored(e.state()), after) {
		t.Fatal("the restored session's next batch never reached the server")
	}
}

// The marker an earlier abort left is that abort's, and its delete may already
// be queued: a later abort that fails must not take it back.
func TestAFailedAbortKeepsAnEarlierAbortsMarker(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 2))
	e.mustDrain()
	marker := filepath.Join(e.dir, "delete-requested")
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	failAbort(t, e)

	if _, err := livesession.Abort(t.Context(), e.cfg, e.dir, testBudget); err == nil {
		t.Fatal("precondition: the abort must fail")
	}

	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("an earlier abort's marker was removed: %v", err)
	}
	if st := e.state(); st.Status != livesession.StatusDeleted {
		t.Fatalf("status = %s, want deleted while the earlier abort's marker stands", st.Status)
	}
}
