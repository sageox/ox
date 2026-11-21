package livesession

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/sessionstream"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A receipt log is read from a place anyone may have written to once, so every
// way it can be unreadable must be an error, never an empty queue.
func TestReceiptLogReaderRefusesWhatItCannotTrust(t *testing.T) {
	dir := t.TempDir()
	if lines, err := readBoundedLines(filepath.Join(dir, "absent")); err != nil || lines != nil {
		t.Fatalf("a missing log is an empty log: %v, %v", lines, err)
	}
	if err := os.Mkdir(filepath.Join(dir, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundedLines(filepath.Join(dir, "dir")); err == nil {
		t.Fatal("a directory was read as a log")
	}
	big := filepath.Join(dir, "big")
	writeFile(t, big, strings.Repeat("x", maxAckLogBytes+1))
	if _, err := readBoundedLines(big); !errors.Is(err, ErrAckLogTooLarge) {
		t.Fatalf("oversized by bytes: %v", err)
	}
	many := filepath.Join(dir, "many")
	writeFile(t, many, strings.Repeat("a\n", maxAckLogLines+1))
	if _, err := readBoundedLines(many); !errors.Is(err, ErrAckLogTooLarge) {
		t.Fatalf("oversized by lines: %v", err)
	}
	blank := filepath.Join(dir, "blank")
	writeFile(t, blank, "a\n\n  \nb\n")
	if lines, err := readBoundedLines(blank); err != nil || len(lines) != 2 {
		t.Fatalf("blank lines kept: %v, %v", lines, err)
	}
	// an unreadable file (no permission) is an error too
	locked := filepath.Join(dir, "locked")
	writeFile(t, locked, "a\n")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != 0 {
		if _, err := readBoundedLines(locked); err == nil {
			t.Fatal("an unreadable log read as empty")
		}
	}
}

func TestEveryReceiptReaderReportsAnUnreadableLog(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{teammateAcksFileName, teammateAcksSentFileName, teammateResendsFileName} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := readAckLog(dir); err == nil {
		t.Error("readAckLog")
	}
	if _, err := readSettled(dir); err == nil {
		t.Error("readSettled")
	}
	if _, _, err := readResends(dir); err == nil {
		t.Error("readResends")
	}
	if _, err := ackObligations(dir); err == nil {
		t.Error("ackObligations")
	}
	if _, err := shownTeammateAcks(dir); err == nil {
		t.Error("shownTeammateAcks")
	}
	if err := addTeammateResend(dir, "x", time.Now()); err == nil {
		t.Error("addTeammateResend")
	}
	if err := clearTeammateResends(dir, []sessionstream.TeammateCommentAck{{ID: "x"}}); err == nil {
		t.Error("clearTeammateResends")
	}
	if err := markTeammateAcksSent(dir, nil); err == nil {
		t.Error("markTeammateAcksSent")
	}
}

func TestReceiptLogParsingKeepsTheFirstRecordAndDatesLegacyLines(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, teammateAcksFileName),
		"not json\n"+
			`{"id":"","delivered_at_seq":3}`+"\n"+
			`{"id":"a","delivered_at_seq":0}`+"\n"+
			`{"id":"a","delivered_at_seq":5}`+"\n"+ // legacy: no timestamp
			`{"id":"a","delivered_at_seq":9,"at":"2026-01-02T03:04:05Z"}`+"\n"+
			`{"id":"b","delivered_at_seq":6,"at":"2026-01-02T03:04:05Z"}`+"\n")
	by, order, err := readAckLog(dir)
	if err != nil || len(order) != 2 || order[0] != "a" {
		t.Fatalf("order = %v, %v", order, err)
	}
	if by["a"].ack.DeliveredAtSeq != 5 {
		t.Fatalf("the first record must win: %+v", by["a"])
	}
	if by["a"].since.IsZero() {
		t.Fatal("a line with no timestamp must count from the file's last write")
	}
	if by["b"].since.Year() != 2026 {
		t.Fatalf("since = %v", by["b"].since)
	}
	writeFile(t, filepath.Join(dir, teammateResendsFileName), "a\t100\na\t200\nb\n")
	since, rorder, err := readResends(dir)
	if err != nil || len(rorder) != 2 || since["a"].Unix() != 100 || since["b"].IsZero() {
		t.Fatalf("resends = %v %v %v", since, rorder, err)
	}
}

