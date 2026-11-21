package livesession_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionid"
)

// A person exits the agent and resumes it seconds later: the next recording
// starts while the daemon still writes the stopped stream's summary, before its
// close. These tests stand in for the daemon with that summary deliberately
// blocked: the stream's close is requested (the SessionEnd hook) and Close runs
// only once the test releases it.

// closing streams s, then asks for its close the way the SessionEnd hook does,
// leaving the close itself to a blocked summarizer.
func (s *stoppedSession) closing(t *testing.T) {
	t.Helper()
	if _, err := livesession.Open(t.Context(), s.cfg, s.dir, livesession.OpenParams{SessionID: s.st.SessionID}); err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := livesession.Drain(t.Context(), s.cfg, s.dir, testBudget); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if err := livesession.RequestClose(s.dir); err != nil {
		t.Fatalf("request close: %v", err)
	}
}

// summarizer is the daemon's close of s, held until release.
type summarizer struct {
	release chan struct{}
	done    chan error
}

func (s *stoppedSession) blockedSummarizer(t *testing.T, summary string) *summarizer {
	t.Helper()
	b := &summarizer{release: make(chan struct{}), done: make(chan error, 1)}
	go func() {
		<-b.release
		res, err := livesession.Close(t.Context(), s.cfg, s.dir, summary, testBudget)
		if err == nil && !res.Done {
			err = errors.New("close did not finish")
		}
		b.done <- err
	}()
	return b
}

func (b *summarizer) finish(t *testing.T) {
	t.Helper()
	close(b.release)
	if err := <-b.done; err != nil {
		t.Fatalf("the awaited close: %v", err)
	}
}

type continuation struct {
	dir, id, cnv string
	header       []byte
}

// continueWaiting starts the recording that continues s in its own folder,
// exactly as session start does for a source still closing: Prepare, the
// header spooled, then the wait.
func (s *stoppedSession) continueWaiting(t *testing.T, src *livesession.ReopenSource) continuation {
	t.Helper()
	c := continuation{dir: filepath.Join(s.sessions, "second"), id: sessionid.GenerateSessionID()}
	c.cnv, _ = livesession.ConversationID(c.id)
	c.header = []byte(`{"type":"header","metadata":{"session_id":"second"}}` + "\n")
	if err := os.Mkdir(c.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.dir, "raw.jsonl"), c.header, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := livesession.Prepare(t.Context(), s.cfg, c.dir, livesession.OpenParams{
		SessionID: c.id, SessionName: "second-name", RepoID: "repo_test", Agent: "claude-code", StartedAt: time.Now(),
		ContinuedFromSessionID: s.st.SessionID,
	}); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := livesession.Spool(t.Context(), c.dir, c.header, livesession.SpoolOptions{}); err != nil {
		t.Fatalf("spool header: %v", err)
	}
	st, err := livesession.AwaitReopen(t.Context(), s.cfg, c.dir, c.id, src)
	if err != nil || st.AwaitReopen == nil || st.ConversationID != c.cnv {
		t.Fatalf("await reopen: %+v %v, want the recording's own conversation, waiting", st, err)
	}
	return c
}

func (s *stoppedSession) closingSource(t *testing.T) *livesession.ReopenSource {
	t.Helper()
	src := s.source(t, s.st.SessionID)
	if src == nil || !src.Pending || src.State.ConversationID != s.st.ConversationID {
		t.Fatalf("source of a stream whose close is in flight = %+v, want it pending", src)
	}
	return src
}

