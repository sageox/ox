package livesession_test

import (
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/session"
)

// Failure prevented: the hold writer now takes live.lock for a folder with a
// live stream, and a drain hearing the 410 already holds it. Taken again from
// the same process, flock waits out the writer's whole lock wait (15 s) behind
// its own holder before writing the hold unlocked: every server-side delete
// stalled its drain past the drain's budget.
func TestDrainThatHearsTheDeleteHoldsWithoutWaitingOnItsOwnLock(t *testing.T) {
	e := newEnv(t)
	e.spool(withPrompt(lines(0, 2)))
	e.mustDrain()
	e.srv.DeleteSession(e.st.ConversationID)
	e.spool(lines(2, 3))

	start := time.Now()
	res, err := e.drain()
	elapsed := time.Since(start)
	if err != nil || res.Status != livesession.StatusDeleted {
		t.Fatalf("drain after a delete = %+v %v, want deleted", res, err)
	}
	if elapsed >= testBudget {
		t.Fatalf("drain took %s to record the delete, past its %s budget", elapsed, testBudget)
	}
	if hold, err := session.ReadHoldMarker(e.dir); err != nil || hold.Reason != session.HoldDeletedOnServer || hold.SessionID != e.st.SessionID {
		t.Fatalf("hold = %+v %v, want deleted_on_server for this recording", hold, err)
	}
}
