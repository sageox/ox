package livesession_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/livesession"
)

// A refused delete leaves the content on the server. Reporting it as deleted
// (or dropping it from the queue) told the person their session was gone.
func TestAbortRefusedDeleteIsNeverReportedAsDeleted(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 3))
	e.mustDrain()
	e.srv.RefuseDelete(true)

	res, err := livesession.Abort(t.Context(), e.cfg, e.dir, testBudget)
	if err != nil || res.Deleted || !res.Refused || res.Reason != "forbidden" {
		t.Fatalf("abort refused by the server: %+v %v, want refused (forbidden), not deleted", res, err)
	}
	if e.srv.IsDeleted(e.st.ConversationID) {
		t.Fatal("test setup: the server deleted it anyway")
	}
	refused, err := livesession.RefusedDeletes(t.Context(), e.cfg)
	if err != nil || len(refused) != 1 || refused[0].ConversationID != e.st.ConversationID || refused[0].Reason != "forbidden" {
		t.Fatalf("refused deletes = %+v %v, want the one refusal kept visible", refused, err)
	}

	// kept, but not retried forever
	sent := e.srv.CallsTo("DELETE", e.st.ConversationID)
	if n, err := livesession.RetryPendingDeletes(t.Context(), e.cfg); n != 0 || err != nil {
		t.Fatalf("retry: n=%d err=%v, want nothing confirmed and nothing failing", n, err)
	}
	if again := e.srv.CallsTo("DELETE", e.st.ConversationID); again != sent {
		t.Fatalf("a refused delete was sent again (%d -> %d)", sent, again)
	}
	if q := pendingDeletes(t, e.cfg); len(q) != 1 || !q[0].Refused {
		t.Fatalf("queue = %+v, want the refusal recorded", q)
	}
}

// A refusal heard first by the daemon's retry is recorded the same way.
func TestRetryPendingDeletesRecordsARefusalInsteadOfDroppingIt(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 3))
	e.mustDrain()
	e.srv.SetDown(true)
	if res, err := livesession.Abort(t.Context(), e.cfg, e.dir, testBudget); err != nil || !res.Queued {
		t.Fatalf("abort offline: %+v %v", res, err)
	}
	e.srv.SetDown(false)
	e.srv.RefuseDelete(true)

	if n, err := livesession.RetryPendingDeletes(t.Context(), e.cfg); n != 0 || err != nil {
		t.Fatalf("retry refused: n=%d err=%v, want nothing confirmed", n, err)
	}
	if q := pendingDeletes(t, e.cfg); len(q) != 1 || !q[0].Refused || q[0].Reason != "forbidden" {
		t.Fatalf("queue = %+v, want the refusal kept, not dropped", q)
	}
}

// Without its signing key this machine can never delete the session; that is
// a case for the person, not a success.
func TestAbortWithoutSigningKeyIsRefusedNotDeleted(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 3))
	e.mustDrain()
	keys, _ := filepath.Glob(filepath.Join(e.cfg.KeyDir, "*.key"))
	for _, k := range keys {
		if err := os.Remove(k); err != nil {
			t.Fatal(err)
		}
	}

	res, err := livesession.Abort(t.Context(), e.cfg, e.dir, testBudget)
	if err != nil || res.Deleted || !res.Refused || res.Reason != "no_signing_key" {
		t.Fatalf("abort without a key: %+v %v, want refused (no_signing_key)", res, err)
	}
	if q := pendingDeletes(t, e.cfg); len(q) != 1 || !q[0].Refused {
		t.Fatalf("queue = %+v, want the request kept as refused", q)
	}
}