func TestQuickResumeWaitsForTheStoppedCloseThenReopensItsConversation(t *testing.T) {
	s := newStopped(t)
	s.closing(t)
	daemon := s.blockedSummarizer(t, "SUMMARY-OF-THE-FIRST")
	c := s.continueWaiting(t, s.closingSource(t))

	fresh := withPrompt(lines(10, 12))
	if _, err := livesession.Spool(t.Context(), c.dir, fresh, livesession.SpoolOptions{}); err != nil {
		t.Fatal(err)
	}
	// a prompt is there, but the stream may yet become a reopen: nothing opens
	st, err := livesession.Open(t.Context(), s.cfg, c.dir, livesession.OpenParams{SessionID: c.id})
	if err != nil || st.Status != livesession.StatusPending || st.AwaitReopen == nil {
		t.Fatalf("open while the stopped stream closes = %+v %v, want pending and waiting", st, err)
	}
	if _, err := livesession.Drain(t.Context(), s.cfg, c.dir, testBudget); err != nil {
		t.Fatalf("drain while waiting: %v", err)
	}
	if got := s.srv.CallsTo("PUT", c.cnv); got != 0 {
		t.Fatalf("the waiting recording opened its own conversation (%d opens)", got)
	}
	if got := s.srv.CallsTo("PUT", s.st.ConversationID); got != 1 {
		t.Fatalf("opens of the stopped conversation = %d, want its own one only", got)
	}

	daemon.finish(t)
	if closed, _ := s.srv.ClosedWith(s.st.ConversationID); closed.Summary != "SUMMARY-OF-THE-FIRST" {
		t.Fatalf("the awaited close carried %q, want its summary", closed.Summary)
	}

	st, err = livesession.Open(t.Context(), s.cfg, c.dir, livesession.OpenParams{SessionID: c.id})
	if err != nil {
		t.Fatalf("open after the awaited close: %v", err)
	}
	if st.ConversationID != s.st.ConversationID || st.AwaitReopen != nil || st.Status != livesession.StatusStreaming {
		t.Fatalf("state = %+v, want the stopped conversation, streaming", st)
	}
	opened, ok := s.srv.OpenedWith(s.st.ConversationID)
	if !ok || !opened.Reopen || opened.LayerID != st.LayerID || opened.SessionName != "first-name" {
		t.Fatalf("server saw %+v, want a reopen on the new layer under the conversation's own name", opened)
	}
	if _, err := livesession.Drain(t.Context(), s.cfg, c.dir, testBudget); err != nil {
		t.Fatalf("drain: %v", err)
	}
	// seed first, then this recording's lines, each exactly once; its own
	// header is gone, since the seed already states the session's
	want := append(append([]byte(nil), s.content...), fresh...)
	if got := s.srv.Stored(t, s.st.ConversationID, st.LayerID); !bytes.Equal(got, want) {
		t.Fatalf("new layer holds %q, want %q", got, want)
	}
	if got := s.srv.CallsTo("PUT", c.cnv); got != 0 {
		t.Fatalf("the recording's own conversation reached the server (%d opens)", got)
	}

	if res, err := livesession.Close(t.Context(), s.cfg, c.dir, "SUMMARY-OF-BOTH", testBudget); err != nil || !res.Done {
		t.Fatalf("close: %+v %v", res, err)
	}
	if closed, _ := s.srv.ClosedWith(s.st.ConversationID); closed.LayerID != st.LayerID || closed.Summary != "SUMMARY-OF-BOTH" {
		t.Fatalf("close = %+v, want the new layer with its summary", closed)
	}
}

func TestQuickResumeStreamsAsItsOwnConversationWhenTheWaitEnds(t *testing.T) {
	tests := []struct {
		name    string
		expired bool // the wait starts already past its deadline
		setup   func(t *testing.T, s *stoppedSession)
	}{
		{"manual stop", false, func(t *testing.T, s *stoppedSession) {
			if err := livesession.MarkManualStop(s.dir); err != nil {
				t.Fatal(err)
			}
			if _, err := livesession.Close(t.Context(), s.cfg, s.dir, "", testBudget); err != nil {
				t.Fatal(err)
			}
		}},
		{"deleted on SageOx", false, func(t *testing.T, s *stoppedSession) {
			s.srv.DeleteSession(s.st.ConversationID)
			_, _ = livesession.Close(t.Context(), s.cfg, s.dir, "", testBudget)
			if !livesession.WasDeletedOnServer(s.dir) {
				t.Fatal("setup: the close did not hear the delete")
			}
		}},
		{"held", false, func(t *testing.T, s *stoppedSession) {
			if err := session.WriteHoldMarker(s.dir, session.HoldManualPublishing, "test"); err != nil {
				t.Fatal(err)
			}
		}},
		{"gone", false, func(t *testing.T, s *stoppedSession) {
			if err := os.RemoveAll(s.dir); err != nil { // never closed, so never stashed
				t.Fatal(err)
			}
		}},
		{"deadline", true, func(*testing.T, *stoppedSession) {}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newStopped(t)
			s.closing(t)
			src := s.closingSource(t)
			if tt.expired {
				defer livesession.SetReopenAwaitWindow(-time.Second)()
			}
			c := s.continueWaiting(t, src)
			tt.setup(t, s)

			fresh := withPrompt(lines(10, 12))
			if _, err := livesession.Spool(t.Context(), c.dir, fresh, livesession.SpoolOptions{}); err != nil {
				t.Fatal(err)
			}
			st, err := livesession.Open(t.Context(), s.cfg, c.dir, livesession.OpenParams{SessionID: c.id})
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if st.ConversationID != c.cnv || st.Reopen || st.AwaitReopen != nil || st.Status != livesession.StatusStreaming {
				t.Fatalf("state = %+v, want the recording's own conversation, streaming", st)
			}
			if opened, _ := s.srv.OpenedWith(c.cnv); opened.Reopen {
				t.Fatalf("own conversation opened with reopen: %+v", opened)
			}
			if _, err := livesession.Drain(t.Context(), s.cfg, c.dir, testBudget); err != nil {
				t.Fatalf("drain: %v", err)
			}
			want := append(append([]byte(nil), c.header...), fresh...)
			if got := s.srv.Stored(t, c.cnv, st.LayerID); !bytes.Equal(got, want) {
				t.Fatalf("own conversation holds %q, want its header and lines only", got)
			}
			if layers := s.srv.Layers(s.st.ConversationID); len(layers) != 1 {
				t.Fatalf("awaited conversation layers = %v, want it left alone", layers)
			}
		})
	}
}

