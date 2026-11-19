package adapterruntime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A final drain waits for the agent's last record to finish, never longer than
// asked, and never for a file with no partial record in it.
func TestWaitForCompleteRecord(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		initial string // "" with missing=true: no file at all
		missing bool
		timeout time.Duration
		want    bool
		minWait time.Duration
	}{
		{name: "missing file", missing: true, timeout: time.Second, want: true},
		{name: "empty file", timeout: time.Second, want: true},
		{name: "ends on a newline", initial: "{\"a\":1}\n", timeout: time.Second, want: true},
		{name: "record never completes", initial: "{\"a\":1}\n{\"b\":", timeout: 150 * time.Millisecond, want: false, minWait: 150 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "session.jsonl")
			if !tt.missing {
				if err := os.WriteFile(path, []byte(tt.initial), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			start := time.Now()
			got := WaitForCompleteRecord(context.Background(), path, tt.timeout)
			waited := time.Since(start)
			if got != tt.want {
				t.Fatalf("WaitForCompleteRecord = %v, want %v", got, tt.want)
			}
			if waited < tt.minWait || waited > tt.timeout+time.Second {
				t.Fatalf("waited %v, want between %v and %v", waited, tt.minWait, tt.timeout+time.Second)
			}
		})
	}
}

// A record the agent is still writing when the wait starts is waited for, and
// the wait reports it complete once the agent finishes it. The probe finishes
// the record only after the wait has seen it partial, so the order is fixed.
func TestWaitForCompleteRecordWaitsForThePartialRecordToFinish(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte("{\"a\":1}\n{\"b\":"), 0o600); err != nil {
		t.Fatal(err)
	}
	var seen []bool
	probe := func() bool {
		complete := endsOnRecordBoundary(path)
		seen = append(seen, complete)
		if !complete && len(seen) == 1 {
			finishRecord(t, path, "2}\n")
		}
		return complete
	}

	// the ceiling only bounds a broken wait: a working one returns within one
	// poll of the record finishing
	if !waitForCompleteRecord(context.Background(), 10*time.Second, probe) {
		t.Fatalf("the record was finished, but the wait reported it partial (probes saw %v)", seen)
	}
	if len(seen) < 2 || seen[0] {
		t.Fatalf("probes saw %v, want the partial record seen before the finished one", seen)
	}
}

func finishRecord(t *testing.T, path, rest string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(rest); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// A canceled caller stops waiting at once.
func TestWaitForCompleteRecordStopsWhenCanceled(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte("{\"b\":"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if WaitForCompleteRecord(ctx, path, time.Minute) {
		t.Fatal("a partial record reported complete")
	}
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("waited %v after cancellation", waited)
	}
}