// A drain can hold live.lock far longer than abort's budget. The delete must
// be durable before abort waits for it, and survive the caller removing the
// session folder (C2, C10).
func TestAbortRecordsTheDeleteBeforeWaitingForTheSession(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 3))
	e.mustDrain()
	e.srv.SetDown(true)

	held, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- fileutil.WithFileLock(t.Context(), livesession.LockTarget(e.dir), func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	start := time.Now()
	res, err := livesession.Abort(t.Context(), e.cfg, e.dir, 100*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("abort waited %s behind the drain", elapsed)
	}
	if err != nil || !res.Queued {
		close(release)
		t.Fatalf("abort while a drain held the session: %+v %v, want the delete queued", res, err)
	}
	if q := pendingDeletes(t, e.cfg); len(q) != 1 || q[0].ConversationID != e.st.ConversationID {
		close(release)
		t.Fatalf("queue = %+v, want the delete recorded while the lock was still held", q)
	}
	// nothing may spool into the aborted session, even before it is wiped
	if _, err := livesession.Spool(t.Context(), e.dir, lines(9, 10), livesession.SpoolOptions{}); !errors.Is(err, livesession.ErrNotStreaming) {
		close(release)
		t.Fatalf("spool into an aborted session: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(e.dir); err != nil { // the caller's local abort
		t.Fatal(err)
	}
	e.srv.SetDown(false)
	if n, err := livesession.RetryPendingDeletes(t.Context(), e.cfg); err != nil || n != 1 || !e.srv.IsDeleted(e.st.ConversationID) {
		t.Fatalf("daemon retry: n=%d err=%v deleted=%v", n, err, e.srv.IsDeleted(e.st.ConversationID))
	}
}

// An open whose reply was lost leaves the session pending locally while the
// server holds the conversation. Abort must still delete it there.
func TestAbortDeletesAPendingSessionWhoseOpenReplyWasLost(t *testing.T) {
	srv, cfg, dir, p := prepared(t)
	if _, err := livesession.Spool(t.Context(), dir, lines(0, 2), livesession.SpoolOptions{}); err != nil {
		t.Fatal(err)
	}
	srv.DropOpenReply(1)
	if _, err := livesession.Open(t.Context(), cfg, dir, p); err == nil {
		t.Fatal("open succeeded though its reply was lost")
	}
	st, err := livesession.ReadState(dir)
	if err != nil || st.Status != livesession.StatusPending || len(srv.Layers(st.ConversationID)) == 0 {
		t.Fatalf("setup: state=%+v err=%v layers=%v, want pending locally and opened on the server", st, err, srv.Layers(st.ConversationID))
	}

	res, err := livesession.Abort(t.Context(), cfg, dir, testBudget)
	if err != nil || !res.Deleted || !srv.IsDeleted(st.ConversationID) {
		t.Fatalf("abort: %+v %v deleted=%v, want the server copy deleted", res, err, srv.IsDeleted(st.ConversationID))
	}
}

// Everything the stream writes into a session folder is machine-local and
// must stay out of the Ledger; IsLocalFile is what the stager skips (C5).
func TestIsLocalFileCoversEveryFileTheStreamWrites(t *testing.T) {
	srv, cfg, dir, p := prepared(t)
	seen := map[string]bool{}
	snapshot := func() {
		t.Helper()
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), ".tmp-") { // AtomicWrite's own temp files
				seen[e.Name()] = true
			}
		}
	}
	if _, err := livesession.Spool(t.Context(), dir, lines(0, 2), livesession.SpoolOptions{}); err != nil {
		t.Fatal(err)
	}
	snapshot()
	if _, err := livesession.Open(t.Context(), cfg, dir, p); err != nil {
		t.Fatal(err)
	}
	srv.SetDown(true) // leaves the close requested
	_, _ = livesession.Close(t.Context(), cfg, dir, "", testBudget)
	snapshot()
	_, _ = livesession.Abort(t.Context(), cfg, dir, testBudget)
	snapshot()

	for _, want := range []string{"spool.jsonl", "stream-state.json", "close-requested", "delete-requested"} {
		if !seen[want] {
			t.Errorf("the stream never wrote %s; the test no longer exercises it", want)
		}
	}
	for name := range seen {
		if !livesession.IsLocalFile(name) {
			t.Errorf("%s is written by the live stream but not reported by IsLocalFile, so it would reach the Ledger", name)
		}
	}
	if livesession.IsLocalFile("raw.jsonl") {
		t.Error("raw.jsonl is session content, not a live-stream file")
	}
}

// waitFor polls cond until it holds, failing the test after a few seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// An abort that lands while the daemon is still registering the key for an
// open: the open must never be sent, since abort saw nothing to delete.
func TestAbortWhileTheOpenRegistersItsKeyStopsTheOpen(t *testing.T) {
	srv, cfg, dir, p := prepared(t)
	srv.Latency(300 * time.Millisecond)
	opened := make(chan error, 1)
	go func() { _, err := livesession.Open(t.Context(), cfg, dir, p); opened <- err }()
	waitFor(t, "the key registration", func() bool { return srv.InFlight() > 0 })

	res, err := livesession.Abort(t.Context(), cfg, dir, 2*time.Second)
	if err != nil || res != (livesession.AbortResult{}) {
		t.Fatalf("abort: %+v %v, want nothing to delete yet", res, err)
	}
	if err := <-opened; err != nil {
		t.Fatalf("open: %v", err)
	}
	st, _ := livesession.ReadState(dir)
	if n := srv.CallsTo("PUT", st.ConversationID); n != 0 || len(srv.Layers(st.ConversationID)) != 0 {
		t.Fatalf("the open was sent after the abort (%d PUTs)", n)
	}
}

// An abort whose DELETE reaches the server before an open already on its way
// sees nothing to delete; the open then creates the conversation. The open
// must queue that delete itself, or the copy stays on SageOx.
func TestAbortThatOutrunsAnInFlightOpenStillDeletesTheConversation(t *testing.T) {
	srv, cfg, dir, p := prepared(t)
	srv.DelayOpen(300 * time.Millisecond)
	opened := make(chan error, 1)
	go func() { _, err := livesession.Open(t.Context(), cfg, dir, p); opened <- err }()
	waitFor(t, "the open in flight", func() bool {
		st, err := livesession.ReadState(dir)
		return err == nil && st.OpenSent && srv.InFlight() > 0
	})
	st, _ := livesession.ReadState(dir)

	if res, err := livesession.Abort(t.Context(), cfg, dir, 2*time.Second); err != nil || !res.Deleted {
		t.Fatalf("abort: %+v %v", res, err)
	}
	if err := <-opened; err != nil {
		t.Fatalf("open: %v", err)
	}
	if len(srv.Layers(st.ConversationID)) == 0 {
		t.Fatal("test setup: the open never reached the server")
	}
	if _, err := livesession.RetryPendingDeletes(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if !srv.IsDeleted(st.ConversationID) {
		t.Fatal("the conversation the racing open created was never deleted")
	}
}
