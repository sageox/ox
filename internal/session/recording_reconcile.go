package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ReconcileRawPrefix proves which entries after the durable checkpoint already
// reached raw.jsonl. The caller must hold the raw-file lock and must supply the
// source batch beginning at that checkpoint, before any new append.
//
// Comparison uses RawWriter's redacted representation. A complete matching
// prefix is retained and returned as the number of entries to skip on replay.
// A torn last line is removed only if its bytes are a prefix of the next entry
// that RawWriter would write. All other mismatches leave the file untouched.
// This is the legacy fallback for batches without an append journal. New
// capture batches must recover their journal first so a changed REDACT policy
// can safely rewrite unacknowledged bytes. Without a journal or matching policy,
// an old suffix cannot be proved and deliberately remains untouched.
func ReconcileRawPrefix(rawPath string, checkpointEntryCount int, entries []Entry, projectRoot string) (int, error) {
	data, err := os.ReadFile(rawPath)
	if errors.Is(err, os.ErrNotExist) {
		data = nil
	} else if err != nil {
		return 0, fmt.Errorf("read captured prefix: %w", err)
	}
	captured, completeBytes, err := completeRawRecords(data)
	if err != nil {
		return 0, err
	}
	if checkpointEntryCount < 0 || checkpointEntryCount > len(captured) {
		return 0, fmt.Errorf("recording checkpoint entry count=%d exceeds complete captured entries=%d", checkpointEntryCount, len(captured))
	}
	uncertain := captured[checkpointEntryCount:]
	partial := data[completeBytes:]
	needed := len(uncertain)
	if len(partial) > 0 {
		needed++
	}
	if needed > len(entries) {
		return 0, fmt.Errorf("uncheckpointed raw suffix needs %d source entries, got %d", needed, len(entries))
	}
	if needed == 0 {
		return 0, nil
	}

	expected, err := serializeReconcileEntries(rawPath, entries[:needed], projectRoot)
	if err != nil {
		return 0, fmt.Errorf("stage redacted replay: %w", err)
	}
	for i, actual := range uncertain {
		var source map[string]any
		if err := json.Unmarshal(expected[i], &source); err != nil {
			return 0, fmt.Errorf("parse staged replay entry: %w", err)
		}
		// JSON object order is immaterial; every field and value is retained.
		actualJSON, err := json.Marshal(actual)
		if err != nil {
			return 0, fmt.Errorf("encode captured entry: %w", err)
		}
		sourceJSON, err := json.Marshal(source)
		if err != nil {
			return 0, fmt.Errorf("encode replay entry: %w", err)
		}
		if !bytes.Equal(actualJSON, sourceJSON) {
			return 0, fmt.Errorf("uncheckpointed captured entry %d does not match redacted source", i)
		}
	}
	if len(partial) > 0 {
		if !bytes.HasPrefix(expected[len(uncertain)], partial) {
			return 0, fmt.Errorf("incomplete captured entry does not match redacted source")
		}
		// This deletes only a proven fragment of the entry the caller will
		// replay. A crash here is safe: the old source checkpoint still owns
		// the complete entry, and all complete captured bytes remain intact.
		if err := os.Truncate(rawPath, int64(completeBytes)); err != nil {
			return 0, fmt.Errorf("remove proven incomplete captured entry: %w", err)
		}
	}
	return len(uncertain), nil
}

// completeRawRecords treats even valid JSON without its terminating newline as
// incomplete. Otherwise a crash between '}' and '\n' would cause the next
// append to concatenate two JSON objects into one broken record.
func completeRawRecords(data []byte) ([]map[string]any, int, error) {
	var entries []map[string]any
	consumed := 0
	for consumed < len(data) {
		n := bytes.IndexByte(data[consumed:], '\n')
		if n < 0 {
			break
		}
		line := bytes.TrimSpace(data[consumed : consumed+n])
		if len(line) > 0 {
			var entry map[string]any
			if err := json.Unmarshal(line, &entry); err != nil || entry == nil {
				return nil, consumed, fmt.Errorf("invalid complete captured record at byte %d", consumed)
			}
			if _, header := entry["_meta"]; !header && entry["type"] != "header" && entry["type"] != "footer" {
				entries = append(entries, entry)
			}
		}
		consumed += n + 1
	}
	return entries, consumed, nil
}

// serializeReconcileEntries deliberately uses the same chokepoint as capture.
// No unredacted source bytes are written to this owner-only temporary file.
func serializeReconcileEntries(rawPath string, entries []Entry, projectRoot string) ([][]byte, error) {
	tmp, err := os.CreateTemp(filepath.Dir(rawPath), ".recording-reconcile-*")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	rw, err := NewRawWriterTruncate(tmpPath, projectRoot)
	if err != nil {
		return nil, err
	}
	defer rw.Close()
	for _, entry := range entries {
		// RawWriter mutates its argument; leave the caller's source intact.
		if err := rw.WriteEntry(&entry); err != nil {
			return nil, err
		}
	}
	if err := rw.Close(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(tmpPath)
	if err != nil {
		return nil, err
	}
	var lines [][]byte
	for len(data) > 0 {
		n := bytes.IndexByte(data, '\n')
		if n < 0 {
			return nil, fmt.Errorf("staged replay has an incomplete record")
		}
		lines = append(lines, data[:n+1])
		data = data[n+1:]
	}
	if len(lines) != len(entries) {
		return nil, fmt.Errorf("staged replay entry count mismatch")
	}
	return lines, nil
}
