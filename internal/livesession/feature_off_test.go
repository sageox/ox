package livesession_test

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/sessionid"
	"github.com/sageox/ox/internal/sessionstream"
)

// Failure prevented: the rollout flag turned off after ox's settings cache said
// on, so the first open was refused 403 feature_off (action stop) and ox
// retried it every 30 s for good. Only feature_off ends the stream: any other
// refusal of the open, a stop included, keeps today's retry.
func TestOpenRefusalEndsTheStreamOnlyForFeatureOff(t *testing.T) {
	cases := []struct {
		name         string
		status       int
		reason       string
		wantDisabled bool
	}{
		{"feature off", http.StatusForbidden, sessionstream.ReasonFeatureOff, true},
		{"forbidden is retried", http.StatusForbidden, sessionstream.ReasonForbidden, false},
		{"repo not found is retried", http.StatusNotFound, sessionstream.ReasonNotFound, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, cfg, dir, p := prepared(t)
			if _, err := livesession.Spool(t.Context(), dir, prompt(), livesession.SpoolOptions{}); err != nil {
				t.Fatal(err)
			}
			cnv := mustState(t, dir).ConversationID
			srv.RefuseNextOpens(1, tc.status, tc.reason, sessionstream.ActionStop)

			st, err := livesession.Open(t.Context(), cfg, dir, p)

			if !tc.wantDisabled {
				var api *sessionstream.APIError
				if !errors.As(err, &api) || api.Body.Reason != tc.reason || mustState(t, dir).Status != livesession.StatusPending {
					t.Fatalf("open = %+v, %v; want the refusal returned and the stream left pending", st, err)
				}
				if st, err := livesession.Open(t.Context(), cfg, dir, p); err != nil || st.Status != livesession.StatusStreaming {
					t.Fatalf("retry = %+v, %v; want it to open once the server admits it", st, err)
				}
				return
			}
			if err != nil || st.Status != livesession.StatusDisabled || st.DisabledReason != sessionstream.ReasonFeatureOff {
				t.Fatalf("open = %+v, %v; want a disabled stream", st, err)
			}
			if on := mustState(t, dir); on.Status != livesession.StatusDisabled {
				t.Fatalf("on-disk status = %s, want disabled", on.Status)
			}
			if _, err := livesession.Open(t.Context(), cfg, dir, p); err != nil {
				t.Fatalf("open of a disabled stream: %v", err)
			}
			if n := srv.CallsTo(http.MethodPut, cnv); n != 1 {
				t.Fatalf("%d opens sent, want 1: a disabled stream is never opened again", n)
			}
		})
	}
}

// Failure prevented: a stop whose open was refused feature_off left the stream
// pending with its close requested, so the daemon re-ran the summary model and
// the open on every tick. The stop settles it at once.
func TestCloseRefusedFeatureOffAtOpenSettlesTheStream(t *testing.T) {
	srv, cfg, dir, _ := prepared(t)
	if _, err := livesession.Spool(t.Context(), dir, withPrompt(lines(0, 2)), livesession.SpoolOptions{}); err != nil {
		t.Fatal(err)
	}
	cnv := mustState(t, dir).ConversationID
	srv.RefuseNextOpens(1, http.StatusForbidden, sessionstream.ReasonFeatureOff, sessionstream.ActionStop)

	res, err := livesession.Close(t.Context(), cfg, dir, "a summary", testBudget)
	if err != nil || !res.Done || res.Status != livesession.StatusDisabled {
		t.Fatalf("close = %+v, %v; want it done with the stream disabled", res, err)
	}
	if res, err := livesession.Close(t.Context(), cfg, dir, "a summary", testBudget); err != nil || !res.Done {
		t.Fatalf("second close = %+v, %v; want done", res, err)
	}
	if n := srv.CallsTo(http.MethodPut, cnv); n != 1 {
		t.Fatalf("%d opens sent, want 1", n)
	}
	if _, ok := srv.ClosedWith(cnv); ok {
		t.Fatal("a stream the server never admitted was closed")
	}
}

