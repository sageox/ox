package session

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/sageox/ox/internal/lfs"
)

// CountValidatedEntries counts normalized JSONL without retaining conversation
// content. Unlike UI best-effort counters, publication cannot skip malformed rows.
func CountValidatedEntries(ctx context.Context, path string) (int, error) {
	if lfs.IsPointerFile(path) {
		return 0, fmt.Errorf("%w: transcript is an LFS pointer", ErrSessionNotHydrated)
	}
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return 0, err
	}
	if !before.Mode().IsRegular() {
		return 0, fmt.Errorf("transcript is not a regular file")
	}
	scanner := bufio.NewScanner(io.NewSectionReader(file, 0, before.Size()))
	// Match the existing normalized transcript reader's 10MiB per-record limit;
	// total conversation size has no cap and the buffer grows only when required.
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	count, line := 0, 0
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		line++
		var record struct {
			Type       string          `json:"type"`
			Meta       json.RawMessage `json:"_meta"`
			Metadata   json.RawMessage `json:"metadata"`
			EntryCount *int            `json:"entry_count"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return 0, fmt.Errorf("invalid transcript record %d: %w", line, err)
		}
		if record.Type == "header" || record.Type == "footer" || len(record.Meta) > 0 || (record.Type == "" && (len(record.Metadata) > 0 || record.EntryCount != nil)) {
			continue
		}
		if record.Type == "" {
			return 0, fmt.Errorf("transcript record %d has no type", line)
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("scan transcript: %w", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return 0, fmt.Errorf("transcript changed during validation")
	}
	return count, nil
}
