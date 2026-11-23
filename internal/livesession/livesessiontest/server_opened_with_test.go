package livesessiontest_test

import (
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/livesession/livesessiontest"
	"github.com/sageox/ox/internal/sessionid"
)

// OpenedWith is what tests read to check what a client put on the wire, so
// it must report the open the fake accepted, as decoded, and nothing for an
// open it refused. Failure prevented: a refused open reported as accepted,
// passing a test of a stream that never opened.
func TestOpenedWithReportsOnlyTheOpenTheFakeAccepted(t *testing.T) {
	srv := livesessiontest.New(t)
	cfg := srv.Config(t)
	dir := t.TempDir()
	parent := sessionid.GenerateSessionID()
	p := livesession.OpenParams{
		SessionID: sessionid.GenerateSessionID(), SessionName: "2026-03-04T13-41-person-a-Oxabc1",
		RepoID: "repo_test", Agent: "claude-code", StartedAt: time.Now(), ParentSessionID: parent,
	}
	cnv, err := livesession.ConversationID(p.SessionID)
	if err != nil {
		t.Fatal(err)
	}

	srv.RefuseOpen(1)
	if _, err := livesession.Open(t.Context(), cfg, dir, p); err == nil {
		t.Fatal("harness: the refused open succeeded")
	}
	if got, ok := srv.OpenedWith(cnv); ok {
		t.Fatalf("a refused open was reported as accepted: %+v", got)
	}

	if _, err := livesession.Open(t.Context(), cfg, dir, p); err != nil {
		t.Fatalf("open: %v", err)
	}
	got, ok := srv.OpenedWith(cnv)
	if !ok || got.SessionID != p.SessionID || got.ParentSessionID != parent {
		t.Fatalf("accepted open = %+v (%v), want session %s with parent %s", got, ok, p.SessionID, parent)
	}
}
