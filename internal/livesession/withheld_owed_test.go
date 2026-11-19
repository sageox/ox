package livesession_test

import (
	"os"
	"path/filepath"
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

// Failure prevented (PR 1971 review): the flag was saved before the owed mark,
// so a mark that could not be written left a stream that withheld a secret
// with no mark, and later withholds never retried it: the notice was lost.
func TestAWithheldFlagNeverStandsWithoutItsOwedMark(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 2))
	e.mustDrain()
	e.spool(secretLine)
	mark := filepath.Join(e.dir, "withheld-notice-owed")
	if err := os.Mkdir(mark, 0o700); err != nil { // the mark cannot be written
		t.Fatal(err)
	}

	if _, err := e.drain(); err == nil {
		t.Fatal("a withhold whose owed mark could not be written must fail and retry")
	}
	if e.state().SecretWithheld {
		t.Fatal("the withheld flag was saved without its owed mark")
	}

	if err := os.Remove(mark); err != nil {
		t.Fatal(err)
	}
	e.mustDrain()
	if !e.state().SecretWithheld || !livesession.OwesSecretWithheldNotice(e.dir) {
		t.Fatal("the retried withhold must save both the flag and its owed mark")
	}
}
