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
