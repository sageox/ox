package livesession_test

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/livesession/livesessiontest"
	"github.com/sageox/ox/internal/sessionid"
	"github.com/sageox/ox/internal/sessionstream"
)

func comment(id, body string) sessionstream.TeammateComment {
	return sessionstream.TeammateComment{ID: id, AuthorName: "Person A", Body: body, CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
}

func (e *env) spoolAndPrompt() {
	e.spool(withPrompt(lines(0, 3)))
}

// The server re-sends every undelivered comment on each append; a drain that
// spans several appends must hand each one over once, in order.
func TestDrainTeammateCommentsDedupedAcrossAppendsInOrder(t *testing.T) {
	e := newEnv(t)
	e.srv.QueueTeammateComment(comment("tcm_1", "first"))
	e.srv.QueueTeammateComment(comment("tcm_2", "second"))
	e.spool(withPrompt(lines(0, 700))) // ~700 KB: several appends, each re-sending both

	res := e.mustDrain()
	if n := e.srv.CallsTo("POST", "/lines"); n < 3 {
		t.Fatalf("setup: %d appends, want several", n)
	}
	if len(res.TeammateComments) != 2 || res.TeammateComments[0].ID != "tcm_1" || res.TeammateComments[1].ID != "tcm_2" {
		t.Fatalf("comments = %+v, want tcm_1 then tcm_2 once each", res.TeammateComments)
	}
}

func TestTeammateCommentAckSentOnceWithPostCaptureSeq(t *testing.T) {
	e := newEnv(t)
	e.spoolAndPrompt()
	e.mustDrain()
	if err := livesession.RecordTeammateCommentAck(e.dir, "tcm_1", 42); err != nil {
		t.Fatal(err)
	}
	e.mustDrain()
	e.mustDrain() // already sent: must not go again

	reqs := e.srv.AckRequests()
	if len(reqs) != 1 || len(reqs[0].Acks) != 1 || reqs[0].Acks[0].ID != "tcm_1" || reqs[0].Acks[0].DeliveredAtSeq != 42 {
		t.Fatalf("ack requests = %+v, want exactly one for tcm_1 at seq 42", reqs)
	}
	if reqs[0].LayerID != e.state().LayerID {
		t.Fatalf("ack layer = %q, want the stream's layer %q", reqs[0].LayerID, e.state().LayerID)
	}
	if seq, ok := e.srv.DeliveredAtSeq("tcm_1"); !ok || seq != 42 {
		t.Fatalf("server delivered_at_seq = %d, %v", seq, ok)
	}
}

func TestTeammateCommentAckSurvivesRetryableRefusalAndNeverFailsDrain(t *testing.T) {
	e := newEnv(t)
	e.spoolAndPrompt()
	e.mustDrain()
	if err := livesession.RecordTeammateCommentAck(e.dir, "tcm_1", 7); err != nil {
		t.Fatal(err)
	}
	e.srv.RefuseNextAck(http.StatusServiceUnavailable, sessionstream.ReasonUnavailable, sessionstream.ActionRetry)
	if _, err := e.drain(); err != nil {
		t.Fatalf("a refused ack failed the byte drain: %v", err)
	}
	if _, ok := e.srv.DeliveredAtSeq("tcm_1"); ok {
		t.Fatal("setup: the refused ack was recorded")
	}
	e.mustDrain() // next drain retries it
	if seq, ok := e.srv.DeliveredAtSeq("tcm_1"); !ok || seq != 7 {
		t.Fatalf("ack not retried after a retryable refusal: %d, %v", seq, ok)
	}
}

func TestTeammateCommentAckPermanentRefusalIsDroppedNotRetriedForever(t *testing.T) {
	e := newEnv(t)
	e.spoolAndPrompt()
	e.mustDrain()
	if err := livesession.RecordTeammateCommentAck(e.dir, "tcm_bad", 7); err != nil {
		t.Fatal(err)
	}
	e.srv.RefuseNextAck(http.StatusBadRequest, sessionstream.ReasonInvalidRequest, sessionstream.ActionFix)
	e.mustDrain()
	e.mustDrain()
	if n := e.srv.CallsTo("POST", "/teammate-comments/acks"); n != 1 {
		t.Fatalf("ack requests = %d, want 1: a fix refusal must not be retried", n)
	}
}

func TestTeammateCommentAcksBatchedAtWireCap(t *testing.T) {
	e := newEnv(t)
	e.spoolAndPrompt()
	e.mustDrain()
	total := sessionstream.MaxTeammateCommentAcks + 3
	for i := range total {
		if err := livesession.RecordTeammateCommentAck(e.dir, "tcm_"+string(rune('a'+i)), int64(i+1)); err != nil {
			t.Fatal(err)
		}
	}
	e.mustDrain()
	reqs := e.srv.AckRequests()
	if len(reqs) != 2 || len(reqs[0].Acks) != sessionstream.MaxTeammateCommentAcks || len(reqs[1].Acks) != 3 {
		t.Fatalf("requests = %d (sizes %v), want %d then 3", len(reqs), len(reqs), sessionstream.MaxTeammateCommentAcks)
	}
}

// The ack logs are machine-local: copying them into the Ledger would publish
// which teammate notes reached which turn.
func TestTeammateCommentAckFilesAreLocalOnly(t *testing.T) {
	for _, name := range []string{"teammate-comment-acks", "teammate-comment-acks-sent"} {
		if !livesession.IsLocalFile(name) {
			t.Errorf("%s would be copied into the Ledger", name)
		}
	}
}

func TestTeammateCommentAckRejectsUnusableRecords(t *testing.T) {
	dir := t.TempDir()
	if livesession.RecordTeammateCommentAck(dir, "", 3) == nil || livesession.RecordTeammateCommentAck(dir, "x", 0) == nil {
		t.Fatal("an ack with no id or seq 0 was accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "teammate-comment-acks")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a rejected ack still wrote the log: %v", err)
	}
}

// Failure prevented: an ack the server answered but never recorded leaves its
// note listed forever; after five of them the append's slots are all taken.
func TestTeammateCommentLostAckIsResentWhenTheServerListsTheNoteAgain(t *testing.T) {
	e := newEnv(t)
	e.srv.QueueTeammateComment(comment("tcm_1", "once"))
	e.spool(withPrompt(lines(0, 3)))
	first := e.mustDrain()
	if len(first.TeammateComments) != 1 {
		t.Fatalf("setup: first drain returned %v", first.TeammateComments)
	}
	if err := livesession.RecordTeammateCommentAck(e.dir, "tcm_1", 5); err != nil {
		t.Fatal(err)
	}
	e.srv.IgnoreNextAcks(1)
	e.mustDrain() // the ack is "sent" and marked settled, yet the server recorded nothing
	if _, ok := e.srv.DeliveredAtSeq("tcm_1"); ok {
		t.Fatal("setup: the server recorded the ack")
	}

	e.spool(lines(3, 6))
	again := e.mustDrain() // the append lists tcm_1 again
	if len(again.TeammateComments) != 0 {
		t.Fatalf("a note already shown was returned for display again: %v", again.TeammateComments)
	}
	if seq, ok := e.srv.DeliveredAtSeq("tcm_1"); !ok || seq != 5 {
		t.Fatalf("server delivered_at_seq = %d, %v; want the original 5", seq, ok)
	}
	e.spool(lines(6, 9))
	if third := e.mustDrain(); len(third.TeammateComments) != 0 {
		t.Fatalf("the slot did not free: %v", third.TeammateComments)
	}
	if n := len(e.srv.AckRequests()); n != 2 {
		t.Fatalf("ack requests = %d, want 2 (the lost one and one resend, then none)", n)
	}
}

// Failure prevented (round 3): an open refused because another person owns the
// session name retried every tick forever.
func TestOpenRefusedAsSessionNameTakenEndsTheStreamInsteadOfRetrying(t *testing.T) {
	srv := livesessiontest.New(t)
	srv.RefuseOpensAsNameTaken(true)
	cfg, dir := srv.Config(t), t.TempDir()
	params := livesession.OpenParams{SessionID: sessionid.GenerateSessionID(), SessionName: "2026-03-04T13-41-person-a-Oxabc1", RepoID: "repo_test", Agent: "claude-code", StartedAt: time.Now()}
	if _, err := livesession.Prepare(t.Context(), cfg, dir, params); err != nil {
		t.Fatal(err)
	}
	if _, err := livesession.Spool(t.Context(), dir, prompt(), livesession.SpoolOptions{}); err != nil {
		t.Fatal(err)
	}
	st, err := livesession.Open(t.Context(), cfg, dir, params)
	if err != nil || st.Status != livesession.StatusDisabled || st.DisabledReason != sessionstream.ReasonSessionNameTaken {
		t.Fatalf("open = %+v, %v; want a disabled stream", st, err)
	}
	if on, _ := livesession.ReadState(dir); on.Status != livesession.StatusDisabled {
		t.Fatalf("on-disk status = %s, want disabled", on.Status)
	}
}

// The wire flag, not "an append succeeded", says the list is complete.
func TestDrainReportsAListOnlyWhenTheServerSaidItLooked(t *testing.T) {
	e := newEnv(t)
	e.srv.QueueTeammateComment(comment("tcm_1", "x"))
	e.srv.FailTeammateLookup(true)
	e.spool(withPrompt(lines(0, 3)))
	if res := e.mustDrain(); res.TeammateCommentsListed || len(res.TeammateComments) != 0 {
		t.Fatalf("an unlisted answer was reported as a list: %+v", res)
	}
}

// Failure prevented (N2): a resend obligation lived only in memory, so a 503 on
// the resend was never retried by a drain with no new spool bytes.
func TestTeammateCommentResendRefusedWith503IsRetriedWithNoNewSpoolBytes(t *testing.T) {
	e := newEnv(t)
	e.srv.QueueTeammateComment(comment("tcm_1", "once"))
	e.spool(withPrompt(lines(0, 3)))
	e.mustDrain()
	if err := livesession.RecordTeammateCommentAck(e.dir, "tcm_1", 5); err != nil {
		t.Fatal(err)
	}
	e.srv.IgnoreNextAcks(1)
	e.mustDrain() // "sent", settled, never recorded by the server

	e.spool(lines(3, 6))
	e.srv.RefuseAcks(1, http.StatusServiceUnavailable, sessionstream.ReasonUnavailable, sessionstream.ActionRetry)
	e.mustDrain() // the append lists tcm_1 again; the resend meets a 503
	if _, ok := e.srv.DeliveredAtSeq("tcm_1"); ok {
		t.Fatal("setup: the refused resend was recorded")
	}
	e.mustDrain() // nothing new was spooled
	if seq, ok := e.srv.DeliveredAtSeq("tcm_1"); !ok || seq != 5 {
		t.Fatalf("delivered_at_seq = %d, %v; the resend must be retried with the first seq", seq, ok)
	}
}

// Failure prevented (N2): a receipt owed when the owner stops the session never
// sent, because the sweep skips closed streams and Close sent none.
func TestCloseSendsTheAcksStillOwed(t *testing.T) {
	e := newEnv(t)
	e.spool(withPrompt(lines(0, 3)))
	e.mustDrain()
	if err := livesession.RecordTeammateCommentAck(e.dir, "tcm_1", 5); err != nil {
		t.Fatal(err)
	}
	if res, err := livesession.Close(t.Context(), e.cfg, e.dir, "", testBudget); err != nil || !res.Done {
		t.Fatalf("close = %+v, %v", res, err)
	}
	if seq, ok := e.srv.DeliveredAtSeq("tcm_1"); !ok || seq != 5 {
		t.Fatalf("delivered_at_seq = %d, %v; Close must flush the owed ack", seq, ok)
	}
}

// After a close that could not deliver the ack, the daemon's flush keeps trying
// until the server answers.
func TestFlushAfterCloseDeliversAnAckTheCloseCouldNot(t *testing.T) {
	e := newEnv(t)
	e.spool(withPrompt(lines(0, 3)))
	e.mustDrain()
	if err := livesession.RecordTeammateCommentAck(e.dir, "tcm_1", 5); err != nil {
		t.Fatal(err)
	}
	e.srv.RefuseAcks(1, http.StatusServiceUnavailable, sessionstream.ReasonUnavailable, sessionstream.ActionRetry)
	if res, err := livesession.Close(t.Context(), e.cfg, e.dir, "", testBudget); err != nil || !res.Done {
		t.Fatalf("close = %+v, %v", res, err)
	}
	if !acksOwed(t, e) {
		t.Fatal("setup: the refused ack is no longer owed")
	}
	if _, err := livesession.FlushTeammateAcks(t.Context(), e.cfg, e.dir, testBudget, time.Now()); err != nil {
		t.Fatal(err)
	}
	if seq, ok := e.srv.DeliveredAtSeq("tcm_1"); !ok || seq != 5 || acksOwed(t, e) {
		t.Fatalf("flush delivered %d, %v; owed=%v", seq, ok, acksOwed(t, e))
	}
}

// The retry is bounded: after a day of failures the obligation is given up once,
// with one log line, and the session leaves the sweep.
func TestFlushGivesUpAfterADayAndLogsOnce(t *testing.T) {
	e := newEnv(t)
	var logs bytes.Buffer
	e.cfg.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	e.spool(withPrompt(lines(0, 3)))
	e.mustDrain()
	if err := livesession.RecordTeammateCommentAck(e.dir, "tcm_1", 5); err != nil {
		t.Fatal(err)
	}
	e.srv.RefuseAcks(100, http.StatusServiceUnavailable, sessionstream.ReasonUnavailable, sessionstream.ActionRetry)
	if _, err := livesession.Close(t.Context(), e.cfg, e.dir, "", testBudget); err != nil {
		t.Fatal(err)
	}
	soon := time.Now().Add(time.Hour)
	if _, err := livesession.FlushTeammateAcks(t.Context(), e.cfg, e.dir, testBudget, soon); err != nil || !acksOwed(t, e) {
		t.Fatalf("an hour of failures gave up (owed=%v, err=%v)", acksOwed(t, e), err)
	}
	late := time.Now().Add(25 * time.Hour)
	for range 3 {
		if _, err := livesession.FlushTeammateAcks(t.Context(), e.cfg, e.dir, testBudget, late); err != nil {
			t.Fatal(err)
		}
	}
	if acksOwed(t, e) {
		t.Fatal("still owed after 24 hours")
	}
	if n := strings.Count(logs.String(), "live_teammate_acks_given_up"); n != 1 {
		t.Fatalf("give-up logged %d times, want once", n)
	}
}

func acksOwed(t *testing.T, e *env) bool {
	t.Helper()
	owed, err := livesession.TeammateAcksOwed(t.Context(), e.cfg, e.dir)
	if err != nil {
		t.Fatalf("owed: %v", err)
	}
	return owed
}

func closedEnvWithAck(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.spool(withPrompt(lines(0, 3)))
	e.mustDrain()
	if err := livesession.RecordTeammateCommentAck(e.dir, "tcm_1", 5); err != nil {
		t.Fatal(err)
	}
	e.srv.RefuseAcks(100, http.StatusServiceUnavailable, sessionstream.ReasonUnavailable, sessionstream.ActionRetry)
	if _, err := livesession.Close(t.Context(), e.cfg, e.dir, "", testBudget); err != nil {
		t.Fatal(err)
	}
	return e
}

// Failure prevented (CIcL): the retry period measured from the delivery log's
// last write, so a note acknowledged long ago and listed again today was given
// up on the first failed resend. Each receipt counts from when IT began to be owed.
func TestFlushRetryPeriodStartsWhenTheResendBegins(t *testing.T) {
	e := newEnv(t)
	e.spool(withPrompt(lines(0, 3)))
	e.mustDrain()
	old := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339Nano)
	logged := `{"id":"tcm_1","delivered_at_seq":5,"at":"` + old + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(e.dir, "teammate-comment-acks"), []byte(logged), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.dir, "teammate-comment-acks-sent"), []byte("tcm_1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// the server lists it again now: a new obligation begins now
	resend := "tcm_1\t" + strconv.FormatInt(time.Now().Unix(), 10) + "\n"
	if err := os.WriteFile(filepath.Join(e.dir, "teammate-comment-resends"), []byte(resend), 0o600); err != nil {
		t.Fatal(err)
	}
	e.srv.RefuseAcks(100, http.StatusServiceUnavailable, sessionstream.ReasonUnavailable, sessionstream.ActionRetry)
	if _, err := livesession.Close(t.Context(), e.cfg, e.dir, "", testBudget); err != nil {
		t.Fatal(err)
	}
	if _, err := livesession.FlushTeammateAcks(t.Context(), e.cfg, e.dir, testBudget, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !acksOwed(t, e) {
		t.Fatal("a resend that began just now was given up because the old delivery was a day old")
	}
	if _, err := livesession.FlushTeammateAcks(t.Context(), e.cfg, e.dir, testBudget, time.Now().Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if acksOwed(t, e) {
		t.Fatal("the resend was not given up 25 hours after it began")
	}
}

// Failure prevented (R4): a receipt log from someone else read before the stream
// was authorized, or parsed without bound. Authorization comes first; past the
// bound a log is refused, with an error, never partly trusted.
func TestFlushAuthorizesBeforeReadingAndBoundsTheLog(t *testing.T) {
	e := closedEnvWithAck(t)
	huge := bytes.Repeat([]byte(`{"id":"x","delivered_at_seq":1}`+"\n"), 20000)
	if err := os.WriteFile(filepath.Join(e.dir, "teammate-comment-resends"), huge, 0o600); err != nil {
		t.Fatal(err)
	}

	stranger := e.cfg
	stranger.DataDir = t.TempDir() // this machine never minted the stream
	if _, err := livesession.FlushTeammateAcks(t.Context(), stranger, e.dir, testBudget, time.Now()); !errors.Is(err, livesession.ErrUntrustedState) {
		t.Fatalf("unminted stream: err = %v; want ErrUntrustedState before any log is read", err)
	}
	if _, err := livesession.TeammateAcksOwed(t.Context(), stranger, e.dir); !errors.Is(err, livesession.ErrUntrustedState) {
		t.Fatalf("TeammateAcksOwed on an unminted stream: err = %v", err)
	}
	if _, err := livesession.FlushTeammateAcks(t.Context(), e.cfg, e.dir, testBudget, time.Now()); !errors.Is(err, livesession.ErrAckLogTooLarge) {
		t.Fatalf("oversized log: err = %v; want ErrAckLogTooLarge", err)
	}
}

// Failure prevented (CIb-): an unreadable receipt log reading as "nothing owed".
func TestAnUnreadableReceiptLogIsAnErrorNotAnEmptyQueue(t *testing.T) {
	e := closedEnvWithAck(t)
	if err := os.Mkdir(filepath.Join(e.dir, "teammate-comment-resends"), 0o700); err != nil {
		t.Fatal(err)
	}
	owed, err := livesession.TeammateAcksOwed(t.Context(), e.cfg, e.dir)
	if err == nil || owed {
		t.Fatalf("owed = %v, err = %v; an unreadable log must be reported", owed, err)
	}
}
