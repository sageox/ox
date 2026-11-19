package livesession_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/secretguard"
	"github.com/sageox/ox/internal/sessionstream"
)

// syntheticAWSKey is the fixture the publication guard tests use; split so the
// literal never appears in source.
const syntheticAWSKey = "AKIA" + "QWERTYUIOPASDFGH"

var secretLine = []byte(`{"type":"msg","content":"` + syntheticAWSKey + `"}` + "\n")

// futureLine holds a credential only the current upstream catalog knows.
var futureLine = []byte(`{"type":"msg","content":"future_synthetic"}` + "\n")

func futureRules(t *testing.T) *secretguard.RuleSet {
	t.Helper()
	rules, err := secretguard.NewRules([]byte("[[rules]]\nid='future-format'\nregex='future_[a-z]+'\n"))
	if err != nil {
		t.Fatal(err)
	}
	return rules
}

// The stream scans with the delta of the last refresh that succeeded (F2): a
// refresh that fails afterwards keeps it, so a credential only the current
// upstream catalog knows is still withheld, and the person is told at stop.
func TestDrainScansWithTheLastGoodDeltaAfterARefreshFails(t *testing.T) {
	rules, down := futureRules(t), false
	livesession.SetCurrentRulesForTest(t, func(context.Context) (*secretguard.RuleSet, error) {
		if down {
			return nil, errors.New("catalog unavailable")
		}
		return rules, nil
	})
	if secretguard.ValidateContent("sessions/content", futureLine) != nil {
		t.Fatal("test setup: the bundled scan already refuses the fixture")
	}
	if err := livesession.RefreshSecretRules(t.Context()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	down = true
	if err := livesession.RefreshSecretRules(t.Context()); err == nil {
		t.Fatal("a failed fetch reported a successful refresh")
	}
	e := newEnv(t)
	before := lines(0, 2)
	e.spool(before)
	e.mustDrain()
	if e.state().SecretWithheld {
		t.Fatal("a clean stream is marked as having withheld a secret")
	}
	e.spool(futureLine)
	e.mustDrain()

	st := e.state()
	if got, want := e.stored(st), joined(before, withheldGapFor(futureLine)); !bytes.Equal(got, want) {
		t.Fatalf("server holds\n%s\nwant\n%s", got, want)
	}
	if !st.SecretWithheld {
		t.Fatal("stop has nothing to tell the person: SecretWithheld unset")
	}
}

// batchesOf splits lines the way the drain reads them: whole lines, up to
// sessionstream.MaxBatchBytes per batch.
func batchesOf(lines [][]byte) [][]byte {
	var out [][]byte
	var cur []byte
	for _, l := range lines {
		if len(cur)+len(l) > sessionstream.MaxBatchBytes {
			out, cur = append(out, cur), nil
		}
		cur = append(cur, l...)
	}
	return append(out, cur)
}

// Only the refused lines are withheld: the clean lines around them, in the
// same batch or the next, still stream, and each refused line gets its own gap
// with its own size. Withholding whole batches hid up to 256 KiB of a
// teammate's work for one pasted key.
func TestOnlyTheRefusedLinesBecomeGaps(t *testing.T) {
	all := [][]byte{lines(0, 1), secretLine}
	for i := 1; i < 300; i++ {
		all = append(all, lines(i, i+1))
	}
	all = append(all, secretLine)
	for i := 300; i < 310; i++ {
		all = append(all, lines(i, i+1))
	}
	if b := batchesOf(all); len(b) != 2 || !bytes.Contains(b[0], secretLine) || !bytes.Contains(b[1], secretLine) {
		t.Fatalf("test setup: want two batches with a secret in each, got %d", len(b))
	}
	e := newEnv(t)
	e.spool(bytes.Join(all, nil))
	e.mustDrain()

	want := bytes.ReplaceAll(bytes.Join(all, nil), secretLine, withheldGapFor(secretLine))
	if got := e.stored(e.state()); !bytes.Equal(got, want) {
		t.Fatalf("server holds %d bytes, want %d: every clean line, and a gap for each refused one", len(got), len(want))
	}
}

// A match that spans lines refuses no line on its own. Then the whole batch is
// withheld as one gap: bytes the scan refused never go out piecemeal.
func TestARefusalAcrossLinesWithholdsTheWholeBatch(t *testing.T) {
	rules, err := secretguard.NewRules([]byte("[[rules]]\nid='split-format'\nregex='''split_a[\\s\\S]{1,80}split_b'''\n"))
	if err != nil {
		t.Fatal(err)
	}
	livesession.SetCurrentRulesForTest(t, func(context.Context) (*secretguard.RuleSet, error) { return rules, nil })
	if err := livesession.RefreshSecretRules(t.Context()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	half1 := []byte(`{"type":"msg","content":"split_a"}` + "\n")
	half2 := []byte(`{"type":"msg","content":"split_b"}` + "\n")
	if livesession.CheckPublicationForTest(half1) != nil || livesession.CheckPublicationForTest(half2) != nil ||
		livesession.CheckPublicationForTest(joined(half1, half2)) == nil {
		t.Fatal("test setup: want the rule to refuse the two halves only together")
	}
	refused := joined(lines(0, 1), half1, half2, lines(1, 2))
	e := newEnv(t)
	e.spool(refused)
	e.mustDrain()
	after := lines(2, 3)
	e.spool(after)
	e.mustDrain()

	want := joined(withheldGapFor(refused), after)
	if got := e.stored(e.state()); !bytes.Equal(got, want) {
		t.Fatalf("server holds\n%s\nwant\n%s", got, want)
	}
}

// A hook that finds live.lock busy appends without it. A drain replacing a
// withheld batch renames a new spool into place meanwhile; an append that went
// to the old file then would be lost with no gap line. The swap lock makes the
// append wait for the rename and land in the new spool.
func TestAnAppendDuringAWithholdLandsInTheNewSpool(t *testing.T) {
	e := newEnv(t)
	before, late := lines(0, 2), lines(2, 3)
	e.spool(before)
	e.mustDrain()
	e.spool(secretLine)

	appended := make(chan error, 1)
	livesession.SetSpoolRewriteHookForTest(t, func() {
		go func() {
			_, err := livesession.Spool(context.Background(), e.dir, late, livesession.SpoolOptions{})
			appended <- err
		}()
		// long enough for the append to give up on live.lock (the drain holds
		// it) and reach the swap lock, well inside the swap lock's own wait
		time.Sleep(500 * time.Millisecond)
	})
	e.mustDrain()
	if err := <-appended; err != nil {
		t.Fatalf("append during the withhold: %v", err)
	}
	e.mustDrain()

	want := joined(before, withheldGapFor(secretLine), late)
	if got := e.spoolBytes(); !bytes.Equal(got, want) {
		t.Fatalf("spool =\n%s\nwant the late append after the gap:\n%s", got, want)
	}
	if got := e.stored(e.state()); !bytes.Equal(got, want) {
		t.Fatalf("server holds\n%s\nwant\n%s", got, want)
	}
}

// The close summary leaves the machine too. A summary the scan refuses is
// dropped, and the session closes without one.
func TestCloseWithholdsASummaryTheScanRefuses(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 2))
	res, err := livesession.Close(t.Context(), e.cfg, e.dir, "deployed with key "+syntheticAWSKey, testBudget)
	if err != nil || res.Status != livesession.StatusClosed {
		t.Fatalf("close: %+v %v", res, err)
	}
	if got, ok := e.srv.ClosedWith(e.st.ConversationID); !ok || got.Summary != "" {
		t.Fatalf("close summary = %q (closed=%v), want it withheld", got.Summary, ok)
	}
}
