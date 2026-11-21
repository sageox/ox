package livesession

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/sessionstream"
)

const (
	goodCnv   = "cnv_01a1f5e1-6ea2-7635-bbd0-25410e9ed413"
	forgedCnv = "cnv_forged"
)

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("a read-only folder does not stop root")
	}
}

// closedMintedStream is a closed stream this machine minted, with an ack logged
// and no key on disk, so nothing can reach a server.
func closedMintedStream(t *testing.T, cnv string) (Config, string) {
	t.Helper()
	cfg, err := Config{Endpoint: "http://127.0.0.1:1", DataDir: t.TempDir(), KeyDir: t.TempDir()}.withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := recordMinted(t.Context(), cfg, cnv, dir); err != nil {
		t.Fatal(err)
	}
	if err := writeState(dir, State{ConversationID: cnv, Status: StatusClosed, Endpoint: cfg.Endpoint}); err != nil {
		t.Fatal(err)
	}
	return cfg, dir
}

// Failure prevented: a half-configured caller (no endpoint) reading as "nothing
// owed" instead of being told its configuration is unusable.
func TestAckEntryPointsRefuseAConfigWithoutAnEndpoint(t *testing.T) {
	if _, err := ReceiptsDir(Config{}, goodCnv); err == nil {
		t.Error("ReceiptsDir")
	}
	if _, err := TeammateAcksOwed(t.Context(), Config{}, t.TempDir()); err == nil {
		t.Error("TeammateAcksOwed")
	}
	if _, err := FlushTeammateAcks(t.Context(), Config{}, t.TempDir(), time.Second, time.Now()); err == nil {
		t.Error("FlushTeammateAcks")
	}
}

// Failure prevented: an unreadable settled list reading as "everything is
// still owed" (resending acks the server already has) or as "nothing owed".
func TestAnUnreadableSettledListIsAnErrorNotAnEmptyOne(t *testing.T) {
	dir := t.TempDir()
	if err := RecordTeammateCommentAck(dir, "tcm_1", 3); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, teammateAcksSentFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ackObligations(dir); err == nil {
		t.Fatal("obligations were computed past an unreadable settled list")
	}
}

// Failure prevented: a state whose conversation id cannot name a receipts
// folder being treated as having no receipts.
func TestAStateWithAnIdThatCannotNameReceiptsIsRefused(t *testing.T) {
	cfg, dir := closedMintedStream(t, forgedCnv)
	if owed, err := TeammateAcksOwed(t.Context(), cfg, dir); owed || err == nil {
		t.Fatalf("owed=%v err=%v", owed, err)
	}
}

// Failure prevented: the state read by the cheap preflight differing from the
// one authorized under the lock (a swap in between). The locked read decides.
func TestFlushRefusesAStateSwappedBetweenPreflightAndLock(t *testing.T) {
	cfg, dir := closedMintedStream(t, goodCnv)
	rdir, err := ReceiptsDir(cfg, goodCnv)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordTeammateCommentAck(rdir, "tcm_1", 5); err != nil {
		t.Fatal(err)
	}
	if err := recordMinted(t.Context(), cfg, forgedCnv, dir); err != nil {
		t.Fatal(err)
	}
	flushPreflightHook = func() {
		if err := writeState(dir, State{ConversationID: forgedCnv, Status: StatusClosed, Endpoint: cfg.Endpoint}); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { flushPreflightHook = nil })
	if attempted, err := FlushTeammateAcks(t.Context(), cfg, dir, time.Second, time.Now()); attempted || err == nil {
		t.Fatalf("attempted=%v err=%v", attempted, err)
	}
}

// Failure prevented: an owed ack with no signing key silently marked settled.
func TestFlushReportsAnOwedAckItCannotSignFor(t *testing.T) {
	cfg, dir := closedMintedStream(t, goodCnv)
	rdir, err := ReceiptsDir(cfg, goodCnv)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordTeammateCommentAck(rdir, "tcm_1", 5); err != nil {
		t.Fatal(err)
	}
	attempted, err := FlushTeammateAcks(t.Context(), cfg, dir, time.Second, time.Now())
	if err == nil || !strings.Contains(err.Error(), "live client") {
		t.Fatalf("err = %v", err)
	}
	if !attempted {
		t.Fatal("receipts were owed, so the sweep must count this session")
	}
	if owed, err := TeammateAcksOwed(t.Context(), cfg, dir); err != nil || !owed {
		t.Fatalf("the ack must stay owed: %v, %v", owed, err)
	}
}

// Failure prevented: a receipt folder the user cannot write reading as success.
func TestResendListWritersFailLoudlyInAReadOnlyFolder(t *testing.T) {
	skipIfRoot(t)
	t.Run("folder cannot be made", func(t *testing.T) {
		parent := t.TempDir()
		if err := os.Chmod(parent, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
		if addTeammateResend(filepath.Join(parent, "sub"), "a", time.Now()) == nil {
			t.Fatal("made a folder in a read-only parent")
		}
	})
	t.Run("list cannot be created", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		if addTeammateResend(dir, "a", time.Now()) == nil {
			t.Fatal("created a list in a read-only folder")
		}
	})
	t.Run("rewritten list cannot be staged", func(t *testing.T) {
		dir := t.TempDir()
		now := time.Now()
		if err := addTeammateResend(dir, "a", now); err != nil {
			t.Fatal(err)
		}
		if err := addTeammateResend(dir, "b", now); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		if clearTeammateResends(dir, []sessionstream.TeammateCommentAck{{ID: "a"}}) == nil {
			t.Fatal("cleared a list it could not rewrite")
		}
	})
}

func TestAckLogCheckCreatesTheLogWithoutWritingToIt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "receipts")
	if err := CheckTeammateAckLog(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, teammateAcksFileName))
	if err != nil || info.Size() != 0 {
		t.Fatalf("the check must leave an empty log: %v, %v", info, err)
	}
}

func TestAckLogCheckFailsWhenTheLogCannotBeOpened(t *testing.T) {
	skipIfRoot(t)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, teammateAcksFileName), "")
	if err := os.Chmod(filepath.Join(dir, teammateAcksFileName), 0o400); err != nil {
		t.Fatal(err)
	}
	if CheckTeammateAckLog(dir) == nil {
		t.Fatal("a log that cannot be appended to passed the check")
	}
	if RecordTeammateCommentAck(dir, "a", 1) == nil {
		t.Fatal("recorded into a log that cannot be appended to")
	}
}
