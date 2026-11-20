package livesession_test

import (
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/livesession/livesessiontest"
	"github.com/sageox/ox/internal/sessionid"
)

// Failure prevented: the hook prepares a stream at start, but the daemon opens
// it later from the state file alone, often after the parent recording has
// ended. Links not carried in that state never reached the server, so a child
// session listed as a row of its own.
func TestOpenSendsTheSessionLinksThePrepareRecorded(t *testing.T) {
	srv := livesessiontest.New(t)
	cfg := srv.Config(t)
	dir := t.TempDir()
	parent, earlier := sessionid.GenerateSessionID(), sessionid.GenerateSessionID()
	p := livesession.OpenParams{
		SessionID: sessionid.GenerateSessionID(), SessionName: "2026-03-04T13-41-person-a-Oxabc1",
		RepoID: "repo_test", Agent: "claude-code", StartedAt: time.Now(),
		ParentSessionID: parent, ContinuedFromSessionID: earlier,
	}
	if _, err := livesession.Prepare(t.Context(), cfg, dir, p); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := livesession.Spool(t.Context(), dir, withPrompt(lines(0, 2)), livesession.SpoolOptions{}); err != nil {
		t.Fatal(err)
	}

	// what the daemon passes: identity only, read back from the state file
	st, err := livesession.Open(t.Context(), cfg, dir, livesession.OpenParams{SessionID: p.SessionID})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	got, ok := srv.OpenedWith(st.ConversationID)
	if !ok {
		t.Fatal("the server never accepted an open")
	}
	if got.ParentSessionID != parent || got.ContinuedFromSessionID != earlier {
		t.Fatalf("open carried parent=%q continued_from=%q, want %q and %q",
			got.ParentSessionID, got.ContinuedFromSessionID, parent, earlier)
	}
}
