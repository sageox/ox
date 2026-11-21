package livesession_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/sessionid"
)

// A resumed recording that gets no prompt after its seeded spool ends empty.
// Its close finishes there, so it must settle the stash like a close does: a
// manual stop is final, any other end keeps the conversation resumable.
func TestEmptyEndOfAResumedStreamSettlesItsStash(t *testing.T) {
	t.Run("manual stop removes it", func(t *testing.T) {
		s := newStopped(t)
		s.stop(t) // the agent exited: stashed, resumable
		dir2 := filepath.Join(s.sessions, "second")
		if err := os.Mkdir(dir2, 0o755); err != nil {
			t.Fatal(err)
		}
		second := sessionid.GenerateSessionID()
		if _, err := livesession.PrepareReopen(t.Context(), s.cfg, dir2, livesession.OpenParams{SessionID: second}, s.source(t, s.st.SessionID)); err != nil {
			t.Fatal(err)
		}
		// the person stops the resumed session before typing anything
		if err := livesession.MarkManualStop(dir2); err != nil {
			t.Fatal(err)
		}
		if res, err := livesession.Close(t.Context(), s.cfg, dir2, "", testBudget); err != nil || res.Status != livesession.StatusEmpty {
			t.Fatalf("close: %+v %v, want empty", res, err)
		}
		if err := os.RemoveAll(dir2); err != nil { // cache pruning
			t.Fatal(err)
		}
		if src := s.source(t, second); src != nil {
			t.Fatalf("a manually stopped conversation was offered for reopen: %+v", src.State)
		}
		if left, _ := filepath.Glob(filepath.Join(s.cfg.DataDir, "live-reopen", s.st.ConversationID+".*")); len(left) != 0 {
			t.Fatalf("the stash of a manually stopped conversation is still on disk: %v", left)
		}
	})
	t.Run("any other end refreshes it", func(t *testing.T) {
		s := newStopped(t)
		s.stop(t)
		src := s.source(t, s.st.SessionID)
		// a stopped stream with no stash (one closed by an older ox, or whose
		// stash failed): the resume itself has nothing to retarget
		for _, ext := range []string{".json", ".spool"} {
			if err := os.Remove(filepath.Join(s.cfg.DataDir, "live-reopen", s.st.ConversationID+ext)); err != nil {
				t.Fatal(err)
			}
		}
		dir2 := filepath.Join(s.sessions, "second")
		if err := os.Mkdir(dir2, 0o755); err != nil {
			t.Fatal(err)
		}
		second := sessionid.GenerateSessionID()
		if _, err := livesession.PrepareReopen(t.Context(), s.cfg, dir2, livesession.OpenParams{SessionID: second}, src); err != nil {
			t.Fatal(err)
		}
		if res, err := livesession.Close(t.Context(), s.cfg, dir2, "", testBudget); err != nil || res.Status != livesession.StatusEmpty {
			t.Fatalf("close: %+v %v, want empty", res, err)
		}
		for _, dir := range []string{s.dir, dir2} { // cache pruning takes both
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
		}
		if again := s.source(t, second); again == nil || again.State.ConversationID != s.st.ConversationID {
			t.Fatalf("next resume = %+v, want the original conversation from the stash", again)
		}
	})
}