func TestStopWhileWaitingResolvesOnceThenCloses(t *testing.T) {
	t.Run("the awaited close is still running", func(t *testing.T) {
		s := newStopped(t)
		s.closing(t)
		daemon := s.blockedSummarizer(t, "")
		c := s.continueWaiting(t, s.closingSource(t))
		fresh := withPrompt(lines(10, 12))
		if _, err := livesession.Spool(t.Context(), c.dir, fresh, livesession.SpoolOptions{}); err != nil {
			t.Fatal(err)
		}
		// the stop never waits on another stream's summary
		if res, err := livesession.Close(t.Context(), s.cfg, c.dir, "OWN-SUMMARY", testBudget); err != nil || !res.Done {
			t.Fatalf("close: %+v %v", res, err)
		}
		closed, ok := s.srv.ClosedWith(c.cnv)
		if !ok || closed.Summary != "OWN-SUMMARY" {
			t.Fatalf("own conversation close = %+v %v, want it closed with its summary", closed, ok)
		}
		if got := s.srv.Stored(t, c.cnv, closed.LayerID); !bytes.Equal(got, append(append([]byte(nil), c.header...), fresh...)) {
			t.Fatalf("own conversation holds %q", got)
		}
		daemon.finish(t)
		if layers := s.srv.Layers(s.st.ConversationID); len(layers) != 1 {
			t.Fatalf("the awaited conversation gained layers %v from a stop that had resolved", layers)
		}
	})
	t.Run("the awaited close finished first", func(t *testing.T) {
		s := newStopped(t)
		s.closing(t)
		daemon := s.blockedSummarizer(t, "")
		c := s.continueWaiting(t, s.closingSource(t))
		fresh := withPrompt(lines(10, 12))
		if _, err := livesession.Spool(t.Context(), c.dir, fresh, livesession.SpoolOptions{}); err != nil {
			t.Fatal(err)
		}
		daemon.finish(t)
		// summarized before the stop reopened the conversation: of this
		// recording's lines only, so it is not the restated session's summary
		if res, err := livesession.Close(t.Context(), s.cfg, c.dir, "WRITTEN-BEFORE-THE-REOPEN", testBudget); err != nil || !res.Done {
			t.Fatalf("close: %+v %v", res, err)
		}
		st, _ := livesession.ReadState(c.dir)
		closed, _ := s.srv.ClosedWith(s.st.ConversationID)
		if st.ConversationID != s.st.ConversationID || closed.LayerID != st.LayerID {
			t.Fatalf("state %+v, close %+v: want the reopened conversation closed on the new layer", st, closed)
		}
		if closed.Summary != "" {
			t.Fatalf("the close sent %q, a summary written for another conversation", closed.Summary)
		}
		if got := s.srv.Stored(t, s.st.ConversationID, st.LayerID); !bytes.Equal(got, append(append([]byte(nil), s.content...), fresh...)) {
			t.Fatalf("reopened layer holds %q", got)
		}
	})
	t.Run("a stop resolves before it summarizes", func(t *testing.T) {
		s := newStopped(t)
		s.closing(t)
		daemon := s.blockedSummarizer(t, "")
		c := s.continueWaiting(t, s.closingSource(t))
		if _, err := livesession.Spool(t.Context(), c.dir, withPrompt(lines(10, 12)), livesession.SpoolOptions{}); err != nil {
			t.Fatal(err)
		}
		daemon.finish(t)
		st, err := livesession.ResolveReopen(t.Context(), s.cfg, c.dir, testBudget)
		if err != nil || st.ConversationID != s.st.ConversationID || !st.Reopen || st.AwaitReopen != nil {
			t.Fatalf("resolve = %+v %v, want the reopen", st, err)
		}
		sum := livesession.Summary{Text: "SUMMARY-OF-THE-RESTATED-SESSION", ConversationID: st.ConversationID}
		if res, err := livesession.CloseOf(t.Context(), s.cfg, c.dir, c.id, sum, testBudget); err != nil || !res.Done {
			t.Fatalf("close: %+v %v", res, err)
		}
		if closed, _ := s.srv.ClosedWith(s.st.ConversationID); closed.LayerID != st.LayerID || closed.Summary != sum.Text {
			t.Fatalf("close = %+v, want the reopened layer with the stop's summary", closed)
		}
	})
	t.Run("ended before a prompt", func(t *testing.T) {
		s := newStopped(t)
		s.closing(t)
		daemon := s.blockedSummarizer(t, "")
		c := s.continueWaiting(t, s.closingSource(t))
		daemon.finish(t)
		if res, err := livesession.Close(t.Context(), s.cfg, c.dir, "", testBudget); err != nil || res.Status != livesession.StatusEmpty {
			t.Fatalf("close: %+v %v, want empty", res, err)
		}
		if got := s.srv.CallsTo("PUT", s.st.ConversationID); got != 1 {
			t.Fatalf("an empty continuation reached the server (%d opens)", got)
		}
		// the conversation it took over is still the one the next prompt resumes
		if err := os.RemoveAll(c.dir); err != nil {
			t.Fatal(err)
		}
		if src := s.source(t, c.id); src == nil || src.State.ConversationID != s.st.ConversationID {
			t.Fatalf("next resume = %+v, want the original conversation", src)
		}
	})
}

