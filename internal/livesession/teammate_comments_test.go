package livesession_test

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
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
