package adapterruntime

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

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
	entries, newOffset, _, err := TailJSONLWithStats(path, offset, parse)
	return entries, newOffset, err
}

// TailStats reports what a read actually saw. It exists because the failure
// this package was built to prevent — a parser that matches nothing the agent
// writes — is invisible from the outside: zero entries, no error, offset
// advanced to EOF, identical to an idle session.
//
// lines=412 parsed=0 says it immediately, on real data, with no fixture
// required. All six of the adapters that shipped broken parsers would have
// announced themselves on their first real run.
type TailStats struct {
	LinesRead     int
	EntriesParsed int
	ParseErrors   int
}

// AllLinesFailedToParse reports a read that consumed records and understood
// none of them. Callers should treat it as an error condition, not as an idle
// session.
func (s TailStats) AllLinesFailedToParse() bool {
	return s.LinesRead > 0 && s.EntriesParsed == 0
}

// TailJSONLWithStats is TailJSONL plus the read counts.
func TailJSONLWithStats(path string, offset int64, parse LineParser) ([]adapterprotocol.RawEntry, int64, TailStats, error) {
	return tailJSONL(path, offset, 0, parse)
}

// TailJSONLBudget is TailJSONL that stops before the record that would take the
// read past maxBytes of transcript, and returns the offset to resume from. At
// least one record is always read, so an oversized turn cannot wedge the
// cursor. A reader whose entries are much larger than their records' text (an
// adapter carrying pasted images) uses it to keep one response under the
// protocol's output limit; the caller's next read picks up the rest.
func TailJSONLBudget(path string, offset, maxBytes int64, parse LineParser) ([]adapterprotocol.RawEntry, int64, error) {
	entries, newOffset, _, err := tailJSONL(path, offset, maxBytes, parse)
	return entries, newOffset, err
}

func tailJSONL(path string, offset, maxBytes int64, parse LineParser) ([]adapterprotocol.RawEntry, int64, TailStats, error) {
	var stats TailStats
	if parse == nil {
		return nil, offset, stats, errors.New("TailJSONL requires a line parser")
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, offset, stats, fmt.Errorf("failed to open session file: %w", err)
	}
	defer func() { _ = f.Close() }()

	// An offset is only meaningful if it names a record boundary. Checking the
	// byte before it is a newline covers every way one goes stale at once —
	// the file shrank, it was replaced by a longer one, it was rewritten to
	// the same size, or a corrupted state file supplied a mid-record value.
	// Each of those otherwise resumes inside an unrelated record: the fragment
	// fails to parse, is silently dropped, and the beginning of the new
	// transcript is lost for good.
	if offset < 0 {
		offset = 0
	}
	if offset > 0 && !endsRecordBoundary(f, offset) {
		offset = 0
	}

	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return nil, offset, stats, fmt.Errorf("failed to seek to offset %d: %w", offset, err)
		}
	}

	var (
		entries  []adapterprotocol.RawEntry
		consumed = offset
		reader   = bufio.NewReaderSize(f, 64*1024)
	)

	for {
		line, n, oversize, err := readLine(reader)

		// io.EOF means the record had no terminating newline: the agent is
		// still writing it. Leave those bytes unconsumed so the completed
		// record is read next time, and stop.
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return entries, consumed, stats, fmt.Errorf("error reading session file: %w", err)
		}
		if n == 0 {
			continue
		}

		if maxBytes > 0 && stats.LinesRead > 0 && consumed-offset+int64(n) > maxBytes {
			break // the next read starts at this record
		}
		consumed += int64(n)
		stats.LinesRead++

		if oversize {
			// truncating would hand the parser a fragment and call it a
			// record; skipping keeps the offset moving without inventing data
			stats.ParseErrors++
			continue
		}
		if parsed, parseErr := parse(trimNewline(line)); parseErr == nil {
			entries = append(entries, parsed...)
			stats.EntriesParsed += len(parsed)
		} else {
			stats.ParseErrors++
		}
	}

	return entries, consumed, stats, nil
}

// endsRecordBoundary reports whether offset sits immediately after a newline,
// which is the only position a resume can safely start from.
func endsRecordBoundary(f *os.File, offset int64) bool {
	var b [1]byte
	if _, err := f.ReadAt(b[:], offset-1); err != nil {
		return false // past EOF, or unreadable — either way, do not trust it
	}
	return b[0] == '\n'
}

// recordWaitPoll is how often WaitForCompleteRecord looks again. An agent
// flushes a record in milliseconds; 25 ms keeps the wait short without
// spinning on stat.
const recordWaitPoll = 25 * time.Millisecond

// WaitForCompleteRecord waits until the file at path ends on a record
// boundary (empty, or its last byte a newline), at most timeout, and reports
// false only when it still ends mid-record at the deadline.
//
// TailJSONL leaves an unterminated last record unread, which is right for a
// read that will run again and wrong for the last one: a final drain that
// lands while the agent is still writing its last record loses that record
// for good. A file that cannot be read has nothing to wait for; the read that
// follows reports why.
func WaitForCompleteRecord(ctx context.Context, path string, timeout time.Duration) bool {
	return waitForCompleteRecord(ctx, timeout, func() bool { return endsOnRecordBoundary(path) })
}

// waitForCompleteRecord is WaitForCompleteRecord with its probe passed in, so
// a test can act between one probe and the next.
func waitForCompleteRecord(ctx context.Context, timeout time.Duration, complete func() bool) bool {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(recordWaitPoll)
	defer ticker.Stop()
	for {
		if complete() {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}

// endsOnRecordBoundary reports whether path ends on a record boundary, or
// cannot be read (and so has no partial record to wait for).
func endsOnRecordBoundary(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return true
	}
	return endsRecordBoundary(f, info.Size())
}

// readLine returns one newline-terminated record.
//
// It returns the record bytes (for parsing), the number of bytes consumed from
// the file (for the offset), and whether the record exceeded maxLineBytes.
// Those are three different numbers once a record is oversize, and conflating
// them is how an offset drifts.
//
// An unterminated trailing record comes back with io.EOF and MUST NOT be
// consumed — the agent is still writing it.
//
// Reading through bufio.ReadSlice rather than ReadBytes is what makes the cap
// real: ReadBytes accumulates the whole record before anyone can measure it, so
// a corrupt file with no newline in it would be read entirely into memory
// before being rejected for being too large.
func readLine(r *bufio.Reader) (line []byte, consumed int, oversize bool, err error) {
	for {
		chunk, e := r.ReadSlice('\n')
		consumed += len(chunk)

		if !oversize && len(line)+len(chunk) > maxLineBytes {
			oversize = true
			line = nil // stop holding a record we will not parse
		}
		if !oversize {
			line = append(line, chunk...)
		}

		if errors.Is(e, bufio.ErrBufferFull) {
			continue // record spans more than one buffer; keep reading
		}
		return line, consumed, oversize, e
	}
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
