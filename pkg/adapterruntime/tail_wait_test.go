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
		name     string
		initial  string // "" with missing=true: no file at all
		missing  bool
		complete string // appended after 100 ms, if set
		timeout  time.Duration
		want     bool
		minWait  time.Duration
	}{
		{name: "missing file", missing: true, timeout: time.Second, want: true},
		{name: "empty file", timeout: time.Second, want: true},
		{name: "ends on a newline", initial: "{\"a\":1}\n", timeout: time.Second, want: true},
		{name: "record completes in time", initial: "{\"a\":1}\n{\"b\":", complete: "2}\n", timeout: 2 * time.Second, want: true, minWait: 100 * time.Millisecond},
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
			if tt.complete != "" {
				go func() {
					time.Sleep(100 * time.Millisecond)
					f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
					if err != nil {
						t.Error(err)
						return
					}
					if _, err := f.WriteString(tt.complete); err != nil {
						t.Error(err)
					}
					if err := f.Close(); err != nil {
						t.Error(err)
					}
				}()
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
