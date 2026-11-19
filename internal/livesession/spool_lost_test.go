package livesession_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/sessionstream"
)

// gapLine is the shape of a declared loss in the spool.
type gapLine struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
	Bytes  int64  `json:"bytes"`
}

func decodeGap(t *testing.T, line []byte) gapLine {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	var g gapLine
	if err := dec.Decode(&g); err != nil || g.Type != sessionstream.LineTypeGap {
		t.Fatalf("line %.120q is not a gap line: %v", line, err)
	}
	return g
}

// failSpoolWrite makes the next Spool of batch fail on a read-only spool file.
func failSpoolWrite(t *testing.T, dir string, batch []byte) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("reason: root ignores file permissions, so EACCES cannot be simulated")
	}
	spool := filepath.Join(dir, "spool.jsonl")
	if err := os.Chmod(spool, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(spool, 0o600) })
	if _, err := livesession.Spool(t.Context(), dir, batch, livesession.SpoolOptions{}); err == nil {
		t.Fatal("a spool append onto a read-only spool reported success")
	}
}

func writableSpool(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(filepath.Join(dir, "spool.jsonl"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// requireLocalFiles fails for any file in dir that IsLocalFile does not cover:
// the Ledger stager would copy it.
func requireLocalFiles(t *testing.T, dir, after string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, ent := range entries {
		if !livesession.IsLocalFile(ent.Name()) {
			t.Errorf("%s was written %s but is not reported by IsLocalFile, so it would reach the Ledger", ent.Name(), after)
		}
	}
}

// ADR-024: a batch the spool could not take is declared, in its place, by the
// next write; the record of it is machine-local and settled once declared.
func TestSpoolWriteFailureIsDeclaredByTheNextWrite(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 2))
	lost := lines(2, 4)
	failSpoolWrite(t, e.dir, lost)
	requireLocalFiles(t, e.dir, "for the failed spool")
	writableSpool(t, e.dir)

	res, err := livesession.Spool(t.Context(), e.dir, lines(4, 5), livesession.SpoolOptions{})
	requireLocalFiles(t, e.dir, "declaring the loss")

	if err != nil || res.Gaps != 1 {
		t.Fatalf("spool after the failure: %+v %v, want one gap declared", res, err)
	}
	spool, before, after := e.spoolBytes(), lines(0, 2), lines(4, 5)
	if !bytes.HasPrefix(spool, before) || !bytes.HasSuffix(spool, after) || len(spool) <= len(before)+len(after) {
		t.Fatalf("spool = %q, want the first batch, one gap line, then the next batch", spool)
	}
	middle := spool[len(before) : len(spool)-len(after)]
	if bytes.Count(middle, []byte{'\n'}) != 1 {
		t.Fatalf("between the batches: %q, want exactly one gap line", middle)
	}
	if g := decodeGap(t, middle); g.Reason != "spool_write_failed" || g.Bytes != int64(len(lost)) {
		t.Fatalf("gap = %+v, want spool_write_failed for %d bytes", g, len(lost))
	}
	e.spool(lines(5, 6))
	if n := bytes.Count(e.spoolBytes(), []byte(`"type":"gap"`)); n != 1 {
		t.Fatalf("%d gap lines after a later write, want the loss declared once", n)
	}
}

// The loss was the session's last batch: no write follows, so the close must
// declare it before it measures the stream, or the server publishes the
// session as complete.
func TestCloseDeclaresASpoolLossNoWriteFollowed(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 2))
	e.mustDrain()
	failSpoolWrite(t, e.dir, lines(2, 3))
	writableSpool(t, e.dir)

	res, err := livesession.Close(t.Context(), e.cfg, e.dir, "", testBudget)

	if err != nil || !res.Done {
		t.Fatalf("close: %+v %v", res, err)
	}
	spool := e.spoolBytes()
	if !bytes.Equal(e.stored(e.st), spool) {
		t.Fatal("the server does not hold the whole spool")
	}
	last := spool[bytes.LastIndexByte(spool[:len(spool)-1], '\n')+1:]
	if g := decodeGap(t, last); g.Reason != "spool_write_failed" {
		t.Fatalf("closed stream ends %q, want the declared loss", last)
	}
	if closed, _ := e.srv.ClosedWith(e.st.ConversationID); closed.StreamSHA256 != sessionstream.BytesSHA256(spool) {
		t.Fatal("close hash does not cover the gap line")
	}
}

// While the loss cannot be written into the spool, the close must not happen:
// the stream would read as complete. It stays closing until it can.
func TestCloseWithAnUndeclarableSpoolLossStaysClosing(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 2))
	e.mustDrain()
	failSpoolWrite(t, e.dir, lines(2, 3)) // the spool stays read-only

	res, err := livesession.Close(t.Context(), e.cfg, e.dir, "", testBudget)

	if err == nil || res.Done || e.state().Status != livesession.StatusClosing {
		t.Fatalf("close over an undeclared loss: %+v %v, want closing and an error", res, err)
	}
	if n := e.srv.CallsTo("POST", "/close"); n != 0 {
		t.Fatalf("close was sent %d times with a loss undeclared", n)
	}
	writableSpool(t, e.dir)
	if res, err := livesession.Close(t.Context(), e.cfg, e.dir, "", testBudget); err != nil || !res.Done {
		t.Fatalf("close once the spool was writable: %+v %v", res, err)
	}
	if !bytes.Contains(e.stored(e.st), []byte(`"reason":"spool_write_failed"`)) {
		t.Fatal("the closed stream does not declare the loss")
	}
}

