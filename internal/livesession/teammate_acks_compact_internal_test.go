package livesession

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/sessionstream"
)

// loggedAcks is the ack log as written, one parsed record per line.
func loggedAcks(t *testing.T, dir string) []ackLogLine {
	t.Helper()
	lines, err := readBoundedLines(filepath.Join(dir, teammateAcksFileName))
	if err != nil {
		t.Fatalf("read ack log: %v", err)
	}
	out := make([]ackLogLine, 0, len(lines))
	for _, l := range lines {
		var a ackLogLine
		if err := json.Unmarshal([]byte(l), &a); err != nil {
			t.Fatalf("unparsable ack log line %q: %v", l, err)
		}
		out = append(out, a)
	}
	return out
}

func owedAcks(t *testing.T, dir string) []sessionstream.TeammateCommentAck {
	t.Helper()
	owed, err := ackObligations(dir)
	if err != nil {
		t.Fatalf("ackObligations: %v", err)
	}
	out := make([]sessionstream.TeammateCommentAck, len(owed))
	for i, o := range owed {
		out[i] = o.ack
	}
	return out
}

func settleIDs(t *testing.T, dir string, ids ...string) {
	t.Helper()
	acks := make([]sessionstream.TeammateCommentAck, len(ids))
	for i, id := range ids {
		acks[i] = sessionstream.TeammateCommentAck{ID: id, DeliveredAtSeq: 1}
	}
	if err := markTeammateAcksSent(dir, acks); err != nil {
		t.Fatalf("settle: %v", err)
	}
}

// Failure prevented: a long-lived conversation's receipt history passing the
// read bound, after which no drain and no closed-session retry could send any
// ack again, though every earlier ack had landed.
func TestReceiptHistoryNeverOutgrowsItsReadBound(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		receipts int
		idPad    int
	}{
		// many short receipts cross the line bound
		{name: "line bound", receipts: maxAckLogLines + 100},
		// few long receipts cross the byte bound, far below the line trigger
		{name: "byte bound", receipts: maxAckLogBytes/1000 + 50, idPad: 1000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			id := func(i int) string { return fmt.Sprintf("tcm_%05d%s", i, strings.Repeat("x", tt.idPad)) }
			// an early receipt that is never settled must survive every compaction
			const keptEarly = 3
			want := []sessionstream.TeammateCommentAck{{ID: id(keptEarly), DeliveredAtSeq: keptEarly + 1}}
			var batch []sessionstream.TeammateCommentAck
			for i := range tt.receipts {
				a := sessionstream.TeammateCommentAck{ID: id(i), DeliveredAtSeq: int64(i + 1)}
				if err := RecordTeammateCommentAck(dir, a.ID, a.DeliveredAtSeq); err != nil {
					t.Fatalf("receipt %d: %v", i, err)
				}
				if i == keptEarly {
					continue
				}
				batch = append(batch, a)
				if len(batch) == sessionstream.MaxTeammateCommentAcks {
					if err := markTeammateAcksSent(dir, batch); err != nil {
						t.Fatalf("settle: %v", err)
					}
					batch = nil
				}
			}
			want = append(want, batch...)
			if got := owedAcks(t, dir); !slices.Equal(got, want) {
				t.Fatalf("owed %d acks, want exactly the %d unsettled ones: %v", len(got), len(want), got)
			}
		})
	}
}

