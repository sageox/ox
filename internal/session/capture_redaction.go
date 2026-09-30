package session

import (
	"bufio"
	"bytes"
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
	// A legacy writer had no journal, so a crash leaves a torn last line; the
	// SessionEnd door then "repairs" it with a newline and stamps a footer after
	// it. Neither is corruption: the torn content is re-read from the source on
	// the next batch, so nothing it named is lost. Such a line is tolerated
	// only while nothing but framing follows it. Garbage with real content
	// after it is a different file from the one that was written, and that
	// stays fail-closed: this scan runs on every batch until one commits, so
	// erring here would wedge the recording, but tolerating it could let a
	// credential result be written unredacted.
	var torn error
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var entry SessionEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			if torn == nil {
				torn = fmt.Errorf("reconstruct command redaction: %w", err)
			}
			continue
		}
		if torn != nil {
			if isFramingLine(line, false) {
				continue
			}
			return torn
		}
		w.cmdRedactor.RedactEntry(&entry)
		if w.cmdRedactor.overflow {
			return fmt.Errorf("too many unmatched credential-output calls")
		}
	}
	return scanner.Err()
}
