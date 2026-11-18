package livesession_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/sessionstream"
)

func TestCloseSendsWholeStreamHashOnlyAfterFullAck(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 700)) // several requests before the close
	res, err := livesession.Close(t.Context(), e.cfg, e.dir, "short summary", testBudget)
	if err != nil || res.Status != livesession.StatusClosed || !res.Done {
		t.Fatalf("close: %+v %v", res, err)
	}

	spool := e.spoolBytes()
	got, ok := e.srv.ClosedWith(e.st.ConversationID)
	if !ok {
		t.Fatal("server never saw a close")
	}
	if got.StreamSHA256 != sessionstream.BytesSHA256(spool) || got.StoredThrough != int64(len(spool)) {
		t.Fatalf("close = %+v, want sha256 of the whole %d-byte spool", got.CloseRequest, len(spool))
	}
	if got.StoredAtClose != int64(len(spool)) {
		t.Fatalf("close arrived with %d bytes stored, want all %d acked first", got.StoredAtClose, len(spool))
	}
	if got.Summary != "short summary" {
		t.Fatalf("summary = %q", got.Summary)
	}
}

func TestCloseWithServerDownLeavesClosingWithinBudgetAndLaterFinishes(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 20))
	e.srv.Hang(true)

	start := time.Now()
	res, err := livesession.Close(t.Context(), e.cfg, e.dir, "", 300*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("close blocked %s past a 300ms budget", elapsed)
	}
	if err == nil || res.Done || res.Status != livesession.StatusClosing {
		t.Fatalf("res=%+v err=%v, want closing and not done", res, err)
	}
	if n := e.srv.CallsTo("POST", "/close"); n != 0 {
		t.Fatalf("close was sent %d times before the spool was acked", n)
	}
	if st := e.state(); st.Status != livesession.StatusClosing {
		t.Fatalf("persisted status = %s, want closing", st.Status)
	}
	if _, err := livesession.Spool(t.Context(), e.dir, lines(50, 51), livesession.SpoolOptions{}); !errors.Is(err, livesession.ErrNotStreaming) {
		t.Fatalf("a closing session accepted more spool: %v", err)
	}

	e.srv.Hang(false) // the daemon's later tick
	res, err = livesession.Close(t.Context(), e.cfg, e.dir, "", testBudget)
	if err != nil || res.Status != livesession.StatusClosed {
		t.Fatalf("retry close: %+v %v", res, err)
	}
	if _, ok := e.srv.ClosedWith(e.st.ConversationID); !ok {
		t.Fatal("server never saw the retried close")
	}
}

func TestAbortOfflineQueuesPendingDeleteAndRetryDrainsIt(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 3))
	e.srv.SetDown(true)

	res, err := livesession.Abort(t.Context(), e.cfg, e.dir, testBudget)
	if err != nil || !res.Queued || res.Deleted {
		t.Fatalf("abort offline: %+v %v", res, err)
	}
	queue := filepath.Join(e.cfg.DataDir, "live-pending-deletes.json")
	info, err := os.Stat(queue)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("queue file: %v mode=%v", err, info)
	}
	var pending []livesession.PendingDelete
	raw, _ := os.ReadFile(queue)
	if json.Unmarshal(raw, &pending) != nil || len(pending) != 1 || pending[0].ConversationID != e.st.ConversationID {
		t.Fatalf("queue = %s", raw)
	}
	if st := e.state(); st.Status != livesession.StatusDeleted {
		t.Fatalf("local status = %s, want deleted", st.Status)
	}

	if n, err := livesession.RetryPendingDeletes(t.Context(), e.cfg); err == nil || n != 0 {
		t.Fatalf("retry while down: n=%d err=%v, want failure and an intact queue", n, err)
	}
	e.srv.SetDown(false)
	n, err := livesession.RetryPendingDeletes(t.Context(), e.cfg)
	if err != nil || n != 1 || !e.srv.IsDeleted(e.st.ConversationID) {
		t.Fatalf("retry: n=%d err=%v deleted=%v", n, err, e.srv.IsDeleted(e.st.ConversationID))
	}
	raw, _ = os.ReadFile(queue)
	if json.Unmarshal(raw, &pending) != nil || len(pending) != 0 {
		t.Fatalf("queue after retry = %s, want empty", raw)
	}
}

func TestAbortOnlineDeletesOnServerWithoutQueueing(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 3))
	res, err := livesession.Abort(t.Context(), e.cfg, e.dir, testBudget)
	if err != nil || !res.Deleted || res.Queued || !e.srv.IsDeleted(e.st.ConversationID) {
		t.Fatalf("abort online: %+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(e.cfg.DataDir, "live-pending-deletes.json")); !os.IsNotExist(err) {
		t.Fatalf("a confirmed delete was queued anyway: %v", err)
	}
}

func TestAbortWithoutLiveStateDoesNothing(t *testing.T) {
	e := newEnv(t)
	res, err := livesession.Abort(t.Context(), e.cfg, t.TempDir(), testBudget)
	if err != nil || res != (livesession.AbortResult{}) || e.srv.CallsTo("DELETE", e.st.ConversationID) != 0 {
		t.Fatalf("abort of a never-streamed session: %+v %v", res, err)
	}
}
