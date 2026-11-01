package contexttrace

import (
	"context"
	"fmt"
	"os"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/sessionid"
)

// AppendUse allocates a use's occurrence and persists it in one locked append.
// sessionID is the recording identity, or empty for a per-agent cache trace.
// Cache traces retain a random scope until the trace is removed; it is never
// derived from a path or the short agent ID. Seq keeps its turn-anchor meaning.
func (w *Writer) AppendUse(ctx context.Context, event Event, sessionID string) (Event, error) {
	if event.Type != EventUsed {
		return Event{}, fmt.Errorf("append use: expected used event")
	}
	if sessionID != "" && !sessionid.IsValidSessionID(sessionID) {
		return Event{}, fmt.Errorf("append use: invalid session ID")
	}
	err := fileutil.WithFileLock(ctx, w.path, func() error {
		events, unterminated, err := readEvents(w.path)
		if err != nil {
			return err
		}
		// Recover the unrecorded trace's scope before allocating its next ordinal.
		if sessionID == "" {
			for _, old := range events {
				if old.Type == EventUsed && sessionid.IsValidSessionID(old.SessionID) {
					sessionID = old.SessionID
					break
				}
			}
		}
		if sessionID == "" {
			id, err := uuid.NewV7()
			if err != nil {
				return fmt.Errorf("generate use scope: %w", err)
			}
			sessionID = sessionid.Prefix() + id.String()
		}
		event.SessionID, event.OccurrenceSeq = sessionID, 1
		for _, old := range events {
			if old.SessionID == sessionID && old.OccurrenceSeq >= event.OccurrenceSeq {
				event.OccurrenceSeq = old.OccurrenceSeq + 1
			}
		}
		if event.Timestamp == "" {
			event.Timestamp = Now()
		}
		if unterminated {
			f, err := os.OpenFile(w.path, os.O_APPEND|os.O_WRONLY, 0o644)
			if err != nil {
				return fmt.Errorf("open context-trace tail: %w", err)
			}
			_, writeErr := f.Write([]byte{'\n'})
			closeErr := f.Close()
			if writeErr != nil {
				return fmt.Errorf("separate context-trace tail: %w", writeErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close context-trace tail: %w", closeErr)
			}
		}
		return w.Append(event)
	})
	if err != nil {
		return Event{}, err
	}
	return event, nil
}
