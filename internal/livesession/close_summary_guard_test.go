package livesession_test

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionid"
	"github.com/sageox/ox/internal/sessionstream"
)

// seededContinuation prepares a reopen of s in <sessions>/second with a prompt
// spooled, and returns its folder, recording id and header line.
func (s *stoppedSession) seededContinuation(t *testing.T) (dir, id string, header []byte) {
	t.Helper()
	dir = filepath.Join(s.sessions, "second")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	header = []byte(`{"type":"header","metadata":{"session_id":"second"}}` + "\n")
	if err := os.WriteFile(filepath.Join(dir, "raw.jsonl"), header, 0o600); err != nil {
		t.Fatal(err)
	}
	id = sessionid.GenerateSessionID()
	if _, err := livesession.PrepareReopen(t.Context(), s.cfg, dir, livesession.OpenParams{SessionID: id, RepoID: "repo_test", Agent: "claude-code"}, s.source(t, s.st.SessionID)); err != nil {
		t.Fatal(err)
	}
	if _, err := livesession.Spool(t.Context(), dir, withPrompt(lines(10, 12)), livesession.SpoolOptions{}); err != nil {
		t.Fatal(err)
	}
	return dir, id, header
}

// Failure prevented: a stop summarized the seeded spool (the earlier
// recording's lines first), then the server refused the reopen, here for a
// conversation deleted on SageOx. The fallback dropped those lines from the
// stream, but finish still published their summary under the replacement.
func TestRefusedReopenNeverClosesWithTheSeedsSummary(t *testing.T) {
	t.Run("refused inside the close", func(t *testing.T) {
		s := newStopped(t)
		s.stop(t)
		dir, id, header := s.seededContinuation(t)
		s.srv.RefuseNextReopen(http.StatusGone, sessionstream.ReasonSessionDeleted, sessionstream.ActionStopAndWipe)

		if res, err := livesession.Close(t.Context(), s.cfg, dir, "SUMMARY-QUOTING-THE-DELETED-SESSION", testBudget); err != nil || !res.Done {
			t.Fatalf("close: %+v %v", res, err)
		}
		own, _ := livesession.ConversationID(id)
		closed, ok := s.srv.ClosedWith(own)
		if !ok {
			t.Fatal("the replacement conversation was not closed")
		}
		if closed.Summary != "" {
			t.Fatalf("the replacement closed with %q, a summary of lines it no longer carries", closed.Summary)
		}
		if got := s.srv.Stored(t, own, closed.LayerID); !bytes.Equal(got, append(append([]byte(nil), header...), withPrompt(lines(10, 12))...)) {
			t.Fatalf("replacement holds %q, want its own lines only", got)
		}
	})
	t.Run("refused before the close, after the summary", func(t *testing.T) {
		s := newStopped(t)
		s.stop(t)
		dir, id, _ := s.seededContinuation(t)
		// the stop read the stream and summarized it; meanwhile the daemon's
		// open was refused and fell back
		written := livesession.Summary{Text: "SUMMARY-QUOTING-THE-DELETED-SESSION", ConversationID: s.st.ConversationID}
		s.srv.RefuseNextReopen(http.StatusGone, sessionstream.ReasonSessionDeleted, sessionstream.ActionStopAndWipe)
		if _, err := livesession.Open(t.Context(), s.cfg, dir, livesession.OpenParams{SessionID: id}); err != nil {
			t.Fatal(err)
		}
		if res, err := livesession.CloseOf(t.Context(), s.cfg, dir, id, written, testBudget); err != nil || !res.Done {
			t.Fatalf("close: %+v %v", res, err)
		}
		own, _ := livesession.ConversationID(id)
		if closed, _ := s.srv.ClosedWith(own); closed.Summary != "" {
			t.Fatalf("the replacement closed with %q, a summary written for the refused conversation", closed.Summary)
		}
	})
	t.Run("a summary written for the stream it closes is sent", func(t *testing.T) {
		s := newStopped(t)
		s.stop(t)
		dir, id, _ := s.seededContinuation(t)
		written := livesession.Summary{Text: "SUMMARY-OF-THE-RESTATED-SESSION", ConversationID: s.st.ConversationID}
		if res, err := livesession.CloseOf(t.Context(), s.cfg, dir, id, written, testBudget); err != nil || !res.Done {
			t.Fatalf("close: %+v %v", res, err)
		}
		if closed, _ := s.srv.ClosedWith(s.st.ConversationID); closed.Summary != written.Text {
			t.Fatalf("close carried %q, want the summary", closed.Summary)
		}
	})
}

// Failure prevented: a hold placed after the closer's own check (the daemon
// checks before its minute-long summary) still let finish send the summary.
// The close re-checks under live.lock, which the hold writer takes too.
func TestHeldStreamIsNeverClosedWithASummary(t *testing.T) {
	e := newEnv(t)
	e.spool(withPrompt(lines(0, 2)))
	e.mustDrain()
	if err := session.WriteHoldMarker(e.dir, session.HoldManualPublishing, "test"); err != nil {
		t.Fatal(err)
	}

	res, err := livesession.Close(t.Context(), e.cfg, e.dir, "SUMMARY-OF-A-HELD-SESSION", testBudget)
	if err != nil || res.Done {
		t.Fatalf("close of a held stream = %+v %v, want it left unfinished", res, err)
	}
	if _, closed := e.srv.ClosedWith(e.st.ConversationID); closed {
		t.Fatal("a held stream was closed with its summary")
	}
	if st := e.state(); st.Status != livesession.StatusClosing {
		t.Fatalf("status = %s, want closing: the daemon's held stop deletes the live copy", st.Status)
	}
	// what the daemon's next step does with it (stopHeld)
	if _, err := livesession.Abort(t.Context(), e.cfg, e.dir, testBudget); err != nil {
		t.Fatal(err)
	}
	if !e.srv.IsDeleted(e.st.ConversationID) {
		t.Fatal("the held stream's live copy was not deleted")
	}
}
