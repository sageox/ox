package livesession_test

import (
	"testing"

	"github.com/sageox/ox/internal/livesession"
)

// Failure prevented: a stream closed by the daemon with no stop of its own to
// mark it (one a new recording superseded) withheld a batch, and nobody was
// ever told.
func TestAWithheldBatchMarksTheNoticeOwedAtOnce(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 2))
	e.spool(secretLine)

	res, err := livesession.Close(t.Context(), e.cfg, e.dir, "", testBudget)
	if err != nil || res.Status != livesession.StatusClosed {
		t.Fatalf("close: %+v %v, want closed", res, err)
	}
	if !livesession.OwesSecretWithheldNotice(e.dir) {
		t.Fatal("the stream withheld a batch but owes no notice: only a stop would have marked it")
	}
}
