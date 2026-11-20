package livesession_test

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/livesession/livesessiontest"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionid"
	"github.com/sageox/ox/internal/sessionstream"
)

// stoppedSession streams a session in <sessions>/first, spools content and
// closes it the way a manual stop does.
type stoppedSession struct {
	srv      *livesessiontest.Server
	cfg      livesession.Config
	sessions string
	dir      string
	st       livesession.State
	content  []byte
}

func newStopped(t *testing.T) *stoppedSession {
	t.Helper()
	srv := livesessiontest.New(t)
	s := &stoppedSession{srv: srv, cfg: srv.Config(t), sessions: t.TempDir()}
	s.dir = filepath.Join(s.sessions, "first")
	if err := os.Mkdir(s.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s.content = withPrompt(lines(0, 3))
	st, err := livesession.Prepare(t.Context(), s.cfg, s.dir, livesession.OpenParams{
		SessionID: sessionid.GenerateSessionID(), SessionName: "first-name", RepoID: "repo_test", Agent: "claude-code", StartedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	s.st = st
	if _, err := livesession.Spool(t.Context(), s.dir, s.content, livesession.SpoolOptions{}); err != nil {
		t.Fatalf("spool: %v", err)
	}
	return s
}

func (s *stoppedSession) stop(t *testing.T) {
	t.Helper()
	res, err := livesession.Close(t.Context(), s.cfg, s.dir, "", testBudget)
	if err != nil || !res.Done {
		t.Fatalf("stop: %+v %v", res, err)
	}
}

func (s *stoppedSession) source(t *testing.T, continuedFrom string) *livesession.ReopenSource {
	t.Helper()
	src, err := livesession.FindReopenSource(t.Context(), s.cfg, s.sessions, continuedFrom)
	if err != nil {
		t.Fatalf("find reopen source: %v", err)
	}
	return src
}

func TestReopenAfterStopReusesConversationAndRestatesFromZero(t *testing.T) {
	s := newStopped(t)
	s.stop(t)

	src := s.source(t, s.st.SessionID)
	if src == nil {
		t.Fatal("a manually stopped stream was not offered for reopen")
	}
	dir2 := filepath.Join(s.sessions, "second")
	if err := os.Mkdir(dir2, 0o755); err != nil {
		t.Fatal(err)
	}
	newID := sessionid.GenerateSessionID()
	st, err := livesession.PrepareReopen(t.Context(), s.cfg, dir2, livesession.OpenParams{SessionID: newID, SessionName: "second-name", RepoID: "repo_test", Agent: "claude-code"}, src)
	if err != nil {
		t.Fatalf("prepare reopen: %v", err)
	}
	if st.ConversationID != s.st.ConversationID || !st.Reopen || st.LayerID == s.st.LayerID || st.Status != livesession.StatusPending {
		t.Fatalf("reopen state = %+v, want the stopped conversation, a new layer, reopen set, pending", st)
	}

	// no prompt typed since the resume: the copied one must not open the stream
	if _, err := livesession.Open(t.Context(), s.cfg, dir2, livesession.OpenParams{SessionID: newID}); !errors.Is(err, livesession.ErrAwaitingPrompt) {
		t.Fatalf("open before a new prompt: %v, want ErrAwaitingPrompt", err)
	}
	if got := s.srv.CallsTo("PUT", s.st.ConversationID); got != 1 {
		t.Fatalf("open requests so far = %d, want only the original one", got)
	}

	fresh := withPrompt(lines(10, 12))
	if _, err := livesession.Spool(t.Context(), dir2, fresh, livesession.SpoolOptions{}); err != nil {
		t.Fatalf("spool: %v", err)
	}
	if _, err := livesession.Open(t.Context(), s.cfg, dir2, livesession.OpenParams{SessionID: newID}); err != nil {
		t.Fatalf("open: %v", err)
	}
	opened, ok := s.srv.OpenedWith(s.st.ConversationID)
	if !ok || !opened.Reopen || opened.LayerID != st.LayerID {
		t.Fatalf("server saw %+v, want reopen on the new layer", opened)
	}
	if want := "ses_" + s.st.ConversationID[len("cnv_"):]; opened.SessionID != want {
		t.Fatalf("open session_id = %s, want the conversation's twin %s", opened.SessionID, want)
	}
	if opened.SessionName != "first-name" {
		t.Fatalf("open session_name = %q, want the conversation's own name", opened.SessionName)
	}

	if _, err := livesession.Drain(t.Context(), s.cfg, dir2, testBudget); err != nil {
		t.Fatalf("drain: %v", err)
	}
	// byte 0 onwards: the earlier lines first, then the new ones
	if got, want := s.srv.Stored(t, s.st.ConversationID, st.LayerID), append(append([]byte(nil), s.content...), fresh...); !bytes.Equal(got, want) {
		t.Fatalf("new layer holds %d bytes, want the %d-byte restated session", len(got), len(want))
	}
	if layers := s.srv.Layers(s.st.ConversationID); len(layers) != 2 {
		t.Fatalf("layers = %v, want the original and one new", layers)
	}
	// once the server took the reopen, later opens are ordinary
	if cur, _ := livesession.ReadState(dir2); cur.Reopen || cur.Status != livesession.StatusStreaming {
		t.Fatalf("state after open = %+v, want streaming with reopen cleared", cur)
	}
}

func TestReopenNeverOfferedAfterWithdrawal(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, s *stoppedSession) string // returns the id to continue from
	}{
		{"still streaming", func(t *testing.T, s *stoppedSession) string { return s.st.SessionID }},
		{"hold marker", func(t *testing.T, s *stoppedSession) string {
			s.stop(t)
			if err := session.WriteHoldMarker(s.dir, session.HoldManualPublishing, "test"); err != nil {
				t.Fatal(err)
			}
			return s.st.SessionID
		}},
		{"deleted on server (410 / stop_and_wipe)", func(t *testing.T, s *stoppedSession) string {
			if _, err := livesession.Open(t.Context(), s.cfg, s.dir, livesession.OpenParams{SessionID: s.st.SessionID}); err != nil {
				t.Fatal(err)
			}
			s.srv.DeleteSession(s.st.ConversationID)
			if _, err := livesession.Spool(t.Context(), s.dir, lines(5, 6), livesession.SpoolOptions{}); err != nil {
				t.Fatal(err)
			}
			_, _ = livesession.Drain(t.Context(), s.cfg, s.dir, testBudget)
			if st, _ := livesession.ReadState(s.dir); !st.DeletedOnServer {
				t.Fatalf("setup: state = %+v, want deleted on server", st)
			}
			return s.st.SessionID
		}},
		{"aborted locally", func(t *testing.T, s *stoppedSession) string {
			s.stop(t)
			if _, err := livesession.Abort(t.Context(), s.cfg, s.dir, testBudget); err != nil {
				t.Fatal(err)
			}
			return s.st.SessionID
		}},
		{"another recording", func(t *testing.T, s *stoppedSession) string {
			s.stop(t)
			return sessionid.GenerateSessionID()
		}},
		{"not a session id", func(t *testing.T, s *stoppedSession) string {
			s.stop(t)
			return "../first"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newStopped(t)
			id := tt.setup(t, s)
			if src := s.source(t, id); src != nil {
				t.Fatalf("offered %+v for reopen", src.State)
			}
		})
	}
}

func TestReopenRefusesAStateNotMintedForItsFolder(t *testing.T) {
	s := newStopped(t)
	s.stop(t)
	// a stream state pulled in with a Ledger checkout: same ids, other folder
	forged := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(filepath.Join(forged, "planted"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"stream-state.json", "spool.jsonl"} {
		b, err := os.ReadFile(filepath.Join(s.dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(forged, "planted", name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	src, err := livesession.FindReopenSource(t.Context(), s.cfg, forged, s.st.SessionID)
	if err != nil || src != nil {
		t.Fatalf("planted state offered for reopen: %v %v", src, err)
	}
}

func TestReopenEndedBeforeAnyPromptCanBeReopenedAgain(t *testing.T) {
	s := newStopped(t)
	s.stop(t)
	dir2 := filepath.Join(s.sessions, "second")
	if err := os.Mkdir(dir2, 0o755); err != nil {
		t.Fatal(err)
	}
	second := sessionid.GenerateSessionID()
	if _, err := livesession.PrepareReopen(t.Context(), s.cfg, dir2, livesession.OpenParams{SessionID: second}, s.source(t, s.st.SessionID)); err != nil {
		t.Fatal(err)
	}
	// stopped before a prompt: nothing is sent, the stream ends empty
	if res, err := livesession.Close(t.Context(), s.cfg, dir2, "", testBudget); err != nil || res.Status != livesession.StatusEmpty {
		t.Fatalf("close: %+v %v, want empty", res, err)
	}
	if src := s.source(t, second); src == nil || src.State.ConversationID != s.st.ConversationID {
		t.Fatalf("the next prompt must still reopen the original conversation, got %+v", src)
	}
}

func TestRefusedReopenStreamsAsANewConversation(t *testing.T) {
	tests := []struct {
		name   string
		status int
		reason string
		action sessionstream.Action
	}{
		{"403 forbidden", http.StatusForbidden, sessionstream.ReasonForbidden, sessionstream.ActionStop},
		{"409 stop", http.StatusConflict, sessionstream.ReasonSessionClosed, sessionstream.ActionStop},
		{"409 new_layer", http.StatusConflict, sessionstream.ReasonBytesConflict, sessionstream.ActionNewLayer},
		{"410 deleted", http.StatusGone, sessionstream.ReasonSessionDeleted, sessionstream.ActionStopAndWipe},
		{"other refusal", http.StatusBadRequest, sessionstream.ReasonInvalidRequest, sessionstream.ActionFix},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newStopped(t)
			s.stop(t)
			dir2 := filepath.Join(s.sessions, "second")
			if err := os.Mkdir(dir2, 0o755); err != nil {
				t.Fatal(err)
			}
			header := []byte(`{"type":"header","metadata":{"session_id":"second"}}` + "\n")
			if err := os.WriteFile(filepath.Join(dir2, "raw.jsonl"), header, 0o600); err != nil {
				t.Fatal(err)
			}
			newID := sessionid.GenerateSessionID()
			if _, err := livesession.PrepareReopen(t.Context(), s.cfg, dir2, livesession.OpenParams{SessionID: newID, SessionName: "second-name", RepoID: "repo_test", Agent: "claude-code"}, s.source(t, s.st.SessionID)); err != nil {
				t.Fatal(err)
			}
			fresh := withPrompt(lines(10, 12))
			if _, err := livesession.Spool(t.Context(), dir2, fresh, livesession.SpoolOptions{}); err != nil {
				t.Fatal(err)
			}
			s.srv.RefuseNextReopen(tt.status, tt.reason, tt.action)

			st, err := livesession.Open(t.Context(), s.cfg, dir2, livesession.OpenParams{SessionID: newID})
			if err != nil {
				t.Fatalf("open after a refused reopen: %v", err)
			}
			wantCnv, _ := livesession.ConversationID(newID)
			if st.ConversationID != wantCnv || st.Reopen || st.Status != livesession.StatusStreaming || st.SessionName != "second-name" {
				t.Fatalf("state = %+v, want a streaming conversation of the new recording", st)
			}
			if _, err := livesession.Drain(t.Context(), s.cfg, dir2, testBudget); err != nil {
				t.Fatalf("drain: %v", err)
			}
			// the stream is this recording alone: its header, then its lines
			if got, want := s.srv.Stored(t, wantCnv, st.LayerID), append(append([]byte(nil), header...), fresh...); !bytes.Equal(got, want) {
				t.Fatalf("new conversation holds %q, want header and new lines only", got)
			}
			if layers := s.srv.Layers(s.st.ConversationID); len(layers) != 1 {
				t.Fatalf("original conversation layers = %v, want it left alone", layers)
			}
			if opened, _ := s.srv.OpenedWith(wantCnv); opened.Reopen {
				t.Fatalf("fallback open still carried reopen: %+v", opened)
			}
		})
	}
}

func TestReopenTransportFailureStillRetries(t *testing.T) {
	s := newStopped(t)
	s.stop(t)
	dir2 := filepath.Join(s.sessions, "second")
	if err := os.Mkdir(dir2, 0o755); err != nil {
		t.Fatal(err)
	}
	newID := sessionid.GenerateSessionID()
	if _, err := livesession.PrepareReopen(t.Context(), s.cfg, dir2, livesession.OpenParams{SessionID: newID}, s.source(t, s.st.SessionID)); err != nil {
		t.Fatal(err)
	}
	if _, err := livesession.Spool(t.Context(), dir2, withPrompt(lines(10, 11)), livesession.SpoolOptions{}); err != nil {
		t.Fatal(err)
	}
	s.srv.RefuseOpen(1) // 503: not a refusal of the reopen
	if _, err := livesession.Open(t.Context(), s.cfg, dir2, livesession.OpenParams{SessionID: newID}); err == nil {
		t.Fatal("open succeeded though the server was unavailable")
	}
	if st, _ := livesession.ReadState(dir2); !st.Reopen || st.ConversationID != s.st.ConversationID {
		t.Fatalf("state after a transient failure = %+v, want the reopen kept", st)
	}
	if _, err := livesession.Open(t.Context(), s.cfg, dir2, livesession.OpenParams{SessionID: newID}); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if opened, _ := s.srv.OpenedWith(s.st.ConversationID); !opened.Reopen {
		t.Fatalf("retry lost reopen: %+v", opened)
	}
}
