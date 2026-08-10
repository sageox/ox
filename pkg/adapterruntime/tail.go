package adapterruntime

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// maxLineBytes bounds a single JSONL record. Agent transcripts routinely carry
// a whole file's contents in one tool result, so the default scanner limit of
// 64 KiB truncates real turns.
const maxLineBytes = 10 * 1024 * 1024

// LineParser turns one raw JSONL record into zero or more entries. Returning
// an error skips the record: transcripts contain lines this adapter does not
// model (config changes, telemetry), and one unknown line must not abort the
// read.
type LineParser func(line []byte) ([]adapterprotocol.RawEntry, error)

// TailJSONL reads newline-delimited JSON records from path starting at offset
// and returns the entries plus the offset to resume from.
//
// Every JSONL-backed adapter needs this and each had written its own copy, so
// the edge cases below were fixed in some and not others.
//
// The returned offset advances only past COMPLETE records. An agent appends to
// its transcript while ox reads it, so the final line is frequently a partial
// write. Advancing to the file size — which is what the per-adapter copies did
// — acknowledges bytes that were never parsed, and the rest of that turn is
// skipped for good once the agent finishes writing it. Stopping at the last
// newline costs one re-read of a few hundred bytes and loses nothing.
func TailJSONL(path string, offset int64, parse LineParser) ([]adapterprotocol.RawEntry, int64, error) {
	if parse == nil {
		return nil, offset, errors.New("TailJSONL requires a line parser")
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, offset, fmt.Errorf("failed to open session file: %w", err)
	}
	defer func() { _ = f.Close() }()

	// A transcript that shrank was replaced (a new session reusing the name, a
	// rotation, a truncation). Resuming at a stale offset would read from the
	// middle of an unrelated record, so start over.
	if info, statErr := f.Stat(); statErr == nil && offset > info.Size() {
		offset = 0
	}

	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return nil, offset, fmt.Errorf("failed to seek to offset %d: %w", offset, err)
		}
	}

	var (
		entries  []adapterprotocol.RawEntry
		consumed = offset
		reader   = bufio.NewReaderSize(f, 64*1024)
	)

	for {
		line, err := readLine(reader)
		if len(line) > 0 && err == nil {
			// only a newline-terminated record is safely consumed
			consumed += int64(len(line))
			if parsed, parseErr := parse(trimNewline(line)); parseErr == nil {
				entries = append(entries, parsed...)
			}
			continue
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			// a trailing partial line stays unconsumed on purpose
			break
		}
		return entries, consumed, fmt.Errorf("error reading session file: %w", err)
	}

	return entries, consumed, nil
}

// readLine returns one line including its trailing newline, or the trailing
// partial line with io.EOF.
func readLine(r *bufio.Reader) ([]byte, error) {
	line, err := r.ReadBytes('\n')
	if len(line) > maxLineBytes {
		// keep the offset moving rather than wedging the reader on one
		// pathological record forever
		return line, nil
	}
	return line, err
}

func trimNewline(line []byte) []byte {
	line = trimSuffixByte(line, '\n')
	return trimSuffixByte(line, '\r')
}

func trimSuffixByte(b []byte, c byte) []byte {
	if len(b) > 0 && b[len(b)-1] == c {
		return b[:len(b)-1]
	}
	return b
}

// ReadFromOffsetJSONL adapts TailJSONL to the read-from-offset protocol
// handler, which is the shape every JSONL adapter registers.
func ReadFromOffsetJSONL(p adapterprotocol.ReadFromOffsetParams, parse LineParser) (*adapterprotocol.ReadFromOffsetResult, error) {
	entries, newOffset, err := TailJSONL(p.SessionFile, p.Offset, parse)
	if err != nil {
		return nil, err
	}
	return &adapterprotocol.ReadFromOffsetResult{
		Entries:   entries,
		NewOffset: newOffset,
	}, nil
}
