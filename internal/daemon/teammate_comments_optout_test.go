package daemon

import (
	"encoding/json"
	"log/slog"
	"testing"
)

// Failure prevented: an owner's note reaches the hook without the flag that
// tells it the words are the user's own, so it is labeled a teammate's.
func TestOwnerNoteKeepsItsOwnerFlagForTheHook(t *testing.T) {
	e, box, _ := newCommentEnv(t, "agent-a")
	own := srvComment("tcm_own", "from me")
	own.FromOwner = true
	e.srv.QueueTeammateComment(own)
	e.srv.QueueTeammateComment(srvComment("tcm_mate", "from a teammate"))
	e.drainOnce()

	got, err := box.take(t.Context(), e.payload(5))
	if err != nil || len(got) != 2 {
		t.Fatalf("take = %+v, %v; want both notes", got, err)
	}
	for _, n := range got {
		flag, set := n.Metadata[tcMetaFromOwner]
		switch n.ID {
		case "tcm_own":
			if flag != "true" {
				t.Fatalf("owner note from_owner = %q, want \"true\"", flag)
			}
		case "tcm_mate":
			if set {
				t.Fatalf("teammate note carries from_owner = %q; only an owner note may", flag)
			}
		default:
			t.Fatalf("unexpected note %s", n.ID)
		}
	}
}

// Failure prevented: with notes off on this machine, the server is never told
// (so the web still offers a note box) and a note admitted before the opt-out
// reached it still lands in the agent.
func TestTeammateNotesOffTellsTheServerAndDropsWhatStillArrives(t *testing.T) {
	e, box, store := newCommentEnv(t, "agent-a")
	off := func() bool { return false }
	e.cfg.AcceptsNotes, box.enabled = off, off
	own := srvComment("tcm_own", "from me")
	own.FromOwner = true
	e.srv.QueueTeammateComment(own)
	e.srv.QueueTeammateComment(srvComment("tcm_mate", "x"))
	e.drainOnce()

	bodies := e.srv.OpenBodies()
	if len(bodies) != 1 {
		t.Fatalf("opens = %d, want 1", len(bodies))
	}
	var open struct {
		AcceptsNotes *bool `json:"accepts_notes"`
	}
	if err := json.Unmarshal(bodies[0], &open); err != nil {
		t.Fatal(err)
	}
	if open.AcceptsNotes == nil || *open.AcceptsNotes {
		t.Fatalf("open accepts_notes = %v, want false", open.AcceptsNotes)
	}
	if ids := stillPending(t, store, "agent-a"); len(ids) != 0 {
		t.Fatalf("pending = %v; notes must be dropped while off, owner notes included", ids)
	}
	if got, err := box.lease(t.Context(), e.payload(5)); err != nil || len(got.Entries) != 0 {
		t.Fatalf("lease = %+v, %v while off", got, err)
	}
	e.live.Wait()
	if ackLogExists(e) {
		t.Fatal("a dropped note was acked")
	}
	for _, id := range []string{"tcm_own", "tcm_mate"} {
		if _, acked := e.srv.DeliveredAtSeq(id); acked {
			t.Fatalf("server recorded a delivery of %s, which was never shown", id)
		}
	}
}

// Failure prevented: the daemon's own streams open without the switch, so only
// CLI-opened streams tell the server notes are off.
func TestLiveSessionsDefaultConfigCarriesTheNotesSwitch(t *testing.T) {
	l := NewLiveSessions(slog.Default(), t.TempDir(), "https://test.sageox.ai")
	if l.config("https://test.sageox.ai").AcceptsNotes != nil {
		t.Fatal("no switch wired: AcceptsNotes must be nil (the open omits the field)")
	}
	l.acceptsNotes = func() bool { return false }
	got := l.config("https://test.sageox.ai").AcceptsNotes
	if got == nil || got() {
		t.Fatal("the daemon's open config must carry the notes switch")
	}
}