// Not a loss: a session that does not stream records nothing.
func TestSpoolOfANonStreamingSessionRecordsNoLoss(t *testing.T) {
	dir := t.TempDir()
	if _, err := livesession.Spool(t.Context(), dir, lines(0, 1), livesession.SpoolOptions{}); !errors.Is(err, livesession.ErrNotStreaming) {
		t.Fatalf("err = %v, want ErrNotStreaming", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "spool-lost")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a non-streaming spool recorded a loss: %v", err)
	}
}

// holdLock takes the advisory lock on target from another goroutine until the
// returned release runs (also at cleanup).
func holdLock(t *testing.T, target string) func() {
	t.Helper()
	acquired, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- fileutil.WithFileLock(context.Background(), target, func() error {
			close(acquired)
			<-release
			return nil
		})
	}()
	select {
	case <-acquired:
	case err := <-done:
		t.Fatalf("hold the lock on %s: %v", target, err)
	}
	var once sync.Once
	rel := func() {
		once.Do(func() {
			close(release)
			if err := <-done; err != nil {
				t.Errorf("release the lock on %s: %v", target, err)
			}
		})
	}
	t.Cleanup(rel)
	return rel
}

// spoolDeclaresLoss spools next and requires exactly one gap line in front of
// it: a spool_write_failed declaring want bytes.
func (e *env) spoolDeclaresLoss(next []byte, want int64) {
	e.t.Helper()
	before := len(e.spoolBytes())
	res, err := livesession.Spool(e.t.Context(), e.dir, next, livesession.SpoolOptions{})
	if err != nil || res.Gaps != 1 {
		e.t.Fatalf("spool after the loss: %+v %v, want one gap declared", res, err)
	}
	gap, _, _ := bytes.Cut(e.spoolBytes()[before:], []byte("\n"))
	if g := decodeGap(e.t, gap); g.Reason != "spool_write_failed" || g.Bytes != want {
		e.t.Fatalf("gap = %+v, want spool_write_failed for %d bytes", g, want)
	}
}

// A batch the spool could not take because the lock directory is unusable is
// still a loss, and recording it must not need that same directory: the next
// write declares it.
func TestASpoolLossIsDeclaredWhenTheLockDirectoryIsUnusable(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 2))
	notADir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tmp := os.Getenv("TMPDIR")
	t.Setenv("TMPDIR", filepath.Join(notADir, "tmp")) // fileutil keeps lock files under os.TempDir()
	lost := lines(2, 4)

	if _, err := livesession.Spool(t.Context(), e.dir, lost, livesession.SpoolOptions{}); err == nil {
		t.Fatal("a spool with no usable lock directory reported success")
	}

	t.Setenv("TMPDIR", tmp)
	e.spoolDeclaresLoss(lines(4, 5), int64(len(lost)))
}

// A spool canceled mid-attempt fails after its batch was committed, so the
// loss must be recorded whatever the context says, even while the lock the
// loss count used to take is held.
func TestASpoolLossIsDeclaredWhenItsOperationWasCanceled(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 2))
	releaseSpool := holdLock(t, filepath.Join(e.dir, "live.lock")) // so the canceled spool fails
	releaseLost := holdLock(t, filepath.Join(e.dir, "spool-lost")) // the previous loss-count lock
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	lost := lines(2, 4)

	if _, err := livesession.Spool(ctx, e.dir, lost, livesession.SpoolOptions{}); err == nil {
		t.Fatal("a canceled spool behind a held lock reported success")
	}

	releaseSpool()
	releaseLost()
	e.spoolDeclaresLoss(lines(4, 5), int64(len(lost)))
}

// The loss files left by an earlier version, or damaged, still read as a
// pending loss: overstating one is safe, hiding one is not.
func TestALossRecordFromAnotherVersionOrDamagedIsDeclared(t *testing.T) {
	tests := []struct {
		name     string
		lost     string // spool-lost as found
		declared string // spool-lost-declared as found; empty means absent
		then     int64  // a loss recorded afterwards; zero means none
		want     int64  // bytes the next write declares
	}{
		{name: "previous version's single count", lost: "1234", want: 1234},
		{name: "previous version's count, then a new loss", lost: "1234", then: 7, want: 1241},
		{name: "corrupt count", lost: "not a count\n", want: 0},
		{name: "corrupt declared total re-declares everything", lost: "\n50\n", declared: "x", want: 50},
		{name: "declared above recorded re-declares everything", lost: "\n5\n", declared: "107", want: 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			e.spool(lines(0, 2))
			if err := os.WriteFile(filepath.Join(e.dir, "spool-lost"), []byte(tt.lost), 0o600); err != nil {
				t.Fatal(err)
			}
			if tt.declared != "" {
				if err := os.WriteFile(filepath.Join(e.dir, "spool-lost-declared"), []byte(tt.declared), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tt.then > 0 {
				if err := livesession.DeclareLost(t.Context(), e.dir, tt.then, errors.New("read back failed")); err != nil {
					t.Fatalf("declare lost: %v", err)
				}
			}

			e.spoolDeclaresLoss(lines(2, 3), tt.want)
		})
	}
}