// A resolution cut short after it copied the seed in, before it wrote the
// reopened state, must leave nothing a later one copies twice or a fallback
// keeps.
func TestInterruptedDeferredReopenIsUndoneNeverRepeated(t *testing.T) {
	for _, tc := range []struct {
		name     string
		withdraw bool // the person stops the awaited session before the retry
	}{
		{"retry reopens", false},
		{"retry falls back", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStopped(t)
			s.closing(t)
			daemon := s.blockedSummarizer(t, "")
			c := s.continueWaiting(t, s.closingSource(t))
			fresh := withPrompt(lines(10, 12))
			if _, err := livesession.Spool(t.Context(), c.dir, fresh, livesession.SpoolOptions{}); err != nil {
				t.Fatal(err)
			}
			daemon.finish(t)

			restore := livesession.SetReopenAwaitedStateHook(func() error { return os.ErrPermission })
			_, err := livesession.Open(t.Context(), s.cfg, c.dir, livesession.OpenParams{SessionID: c.id})
			restore()
			if err == nil {
				t.Fatal("open succeeded though the reopened state was never written")
			}
			cur, _ := livesession.ReadState(c.dir)
			spool, _ := os.ReadFile(filepath.Join(c.dir, "spool.jsonl"))
			if cur.AwaitReopen == nil || cur.AwaitReopen.SeedBytes != int64(len(s.content)) || !bytes.HasPrefix(spool, s.content) {
				t.Fatalf("precondition: state %+v with a seeded spool, want the copy recorded", cur)
			}
			if tc.withdraw {
				if err := livesession.MarkManualStop(s.dir); err != nil {
					t.Fatal(err)
				}
			}

			st, err := livesession.Open(t.Context(), s.cfg, c.dir, livesession.OpenParams{SessionID: c.id})
			if err != nil {
				t.Fatalf("retry: %v", err)
			}
			if _, err := livesession.Drain(t.Context(), s.cfg, c.dir, testBudget); err != nil {
				t.Fatalf("drain: %v", err)
			}
			want := append(append([]byte(nil), s.content...), fresh...)
			wantCnv := s.st.ConversationID
			if tc.withdraw {
				want, wantCnv = append(append([]byte(nil), c.header...), fresh...), c.cnv
			}
			if st.ConversationID != wantCnv {
				t.Fatalf("conversation = %s, want %s", st.ConversationID, wantCnv)
			}
			if got := s.srv.Stored(t, wantCnv, st.LayerID); !bytes.Equal(got, want) {
				t.Fatalf("stream holds %q, want %q", got, want)
			}
		})
	}
}

func TestAwaitReopenRefusesWhatCannotWait(t *testing.T) {
	s := newStopped(t)
	s.closing(t)
	src := s.closingSource(t)
	dir := filepath.Join(s.sessions, "second")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := sessionid.GenerateSessionID()
	if _, err := livesession.Prepare(t.Context(), s.cfg, dir, livesession.OpenParams{SessionID: id}); err != nil {
		t.Fatal(err)
	}
	// no raw.jsonl header to keep: a reopen could not put the seed in its place
	if _, err := livesession.AwaitReopen(t.Context(), s.cfg, dir, id, src); err == nil {
		t.Fatal("waited without the recording's own header line")
	}
	// the stream in its own folder can never be waited for from that folder
	if _, err := livesession.AwaitReopen(t.Context(), s.cfg, s.dir, s.st.SessionID, src); err == nil {
		t.Fatal("waited for a stream in the waiting stream's own folder")
	}
	if _, err := livesession.PrepareReopen(t.Context(), s.cfg, dir, livesession.OpenParams{SessionID: id}, src); err == nil {
		t.Fatal("prepared a reopen of a stream still closing")
	}
	if st, _ := livesession.ReadState(dir); st.AwaitReopen != nil || st.ConversationID == s.st.ConversationID {
		t.Fatalf("a refused wait changed the stream: %+v", st)
	}
}
