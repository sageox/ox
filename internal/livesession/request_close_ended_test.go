package livesession_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionid"
)

// Failure prevented: a recording that ended with no stop (its AI teammate
// crashed) left its stream open, so SageOx showed the session live for 72 h.
// The close is requested only for the stream of that recording, and never
// for a held one, whose live copy the daemon deletes instead.
func TestRequestCloseOfEndedRequestsOnlyThisRecordingsUnheldStream(t *testing.T) {
	cases := []struct {
		name          string
		setup         func(t *testing.T, e *env) string // returns the ended recording's id
		wantRequested bool
	}{
		{"streaming", func(_ *testing.T, e *env) string { return e.st.SessionID }, true},
		{"pending, never opened", func(t *testing.T, e *env) string {
			_, cfg, dir, p := prepared(t)
			e.cfg, e.dir = cfg, dir
			return p.SessionID
		}, true},
		{"another recording's stream in a reused folder", func(*testing.T, *env) string { return sessionid.GenerateSessionID() }, false},
		{"held", func(t *testing.T, e *env) string {
			if err := session.WriteHoldMarker(e.dir, session.HoldManualPublishing, "test"); err != nil {
				t.Fatal(err)
			}
			return e.st.SessionID
		}, false},
		{"already closed", func(t *testing.T, e *env) string {
			if res, err := livesession.Close(t.Context(), e.cfg, e.dir, "", testBudget); err != nil || !res.Done {
				t.Fatalf("close: %+v %v", res, err)
			}
			return e.st.SessionID
		}, false},
		{"no stream", func(t *testing.T, e *env) string {
			e.dir = t.TempDir()
			return e.st.SessionID
		}, false},
		{"no recording id", func(*testing.T, *env) string { return "" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.spool(withPrompt(lines(0, 2)))
			e.mustDrain()
			sessionID := tc.setup(t, e)

			requested, err := livesession.RequestCloseOfEnded(e.dir, sessionID)

			if err != nil || requested != tc.wantRequested {
				t.Fatalf("requested = %v, %v; want %v", requested, err, tc.wantRequested)
			}
			// the marker, not State.CloseRequested: a closed stream's state keeps
			// the flag its own close persisted
			_, statErr := os.Stat(filepath.Join(e.dir, "close-requested"))
			if got := statErr == nil; got != tc.wantRequested {
				t.Fatalf("close-requested marker on disk = %v, want %v", got, tc.wantRequested)
			}
		})
	}
}

// Failure prevented (review): the close request is written without live.lock,
// so a recording restarted in the same minute could take the folder between
// the ownership check and the write, and the daemon then closed that new,
// still-active stream. The request names its recording and binds no other.
func TestCloseRequestOfAnEndedRecordingNeverClosesTheNextOne(t *testing.T) {
	e := newEnv(t)
	e.spool(withPrompt(lines(0, 2)))
	e.mustDrain()
	if requested, err := livesession.RequestCloseOfEnded(e.dir, e.st.SessionID); err != nil || !requested {
		t.Fatalf("requested = %v, %v; want the ended recording's close requested", requested, err)
	}

	// the same-minute restart: the ended stream's state goes, the next recording's arrives
	if err := os.Remove(filepath.Join(e.dir, "stream-state.json")); err != nil {
		t.Fatal(err)
	}
	next := livesession.OpenParams{SessionID: sessionid.GenerateSessionID(), SessionName: "2026-03-04T13-41-person-a-Oxabc1", RepoID: "repo_test", Agent: "claude-code"}
	if _, err := livesession.Prepare(t.Context(), e.cfg, e.dir, next); err != nil {
		t.Fatal(err)
	}

	if st := e.state(); st.SessionID != next.SessionID || st.CloseRequested {
		t.Fatalf("state = %+v; the next recording's stream must not read as closing", st)
	}
	// its own stop still asks for its close in the usual way
	if err := livesession.RequestClose(e.dir); err != nil {
		t.Fatal(err)
	}
	if !e.state().CloseRequested {
		t.Fatal("the next recording's own close request was not recorded")
	}
}