// Failure prevented (review): an admitted stream whose SessionEnd probe open
// was refused feature_off was disabled, so its close was never requested and
// SageOx showed it live until the auto-finish. api-go never re-checks the flag
// on a close, so the stream stays as it was and its close still lands.
func TestFeatureOffOnAnAdmittedStreamStillLetsItClose(t *testing.T) {
	e := newEnv(t)
	e.spool(withPrompt(lines(0, 2)))
	e.mustDrain()
	e.srv.RefuseNextOpens(1, http.StatusForbidden, sessionstream.ReasonFeatureOff, sessionstream.ActionStop)

	_, err := livesession.Open(t.Context(), e.cfg, e.dir, livesession.OpenParams{SessionID: e.st.SessionID})

	var api *sessionstream.APIError
	if !errors.As(err, &api) || api.Body.Reason != sessionstream.ReasonFeatureOff {
		t.Fatalf("probe open: %v; want the feature_off refusal returned", err)
	}
	if st := e.state(); st.Status != livesession.StatusStreaming {
		t.Fatalf("status = %s, want an admitted stream left streaming", st.Status)
	}
	if res, err := livesession.Close(t.Context(), e.cfg, e.dir, "", testBudget); err != nil || res.Status != livesession.StatusClosed {
		t.Fatalf("close = %+v, %v; want closed", res, err)
	}
	if _, ok := e.srv.ClosedWith(e.st.ConversationID); !ok {
		t.Fatal("the server never saw the close")
	}
}

// Review question: a resumed stream refused feature_off falls back to a
// conversation of its own, as for any refused reopen, and that open is refused
// too. Kept on purpose: left on the reopened conversation, the disabled stream
// would name the earlier recording's conversation, and discarding this
// recording would delete it. The earlier conversation is never touched.
func TestFeatureOffOnAReopenEndsDisabledWithoutTouchingTheEarlierConversation(t *testing.T) {
	s := newStopped(t)
	s.stop(t)
	dir2 := filepath.Join(s.sessions, "second")
	if err := os.Mkdir(dir2, 0o755); err != nil {
		t.Fatal(err)
	}
	// the recording's own header, which a fallback restates in place of the seed
	header := []byte(`{"type":"header","metadata":{"session_id":"second"}}` + "\n")
	if err := os.WriteFile(filepath.Join(dir2, "raw.jsonl"), header, 0o600); err != nil {
		t.Fatal(err)
	}
	newID := sessionid.GenerateSessionID()
	if _, err := livesession.PrepareReopen(t.Context(), s.cfg, dir2, livesession.OpenParams{SessionID: newID, SessionName: "second-name", RepoID: "repo_test", Agent: "claude-code"}, s.source(t, s.st.SessionID)); err != nil {
		t.Fatal(err)
	}
	if _, err := livesession.Spool(t.Context(), dir2, withPrompt(lines(10, 12)), livesession.SpoolOptions{}); err != nil {
		t.Fatal(err)
	}
	opensBefore := len(s.srv.Calls())
	s.srv.RefuseNextOpens(2, http.StatusForbidden, sessionstream.ReasonFeatureOff, sessionstream.ActionStop)

	st, err := livesession.Open(t.Context(), s.cfg, dir2, livesession.OpenParams{SessionID: newID})

	ownCnv, _ := livesession.ConversationID(newID)
	if err != nil || st.Status != livesession.StatusDisabled || st.ConversationID != ownCnv {
		t.Fatalf("open = %+v, %v; want disabled on the recording's own conversation", st, err)
	}
	puts := 0
	for _, c := range s.srv.Calls()[opensBefore:] {
		if c.Method == http.MethodPut {
			puts++
		}
	}
	if puts != 2 {
		t.Fatalf("%d opens sent, want 2: the reopen and its fallback, then none", puts)
	}
	if _, err := livesession.Abort(t.Context(), s.cfg, dir2, testBudget); err != nil {
		t.Fatal(err)
	}
	if _, closed := s.srv.ClosedWith(s.st.ConversationID); !closed || s.srv.IsDeleted(s.st.ConversationID) {
		t.Fatal("the earlier recording's conversation must stay closed and undeleted")
	}
	if layers := s.srv.Layers(s.st.ConversationID); len(layers) != 1 {
		t.Fatalf("earlier conversation layers = %v, want only its own", layers)
	}
}

func mustState(t *testing.T, dir string) livesession.State {
	t.Helper()
	st, err := livesession.ReadState(dir)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	return st
}
