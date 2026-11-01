package contexttrace

import (
	"context"
	"os"
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