// Failure prevented: compaction rewriting what a reader relies on, either the
// first recorded seq and time of a receipt still owed, or the record of a
// settled ack the server listed again and so must be resent.
func TestAckLogCompactionKeepsEveryReceiptStillOwed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	record := func(id string, seq int64) {
		t.Helper()
		if err := RecordTeammateCommentAck(dir, id, seq); err != nil {
			t.Fatalf("record %s: %v", id, err)
		}
	}
	record("tcm_owed", 5)
	record("tcm_resend", 6)
	record("tcm_done", 7)
	settleIDs(t, dir, "tcm_resend", "tcm_done")
	if err := addTeammateResend(dir, "tcm_resend", time.Now()); err != nil {
		t.Fatal(err)
	}
	// a later record of the same note never replaces the first
	record("tcm_owed", 99)
	first := loggedAcks(t, dir)[0]

	// settled filler brings the log to exactly the trigger; the next receipt
	// compacts before it appends
	var filler []string
	for i := len(loggedAcks(t, dir)); i < maxAckLogLines/2; i++ {
		id := fmt.Sprintf("tcm_filler_%04d", i)
		record(id, int64(100+i))
		filler = append(filler, id)
	}
	settleIDs(t, dir, filler...)
	record("tcm_next", 5000)

	logged := loggedAcks(t, dir)
	wantLog := []ackLogLine{
		{ID: "tcm_owed", DeliveredAtSeq: 5, At: first.At},
		{ID: "tcm_resend", DeliveredAtSeq: 6},
		{ID: "tcm_next", DeliveredAtSeq: 5000},
	}
	if len(logged) != len(wantLog) {
		t.Fatalf("compacted log holds %d records, want %d", len(logged), len(wantLog))
	}
	for i, w := range wantLog {
		g := logged[i]
		if g.ID != w.ID || g.DeliveredAtSeq != w.DeliveredAtSeq || (!w.At.IsZero() && !g.At.Equal(w.At)) {
			t.Errorf("record %d = %+v, want %+v", i, g, w)
		}
	}

	wantOwed := []sessionstream.TeammateCommentAck{
		{ID: "tcm_owed", DeliveredAtSeq: 5},
		{ID: "tcm_next", DeliveredAtSeq: 5000},
		{ID: "tcm_resend", DeliveredAtSeq: 6},
	}
	if got := owedAcks(t, dir); !slices.Equal(got, wantOwed) {
		t.Errorf("owed %v, want %v", got, wantOwed)
	}

	shown, err := shownTeammateAcks(dir)
	if err != nil {
		t.Fatalf("shownTeammateAcks: %v", err)
	}
	if got := slices.Sorted(maps.Keys(shown)); !slices.Equal(got, []string{"tcm_next", "tcm_owed", "tcm_resend"}) {
		t.Errorf("shown %v", got)
	}
	if shown["tcm_owed"].DeliveredAtSeq != 5 {
		t.Errorf("shown seq of tcm_owed = %d, want the first recorded 5", shown["tcm_owed"].DeliveredAtSeq)
	}

	// a settled id survives only while its receipt does
	settled, err := readBoundedLines(filepath.Join(dir, teammateAcksSentFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(settled, []string{"tcm_resend"}) {
		t.Errorf("settled list holds %d ids, want only tcm_resend", len(settled))
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		switch e.Name() {
		case teammateAcksFileName, teammateAcksSentFileName, teammateResendsFileName:
		default:
			t.Errorf("compaction left %s behind", e.Name())
		}
	}
}

// Failure prevented: a compaction failure becoming a lost receipt. The caller
// treats an error as "not logged" and keeps the note shown with no ack owed.
func TestAReceiptIsLoggedWhenCompactionCannotRun(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, teammateAcksFileName), strings.Repeat(`{"id":"tcm_old","delivered_at_seq":1}`+"\n", maxAckLogLines/2))
	// an unreadable settled list: compaction cannot tell what is settled
	if err := os.Mkdir(filepath.Join(dir, teammateAcksSentFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := RecordTeammateCommentAck(dir, "tcm_new", 2); err != nil {
		t.Fatalf("a compaction failure failed the receipt: %v", err)
	}
	logged := loggedAcks(t, dir)
	if len(logged) != maxAckLogLines/2+1 || logged[len(logged)-1].ID != "tcm_new" {
		t.Fatalf("receipt not appended: %d records", len(logged))
	}
}
