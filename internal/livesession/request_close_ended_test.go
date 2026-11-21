package livesession_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionid"
)

// Failure prevented: a recording that ended with no stop (its AI coworker
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
