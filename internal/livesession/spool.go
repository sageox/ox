package livesession

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/sessionstream"
)

// DefaultSpoolCap bounds unacknowledged spool bytes. Past it, lines are dropped
// behind one declared spool_full gap until a drain frees room.
const DefaultSpoolCap int64 = 64 << 20

// spoolLockWait is how long a hook waits for a drain holding live.lock before
// appending without it (see Spool). Kept short because Claude Code waits on
// every hook: an unlocked append is safe, a hook that stalls a tool call is not.
const spoolLockWait = 200 * time.Millisecond

// tailScanChunk is how far back one step looks for the last newline.
const tailScanChunk = 4096

var gapSpoolFull = []byte(`{"type":"gap","reason":"spool_full"}` + "\n")

// SpoolOptions describes one committed batch.
type SpoolOptions struct {
	// Paused is true when the recording was paused while this batch was captured.
	// Paused content is never spooled.
	Paused bool
	// CapBytes overrides DefaultSpoolCap (tests); zero means the default.
	CapBytes int64
}

// SpoolResult reports what a Spool call wrote.
type SpoolResult struct {
	// Appended is how many bytes were added to the spool (gap lines included).
	Appended int64
	// Gaps is how many gap lines were written in place of lost content.
	Gaps int
	// Skipped is true when the batch was paused and nothing was written.
	Skipped bool
}

// Spool appends one committed raw.jsonl batch (exactly the bytes the capture
// writer added, header line included for the first batch) to the session spool.
// It returns ErrNotStreaming when the session is not streaming.
//
// Oversized lines, lines that are not one JSON object, and lines past the
// unacknowledged-bytes cap become declared gap lines, never silent loss.
//
// A hook must not wait behind a long drain: if live.lock stays busy for
// spoolLockWait the append proceeds unlocked. That is safe because the spool is
// append-only, one write call per batch, and a drain only reads up to the end it
// measured; the lock only orders the cap and torn-tail bookkeeping.
func Spool(ctx context.Context, dir string, batch []byte, opts SpoolOptions) (SpoolResult, error) {
	var res SpoolResult
	if opts.Paused {
		return SpoolResult{Skipped: true}, nil
	}
	if len(batch) == 0 {
		return res, nil
	}
	run := func() error {
		st, err := ReadState(dir)
		if err != nil {
			return err
		}
		if st.Status != StatusStreaming && st.Status != StatusPending {
			return ErrNotStreaming
		}
		res, err = appendToSpool(dir, st.Acked, batch, opts)
		return err
	}
	err := withLock(ctx, dir, spoolLockWait, run)
	var busy *fileutil.ErrLockTimeout
	if errors.As(err, &busy) {
		return res, run()
	}
	return res, err
}

func appendToSpool(dir string, acked int64, batch []byte, opts SpoolOptions) (SpoolResult, error) {
	capBytes := opts.CapBytes
	if capBytes <= 0 {
		capBytes = DefaultSpoolCap
	}
	f, err := os.OpenFile(filepath.Join(dir, spoolFileName), os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return SpoolResult{}, fmt.Errorf("open spool: %w", err)
	}
	defer func() { _ = f.Close() }() // read-only or append-only: nothing to flush

	size, healed, err := healTornTail(f)
	if err != nil {
		return SpoolResult{}, err
	}
	b := lineBuilder{acked: acked, cap: capBytes, size: size}
	b.dropping = healed == 0 && endsWith(f, size, gapSpoolFull)
	if healed > 0 {
		b.gap(fmt.Sprintf(`{"type":"gap","reason":"torn_tail","bytes":%d}`, healed))
	}
	b.addBatch(batch)
	if len(b.out) == 0 {
		return SpoolResult{}, nil
	}
	if _, err := f.Write(b.out); err != nil {
		return SpoolResult{}, fmt.Errorf("append spool: %w", err)
	}
	return SpoolResult{Appended: int64(len(b.out)), Gaps: b.gaps}, nil
}

// lineBuilder turns a batch into the bytes to append, declaring every loss.
type lineBuilder struct {
	out      []byte
	gaps     int
	size     int64 // spool size before this call
	acked    int64
	cap      int64
	dropping bool // the spool already ends in a spool_full gap
}

func (b *lineBuilder) gap(line string) {
	b.out = append(b.out, line...)
	b.out = append(b.out, '\n')
	b.gaps++
}

func (b *lineBuilder) addBatch(batch []byte) {
	for len(batch) > 0 {
		var line []byte
		if i := bytes.IndexByte(batch, '\n'); i >= 0 {
			line, batch = batch[:i], batch[i+1:]
		} else {
			line, batch = batch, nil // a batch may end without its newline
		}
		b.addLine(line)
	}
}

func (b *lineBuilder) addLine(line []byte) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return
	}
	withNL := int64(len(line)) + 1
	switch {
	case withNL > sessionstream.MaxBatchBytes:
		b.gap(fmt.Sprintf(`{"type":"gap","reason":"oversized_line","bytes":%d}`, len(line)))
	case !validLine(trimmed):
		// the server refuses a non-JSON line for the whole batch, which would
		// wedge the stream behind it; declare it instead.
		b.gap(fmt.Sprintf(`{"type":"gap","reason":"invalid_line","bytes":%d}`, len(line)))
	case b.size+int64(len(b.out))+withNL-b.acked > b.cap:
		if !b.dropping {
			b.out = append(b.out, gapSpoolFull...)
			b.gaps++
			b.dropping = true
		}
	default:
		b.out = append(b.out, line...)
		b.out = append(b.out, '\n')
		b.dropping = false
	}
}

func validLine(trimmed []byte) bool {
	return trimmed[0] == '{' && utf8.Valid(trimmed) && json.Valid(trimmed)
}

// healTornTail drops a final line that was cut off mid-write (crash). Only
// unsent bytes can be torn, because a drain sends whole lines only. It returns
// the new size and how many bytes were dropped.
func healTornTail(f *os.File) (size, dropped int64, err error) {
	info, err := f.Stat()
	if err != nil {
		return 0, 0, fmt.Errorf("stat spool: %w", err)
	}
	end, err := lineEnd(f, info.Size())
	if err != nil {
		return 0, 0, err
	}
	if end == info.Size() {
		return end, 0, nil
	}
	if err := f.Truncate(end); err != nil {
		return 0, 0, fmt.Errorf("truncate torn spool tail: %w", err)
	}
	return end, info.Size() - end, nil
}

// lineEnd is the offset just past the last newline in the first size bytes, or 0.
func lineEnd(f io.ReaderAt, size int64) (int64, error) {
	buf := make([]byte, tailScanChunk)
	for hi := size; hi > 0; {
		lo := max(hi-tailScanChunk, 0)
		chunk := buf[:hi-lo]
		if _, err := f.ReadAt(chunk, lo); err != nil && !errors.Is(err, io.EOF) {
			return 0, fmt.Errorf("scan spool tail: %w", err)
		}
		if i := bytes.LastIndexByte(chunk, '\n'); i >= 0 {
			return lo + int64(i) + 1, nil
		}
		hi = lo
	}
	return 0, nil
}

func endsWith(f io.ReaderAt, size int64, suffix []byte) bool {
	n := int64(len(suffix))
	if size < n {
		return false
	}
	got := make([]byte, n)
	if _, err := f.ReadAt(got, size-n); err != nil {
		return false
	}
	return bytes.Equal(got, suffix)
}
