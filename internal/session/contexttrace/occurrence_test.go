package contexttrace

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/sageox/ox/internal/sessionid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: two uses on one turn (including unrecorded Seq=0) receive
// one analytics identity, or concurrent CLI writers reuse an occurrence ordinal.
func TestAppendUse_UniquePersistedOccurrences(t *testing.T) {
	for _, scope := range []string{"", "ses_019ff2f5-2079-7be1-b05e-8caad2772e61"} {
		t.Run(scope, func(t *testing.T) {
			w := NewWriter(t.TempDir())
			const n = 8
			var wg sync.WaitGroup
			errs := make(chan error, n)
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, err := NewFileWriter(w.Path()).AppendUse(context.Background(), Event{Type: EventUsed, Seq: 0, Timestamp: "2026-10-31T12:00:00Z", Evidence: EvidenceNone}, scope)
					errs <- err
				}()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				require.NoError(t, err)
			}
			events, err := ReadEvents(w.Path())
			require.NoError(t, err)
			require.Len(t, events, n)
			assert.True(t, sessionid.IsValidSessionID(events[0].SessionID))
			for i, ev := range events {
				assert.Equal(t, events[0].SessionID, ev.SessionID)
				if scope != "" {
					assert.Equal(t, scope, ev.SessionID)
				}
				assert.Equal(t, i+1, ev.OccurrenceSeq)
				assert.Equal(t, 0, ev.Seq)
				assert.Equal(t, "2026-10-31T12:00:00Z", ev.Timestamp)
			}
			if scope == "" {
				require.NoError(t, os.Remove(w.Path()))
				fresh, err := w.AppendUse(context.Background(), Event{Type: EventUsed}, "")
				require.NoError(t, err)
				assert.NotEqual(t, events[0].SessionID, fresh.SessionID)
				assert.Equal(t, 1, fresh.OccurrenceSeq)
			}
		})
	}
}

// Failure prevented: invalid use records enter the trace, or an unsuccessful
// append returns an occurrence a caller could publish as if it were persisted.
func TestAppendUse_RejectsInvalidAndUnwritableRecords(t *testing.T) {
	for _, tt := range []struct {
		name          string
		event         Event
		sessionID     string
		missingParent bool
		blockedParent bool
		want          string
	}{
		// Failure prevented: another trace event is allocated a use identity.
		{name: "wrong event type", event: Event{Type: EventProvided}, want: "expected used event"},
		// Failure prevented: arbitrary session text becomes an occurrence scope.
		{name: "invalid session ID", event: Event{Type: EventUsed}, sessionID: "private-session", want: "invalid session ID"},
		// Failure prevented: the append error is masked after a successful empty read.
		{name: "missing parent", event: Event{Type: EventUsed}, missingParent: true, want: "open context-trace file"},
		// Failure prevented: an unreadable existing trace is mistaken for an empty trace.
		{name: "parent is a file", event: Event{Type: EventUsed}, blockedParent: true, want: "open context-trace file"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), FileName)
			if tt.missingParent {
				path = filepath.Join(t.TempDir(), "missing", FileName)
			}
			parent := filepath.Join(t.TempDir(), "blocked")
			if tt.blockedParent {
				require.NoError(t, os.WriteFile(parent, []byte("preserve"), 0o600))
				path = filepath.Join(parent, FileName)
			}
			event, err := NewFileWriter(path).AppendUse(context.Background(), tt.event, tt.sessionID)
			require.ErrorContains(t, err, tt.want)
			assert.Equal(t, Event{}, event)
			if tt.blockedParent {
				raw, err := os.ReadFile(parent)
				require.NoError(t, err)
				assert.Equal(t, "preserve", string(raw))
			} else {
				assert.NoFileExists(t, path)
			}
		})
	}
}

// Failure prevented: two writers reach one trace through different spellings
// of its directory (macOS /var vs /private/var), take two different locks and
// hand out the same occurrence, so PostHog keeps one use and drops the other.
func TestAppendUse_SymlinkedSpellingsShareOneLock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating a directory symlink needs privileges on Windows")
	}
	real := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(real, alias))
	const n = 40
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		dir := real
		if i%2 == 1 {
			dir = alias
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := NewWriter(dir).AppendUse(context.Background(), Event{Type: EventUsed, Evidence: EvidenceNone}, "ses_019ff2f5-2079-7be1-b05e-8caad2772e61")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	events, err := ReadEvents(NewWriter(real).Path())
	require.NoError(t, err)
	require.Len(t, events, n)
	seen := map[int]bool{}
	for _, ev := range events {
		assert.False(t, seen[ev.OccurrenceSeq], "occurrence %d handed out twice", ev.OccurrenceSeq)
		seen[ev.OccurrenceSeq] = true
	}
}
