package livesession_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/secretguard"
)

// syntheticAWSKey is the fixture the publication guard tests use; split so the
// literal never appears in source.
const syntheticAWSKey = "AKIA" + "QWERTYUIOPASDFGH"

var secretLine = []byte(`{"type":"msg","content":"` + syntheticAWSKey + `"}` + "\n")

// futureLine holds a credential only the current upstream catalog knows.
var futureLine = []byte(`{"type":"msg","content":"future_synthetic"}` + "\n")

func futureRules(t *testing.T) func(context.Context) (*secretguard.RuleSet, error) {
	t.Helper()
	rules, err := secretguard.NewRules([]byte("[[rules]]\nid='future-format'\nregex='future_[a-z]+'\n"))
	if err != nil {
		t.Fatal(err)
	}
	return func(context.Context) (*secretguard.RuleSet, error) { return rules, nil }
}

// withheld asserts the drain stopped at the clean prefix: the server holds
// exactly what was acked before, the stream is disabled for the refusal, and
// the refused bytes are still in the local spool (withheld, not lost).
func (e *env) withheld(cleanAcked int64, refused []byte) {
	e.t.Helper()
	st := e.state()
	if st.Status != livesession.StatusDisabled || st.DisabledReason != "secret_refused" || st.Acked != cleanAcked {
		e.t.Fatalf("state = %+v, want disabled (secret_refused) at offset %d", st, cleanAcked)
	}
	if got := e.stored(st); !bytes.Equal(got, e.spoolBytes()[:cleanAcked]) {
		e.t.Fatalf("server holds %d bytes, want only the %d-byte clean prefix", len(got), cleanAcked)
	}
	if !bytes.Contains(e.spoolBytes(), refused) {
		e.t.Fatal("the refused bytes left the local spool")
	}
}

// Live uploads are a publication: a credential the bundled scan finds must
// never leave the machine, whatever capture-time redaction let through.
func TestDrainWithholdsBytesTheBundledScanRefuses(t *testing.T) {
	if secretguard.ValidateContent("sessions/content", secretLine) == nil {
		t.Fatal("test setup: the bundled scan accepts the fixture")
	}
	e := newEnv(t)
	e.spool(lines(0, 2))
	clean := e.mustDrain().Acked
	e.spool(secretLine)
	e.spool(lines(2, 3))

	if res, err := e.drain(); err != nil || !res.Done {
		t.Fatalf("drain: %+v %v, want a settled refusal", res, err)
	}
	e.withheld(clean, secretLine)
}

// Publication scans with the current upstream rules too; so must the stream.
func TestDrainWithholdsBytesOnlyTheCurrentRulesRefuse(t *testing.T) {
	livesession.SetCurrentRulesForTest(t, futureRules(t))
	if secretguard.ValidateContent("sessions/content", futureLine) != nil {
		t.Fatal("test setup: the bundled scan already refuses the fixture")
	}
	e := newEnv(t)
	e.spool(lines(0, 2))
	clean := e.mustDrain().Acked
	e.spool(futureLine)

	if _, err := e.drain(); err != nil {
		t.Fatalf("drain: %v", err)
	}
	e.withheld(clean, futureLine)
}

// Publication fails closed when the current rules cannot be loaded: the bytes
// wait in the spool and go once the rules are back.
func TestDrainWaitsWhileTheCurrentRulesAreUnavailable(t *testing.T) {
	down := true
	livesession.SetCurrentRulesForTest(t, func(context.Context) (*secretguard.RuleSet, error) {
		if down {
			return nil, errors.New("catalog unavailable")
		}
		return secretguard.Bundled(), nil
	})
	e := newEnv(t)
	e.spool(lines(0, 3))

	if res, err := e.drain(); err == nil || res.Done {
		t.Fatalf("drain without current rules: %+v %v, want a retryable error", res, err)
	}
	if n := e.srv.CallsTo("POST", "/lines"); n != 0 {
		t.Fatalf("%d appends sent before the scan could run", n)
	}
	if st := e.state(); st.Status != livesession.StatusStreaming || st.Acked != 0 {
		t.Fatalf("state = %+v, want the stream intact for a retry", st)
	}
	down = false
	e.mustDrain()
	if !bytes.Equal(e.stored(e.state()), e.spoolBytes()) {
		t.Fatal("the spool did not go once the rules were back")
	}
}

// The close summary leaves the machine too. A summary the scan refuses is
// withheld; the server then writes its own.
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

// Without the current rules the summary cannot be proven clean, but every
// stream byte already was: the close goes without the summary, not never.
func TestCloseWithoutCurrentRulesWithholdsOnlyTheSummary(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 2))
	e.mustDrain()
	livesession.SetCurrentRulesForTest(t, func(context.Context) (*secretguard.RuleSet, error) {
		return nil, errors.New("catalog unavailable")
	})
	res, err := livesession.Close(t.Context(), e.cfg, e.dir, "a clean summary", testBudget)
	if err != nil || res.Status != livesession.StatusClosed {
		t.Fatalf("close: %+v %v", res, err)
	}
	if got, _ := e.srv.ClosedWith(e.st.ConversationID); got.Summary != "" {
		t.Fatalf("summary %q sent without the current rules", got.Summary)
	}
}
