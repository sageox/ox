package livesession_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// futureCatalog is an upstream catalog with a detector the bundled one lacks.
var futureCatalog = []byte("[[rules]]\nid='future-format'\nregex='future_[a-z]+'\n")

// splitCatalog refuses split_a and split_b only when they appear together.
var splitCatalog = []byte("[[rules]]\nid='split-format'\nregex='''split_a[\\s\\S]{1,80}split_b'''\n")

var (
	splitA = []byte(`{"type":"msg","content":"split_a"}` + "\n")
	splitB = []byte(`{"type":"msg","content":"split_b"}` + "\n")
)

// keepRules does what the daemon's refresh does: fetch catalog as the current
// upstream rules and keep it in cfg's data dir.
func keepRules(t *testing.T, cfg livesession.Config, catalog []byte) {
	t.Helper()
	livesession.SetCurrentCatalogForTest(t, func(context.Context) ([]byte, error) { return catalog, nil })
	if err := livesession.RefreshSecretRules(t.Context(), cfg.DataDir); err != nil {
		t.Fatalf("refresh: %v", err)
	}
}

// padLine is one NDJSON line of exactly n bytes, newline included (n >= 24).
func padLine(n int) []byte {
	return []byte(`{"type":"msg","pad":"` + strings.Repeat("x", n-24) + `"}` + "\n")
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

// The stream scans with the last catalog a refresh kept (F2): a refresh that
// fails afterwards keeps it, even for a daemon started after the failure, so
// a credential only the current upstream catalog knows is still withheld.
func TestDrainScansWithTheLastGoodRulesAfterARefreshFails(t *testing.T) {
	e := newEnv(t)
	down := false
	livesession.SetCurrentCatalogForTest(t, func(context.Context) ([]byte, error) {
		if down {
			return nil, errors.New("catalog unavailable")
		}
		return futureCatalog, nil
	})
	if secretguard.ValidateContent("sessions/content", futureLine) != nil {
		t.Fatal("test setup: the bundled scan already refuses the fixture")
	}
	if err := livesession.RefreshSecretRules(t.Context(), e.cfg.DataDir); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	down = true
	if err := livesession.RefreshSecretRules(t.Context(), e.cfg.DataDir); err == nil {
		t.Fatal("a failed fetch reported a successful refresh")
	}
	livesession.ForgetLoadedRulesForTest() // a daemon restarted while upstream is down
	before := lines(0, 2)
	e.spool(before)
	e.mustDrain()
	if e.state().SecretWithheld {
		t.Fatal("a clean stream is marked as having withheld a secret")
	}
	e.spool(futureLine)
	e.mustDrain()

	st := e.state()
	assertSameBytes(t, "server copy", e.stored(st), joined(before, withheldGapFor(futureLine)))
	if !st.SecretWithheld {
		t.Fatal("stop has nothing to tell the person: SecretWithheld unset")
	}
}

// The CLI's stop closes the stream in a process of its own, which never
// refreshes the rules. Failure prevented: the daemon knew a detector, the
// stop's close did not, and it sent the pending transcript and the close
// summary under the bundled catalog alone.
func TestACloseInAnotherProcessScansWithTheRulesTheDaemonKept(t *testing.T) {
	e := newEnv(t)
	keepRules(t, e.cfg, futureCatalog)
	before := lines(0, 2)
	e.spool(before)
	e.mustDrain()
	livesession.ForgetLoadedRulesForTest() // the stop runs in a new process
	e.spool(futureLine)

	res, err := livesession.Close(t.Context(), e.cfg, e.dir, "rotated future_synthetic", testBudget)
	if err != nil || res.Status != livesession.StatusClosed {
		t.Fatalf("close: %+v %v", res, err)
	}
	server := e.stored(e.state())
	assertSameBytes(t, "server copy", server, joined(before, withheldGapFor(futureLine)))
	assertNotContains(t, "server copy", server, "future_synthetic")
	if got, ok := e.srv.ClosedWith(e.st.ConversationID); !ok || got.Summary != "" {
		t.Fatalf("closed=%v with a %d-byte summary, want the summary withheld", ok, len(got.Summary))
	}
}

// A kept catalog that no longer parses must not stop a stream: the scan falls
// back to the bundled catalog, says so once, and the next refresh replaces it.
func TestAnUnreadableRulesFileFallsBackToTheBundledCatalog(t *testing.T) {
	e := newEnv(t)
	logs := e.captureLogs()
	livesession.ForgetLoadedRulesForTest()
	t.Cleanup(livesession.ForgetLoadedRulesForTest)
	if err := os.WriteFile(livesession.RulesPathForTest(e.cfg), []byte("not a catalog"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := lines(0, 2)
	e.spool(before)
	e.mustDrain()
	e.spool(secretLine)
	e.mustDrain()

	assertSameBytes(t, "server copy", e.stored(e.state()), joined(before, withheldGapFor(secretLine)))
	if n := strings.Count(logs.String(), "msg=live_secret_rules_unreadable"); n != 1 {
		t.Fatalf("the unreadable rules file was logged %d times over two drains, want once", n)
	}
}

// A refused batch is withheld whole, and each refusal declares its own size:
// the gap an earlier refusal wrote leads the next batch and stays as it is.
func TestARefusedBatchIsWithheldWhole(t *testing.T) {
	all := [][]byte{lines(0, 1), secretLine}
	for i := 1; i < 300; i++ {
		all = append(all, lines(i, i+1))
	}
	all = append(all, secretLine)
	for i := 300; i < 310; i++ {
		all = append(all, lines(i, i+1))
	}
	b := batchesOf(all)
	if len(b) != 2 || !bytes.Contains(b[0], secretLine) || !bytes.Contains(b[1], secretLine) {
		t.Fatalf("test setup: want two batches with a secret in each, got %d", len(b))
	}
	e := newEnv(t)
	e.spool(bytes.Join(all, nil))
	e.mustDrain()

	assertSameBytes(t, "server copy", e.stored(e.state()), joined(withheldGapFor(b[0]), withheldGapFor(b[1])))
}

// A Helm extraEnv name and its value in separate records refuse only
// together, under the bundled catalog. Failure prevented: the name record
// was also refused alone by a current-only rule, so only it was withheld, and
// the value streamed without the name that made it a credential.
func TestAHelmNameAndItsValueInSeparateRecordsAreWithheldTogether(t *testing.T) {
	e := newEnv(t)
	keepRules(t, e.cfg, futureCatalog)
	name := []byte(`{"type":"msg","content":"app.extraEnv[0].name='SERVICE_KEY' future_synthetic"}` + "\n")
	value := []byte(`{"type":"msg","content":"app.extraEnv[0].value='simplecredential'"}` + "\n")
	if secretguard.ValidateContent("sessions/content", joined(name, value)) == nil ||
		livesession.CheckPublicationForTest(e.cfg, value) != nil ||
		secretguard.ValidateContent("sessions/content", name) != nil ||
		livesession.CheckPublicationForTest(e.cfg, name) == nil {
		t.Fatal("test setup: want the pair refused by the bundled catalog, the value alone accepted, the name refused only by the current rules")
	}
	refused := joined(lines(0, 1), name, value, lines(1, 2))
	after := lines(2, 3)
	e.spool(refused)
	e.mustDrain()
	e.spool(after)
	e.mustDrain()

	server := e.stored(e.state())
	assertSameBytes(t, "server copy", server, joined(withheldGapFor(refused), after))
	assertNotContains(t, "server copy", server, "simplecredential")
}

// A match that spans lines refuses no line on its own. The whole batch is
// withheld as one gap: bytes the scan refused never go out piecemeal.
func TestARefusalAcrossLinesWithholdsTheWholeBatch(t *testing.T) {
	e := newEnv(t)
	keepRules(t, e.cfg, splitCatalog)
	if livesession.CheckPublicationForTest(e.cfg, splitA) != nil || livesession.CheckPublicationForTest(e.cfg, splitB) != nil ||
		livesession.CheckPublicationForTest(e.cfg, joined(splitA, splitB)) == nil {
		t.Fatal("test setup: want the rule to refuse the two halves only together")
	}
	refused := joined(lines(0, 1), splitA, splitB, lines(1, 2))
	e.spool(refused)
	e.mustDrain()
	after := lines(2, 3)
	e.spool(after)
	e.mustDrain()

	assertSameBytes(t, "server copy", e.stored(e.state()), joined(withheldGapFor(refused), after))
}

// A batch holding a refused line and, at its very end, a cross-line pair.
// Failure prevented: replacing only the refused line with a longer gap line
// pushed the pair's second half into the next batch, so no scan saw the two
// halves together and both went out.
func TestASplitMatchIsNeverPushedIntoTheNextBatch(t *testing.T) {
	e := newEnv(t)
	keepRules(t, e.cfg, splitCatalog)
	first := lines(0, 1)
	remaining := sessionstream.MaxBatchBytes - len(first) - len(secretLine) - len(splitA) - len(splitB)
	refused := [][]byte{first, secretLine}
	for i := 10; ; i++ {
		l := lines(i, i+1)
		if remaining-len(l) < 100 {
			break
		}
		refused, remaining = append(refused, l), remaining-len(l)
	}
	refused = append(refused, padLine(remaining), splitA, splitB)
	after := lines(1000, 1002)
	all := append(append([][]byte{}, refused...), after)
	if b := batchesOf(all); len(b) != 2 || !bytes.Equal(b[0], bytes.Join(refused, nil)) {
		t.Fatal("test setup: want the pair to end the first batch exactly")
	}
	shifted := append([][]byte{first, withheldGapFor(secretLine)}, refused[2:]...)
	if b := batchesOf(append(shifted, after)); !bytes.HasSuffix(b[0], splitA) {
		t.Fatal("test setup: want a gap for the refused line alone to push the pair's second half out of the batch")
	}
	e.spool(bytes.Join(all, nil))
	e.mustDrain()

	server := e.stored(e.state())
	assertSameBytes(t, "server copy", server, joined(withheldGapFor(bytes.Join(refused, nil)), after))
	assertNotContains(t, "server copy", server, "split_b")
}

// A withhold rewrites only the spool's unsent tail, in place. Failure
// prevented: every refusal copied the whole spool, history the server already
// held included, and synced it while appends waited on the swap lock, a cost
// that grew with the session until a hook's one-second budget ran out.
func TestAWithholdRewritesOnlyTheUnsentTail(t *testing.T) {
	e := newEnv(t)
	history := lines(0, 600) // acknowledged: about two batches
	e.spool(history)
	e.mustDrain()
	e.spool(secretLine)
	spool := filepath.Join(e.dir, "spool.jsonl")
	before, err := os.Stat(spool)
	if err != nil {
		t.Fatal(err)
	}
	e.mustDrain()

	after, err := os.Stat(spool)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("the withhold replaced the whole spool file instead of rewriting its unsent tail")
	}
	assertSameBytes(t, "spool", e.spoolBytes(), joined(history, withheldGapFor(secretLine)))
}

// The tail is cut before it is written back. A write that fails in between
// (or a crash) must leave a declared gap, never a spool that silently lost
// its unsent tail.
func TestAWriteFailingAfterTheCutDeclaresTheTailLost(t *testing.T) {
	e := newEnv(t)
	before, after := lines(0, 2), lines(2, 3)
	e.spool(before)
	e.mustDrain()
	e.spool(secretLine)
	livesession.SetSpoolRewriteHookForTest(t, func() error { return errors.New("disk full") })
	if _, err := e.drain(); err == nil {
		t.Fatal("a failed rewrite reported success")
	}
	livesession.SetSpoolRewriteHookForTest(t, nil)
	e.spool(after)
	e.mustDrain()

	lost := fmt.Appendf(nil, `{"type":"gap","reason":"spool_write_failed","bytes":%d}`+"\n", len(secretLine))
	assertSameBytes(t, "server copy", e.stored(e.state()), joined(before, lost, after))
}

// appendDuringRewrite appends late through path while a drain of e withholds
// a batch, from inside the rewrite. It returns once the append either finished
// (a broken exclusion) or is about to wait for the swap lock.
func appendDuringRewrite(t *testing.T, path string, late []byte) <-chan error {
	t.Helper()
	waiting := make(chan struct{}, 1)
	livesession.SetSpoolSwapWaitHookForTest(t, func() {
		select {
		case waiting <- struct{}{}:
		default:
		}
	})
	appended := make(chan error, 1)
	livesession.SetSpoolRewriteHookForTest(t, func() error {
		go func() {
			_, err := livesession.Spool(context.Background(), path, late, livesession.SpoolOptions{})
			appended <- err
		}()
		// the drain holds live.lock, so the append gives up on it after
		// spoolLockWait and waits for the swap lock this rewrite holds
		select {
		case <-waiting:
		case err := <-appended:
			appended <- err // it never waited: the spool check after the drain fails
		}
		return nil
	})
	return appended
}

// A hook that finds live.lock busy appends without it. A drain withholding a
// batch cuts and rewrites the spool's tail meanwhile; an append landing in
// between would be overwritten, lost with no gap line. The swap lock makes it
// wait for the rewrite and land after it.
func TestAnAppendDuringAWithholdLandsAfterTheRewrite(t *testing.T) {
	e := newEnv(t)
	before, late := lines(0, 2), lines(2, 3)
	e.spool(before)
	e.mustDrain()
	e.spool(secretLine)

	appended := appendDuringRewrite(t, e.dir, late)
	e.mustDrain()
	if err := <-appended; err != nil {
		t.Fatalf("append during the withhold: %v", err)
	}
	e.mustDrain()

	want := joined(before, withheldGapFor(secretLine), late)
	assertSameBytes(t, "spool", e.spoolBytes(), want)
	assertSameBytes(t, "server copy", e.stored(e.state()), want)
}

// The daemon drains a session folder by its resolved path while a hook names
// it through a symlinked cache. Failure prevented: the two spellings took
// two different live.lock locks, so the capture appended without waiting for
// the swap lock, inside the rewrite, and the entry was lost with no gap line.
func TestAnAppendThroughASymlinkedFolderWaitsForTheRewrite(t *testing.T) {
	e := newEnv(t)
	alias := filepath.Join(t.TempDir(), "cache-link")
	if err := os.Symlink(e.dir, alias); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
	before, late := lines(0, 2), lines(2, 3)
	e.spool(before)
	e.mustDrain()
	e.spool(secretLine)

	appended := appendDuringRewrite(t, alias, late)
	e.mustDrain()
	if err := <-appended; err != nil {
		t.Fatalf("append through the symlink during the withhold: %v", err)
	}
	e.mustDrain()

	want := joined(before, withheldGapFor(secretLine), late)
	assertSameBytes(t, "spool", e.spoolBytes(), want)
	assertSameBytes(t, "server copy", e.stored(e.state()), want)
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
		t.Fatalf("closed=%v with a %d-byte summary, want it withheld", ok, len(got.Summary))
	}
}