func TestResendListIsAppendOnceAndClearsOnlySettledIDs(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1000, 0)
	for _, id := range []string{"a", "b", "a"} {
		if err := addTeammateResend(dir, id, now); err != nil {
			t.Fatal(err)
		}
	}
	_, order, _ := readResends(dir)
	if len(order) != 2 {
		t.Fatalf("a duplicate resend was recorded: %v", order)
	}
	// nothing settled: the file is left as it was
	if err := clearTeammateResends(dir, []sessionstream.TeammateCommentAck{{ID: "zzz"}}); err != nil {
		t.Fatal(err)
	}
	if _, order, _ = readResends(dir); len(order) != 2 {
		t.Fatalf("an unrelated settle changed the list: %v", order)
	}
	if err := clearTeammateResends(dir, []sessionstream.TeammateCommentAck{{ID: "a"}}); err != nil {
		t.Fatal(err)
	}
	if _, order, _ = readResends(dir); len(order) != 1 || order[0] != "b" {
		t.Fatalf("after settling a: %v", order)
	}
	if err := clearTeammateResends(dir, []sessionstream.TeammateCommentAck{{ID: "b"}}); err != nil {
		t.Fatal(err)
	}
	if _, order, _ = readResends(dir); len(order) != 0 {
		t.Fatalf("after settling b: %v", order)
	}
	// an empty list has nothing to clear
	if err := clearTeammateResends(t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestReceiptWritersFailLoudlyWhenTheFolderCannotBeMade(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	writeFile(t, blocker, "x")
	under := filepath.Join(blocker, "sub") // a path below a regular file
	if RecordTeammateCommentAck(under, "a", 1) == nil {
		t.Error("RecordTeammateCommentAck")
	}
	if CheckTeammateAckLog(under) == nil {
		t.Error("CheckTeammateAckLog")
	}
	if addTeammateResend(under, "a", time.Now()) == nil {
		t.Error("addTeammateResend")
	}
	if markTeammateAcksSent(under, []sessionstream.TeammateCommentAck{{ID: "a"}}) == nil {
		t.Error("markTeammateAcksSent")
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, teammateAcksFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	if RecordTeammateCommentAck(dir, "a", 1) == nil || CheckTeammateAckLog(dir) == nil {
		t.Error("an ack log that is a directory was written")
	}
}

func TestReceiptsDirRefusesAPathShapedConversationID(t *testing.T) {
	if _, err := ReceiptsDir(Config{Endpoint: "http://127.0.0.1:1", DataDir: t.TempDir()}, "../x"); err == nil {
		t.Fatal("accepted")
	}
	if _, err := ReceiptsDir(Config{Endpoint: "http://127.0.0.1:1", DataDir: t.TempDir()}, "cnv_01a1f5e1-6ea2-7635-bbd0-25410e9ed413"); err != nil {
		t.Fatal(err)
	}
}

// With no client (a stream that was not authorized) nothing is sent; an
// unreadable log is logged and sends nothing either.
func TestSendTeammateAcksDoesNothingWithoutAClientOrAReadableLog(t *testing.T) {
	var logs bytes.Buffer
	cfg, _ := Config{Endpoint: "http://127.0.0.1:1", DataDir: t.TempDir(), Logger: slog.New(slog.NewTextHandler(&logs, nil))}.withDefaults()
	s := &session{cfg: cfg, dir: t.TempDir(), st: State{ConversationID: "cnv_01a1f5e1-6ea2-7635-bbd0-25410e9ed413"}}
	s.sendTeammateAcks(t.Context()) // c == nil
	s.c = &sessionstream.Client{BaseURL: "http://127.0.0.1:1"}
	rdir, _ := s.receipts()
	if err := os.MkdirAll(filepath.Join(rdir, teammateAcksFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	s.sendTeammateAcks(t.Context())
	if !strings.Contains(logs.String(), "live_teammate_acks_unreadable") {
		t.Fatalf("an unreadable log was not reported: %q", logs.String())
	}
	bad := &session{cfg: cfg, dir: t.TempDir(), st: State{ConversationID: "nope"}, c: s.c}
	bad.sendTeammateAcks(t.Context())
	bad.settleTeammateAcks(t.Context(), []sessionstream.TeammateCommentAck{{ID: "a"}})
	if _, err := bad.receipts(); err == nil {
		t.Fatal("a bad conversation id has receipts")
	}
	// settle failing to write marks nothing and says so
	logs.Reset()
	if err := os.RemoveAll(filepath.Join(rdir, teammateAcksFileName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(rdir, teammateAcksSentFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	s.settleTeammateAcks(t.Context(), []sessionstream.TeammateCommentAck{{ID: "a"}})
	if !strings.Contains(logs.String(), "live_teammate_acks_mark_failed") {
		t.Fatalf("a failed mark was not reported: %q", logs.String())
	}
	if err := os.Remove(filepath.Join(rdir, teammateAcksSentFileName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(rdir, teammateResendsFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	logs.Reset()
	s.settleTeammateAcks(t.Context(), []sessionstream.TeammateCommentAck{{ID: "a"}})
	if !strings.Contains(logs.String(), "live_teammate_resend_clear_failed") {
		t.Fatalf("a failed resend clear was not reported: %q", logs.String())
	}
}

func TestClosedStreamAndFlushIgnoreWhatIsNotAClosedMintedStream(t *testing.T) {
	cfg := Config{Endpoint: "http://127.0.0.1:1", DataDir: t.TempDir(), KeyDir: t.TempDir()}
	dir := t.TempDir()
	// no state at all
	if owed, err := TeammateAcksOwed(t.Context(), cfg, dir); owed || err == nil {
		t.Fatalf("no state: %v, %v", owed, err)
	}
	if attempted, err := FlushTeammateAcks(t.Context(), cfg, dir, time.Second, time.Now()); attempted || err != nil {
		t.Fatalf("flush with no state: %v, %v", attempted, err)
	}
	// a stream that is still streaming owes nothing here
	st := State{ConversationID: "cnv_01a1f5e1-6ea2-7635-bbd0-25410e9ed413", Status: StatusStreaming, Endpoint: "http://x"}
	if err := writeState(dir, st); err != nil {
		t.Fatal(err)
	}
	if owed, err := TeammateAcksOwed(t.Context(), cfg, dir); owed || err != nil {
		t.Fatalf("streaming: %v, %v", owed, err)
	}
	// closed, but no receipt log was ever written for it
	st.Status = StatusClosed
	if err := writeState(dir, st); err != nil {
		t.Fatal(err)
	}
	if attempted, err := FlushTeammateAcks(t.Context(), cfg, dir, time.Second, time.Now()); attempted || err != nil {
		t.Fatalf("closed, no receipts: %v, %v", attempted, err)
	}
	if hasReceiptLog(t.TempDir()) {
		t.Fatal("an empty folder has a receipt log")
	}
}
