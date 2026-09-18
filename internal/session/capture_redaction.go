package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// RestoreCaptureRedaction carries only CallID-to-slug state across processes.
// Legacy recordings are reconstructed once from redacted raw entries; the next
// batch commits this checkpoint with its native cursor, avoiding repeated scans.
func (w *RawWriter) RestoreCaptureRedaction(state *RecordingState, rawPath string) error {
	if state.CommandRedactionVersion != 0 && state.CommandRedactionVersion != 1 {
		return fmt.Errorf("unsupported command redaction checkpoint")
	}
	if state.CommandRedactionVersion == 1 {
		if len(state.PendingCommandRedactions) > maxPendingCommandRedactions {
			return fmt.Errorf("oversized command redaction checkpoint")
		}
		w.cmdRedactor.pending = make(map[string]string, len(state.PendingCommandRedactions))
		for id, slug := range state.PendingCommandRedactions {
			allowed := false
			for _, rule := range w.cmdRedactor.rules {
				if rule.Slug == slug {
					allowed = true
					break
				}
			}
			if id == "" || !allowed {
				return fmt.Errorf("invalid command redaction checkpoint")
			}
			w.cmdRedactor.pending[id] = slug
		}
		return nil
	}
	f, err := os.Open(rawPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	// Match the transcript record limit while keeping ordinary reads small.
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		var entry SessionEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return fmt.Errorf("reconstruct command redaction: %w", err)
		}
		w.cmdRedactor.RedactEntry(&entry)
		if w.cmdRedactor.overflow {
			return fmt.Errorf("too many unmatched credential-output calls")
		}
	}
	return scanner.Err()
}
