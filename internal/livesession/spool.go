package livesession

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
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
// It returns ErrNotStreaming when the session has no stream left to feed: never
// prepared, or closed, deleted or disabled.
//
// A closing session still takes batches. Stop requested the close, but a batch
// can commit after that: a capture already in flight when stop fired (it spools
// after releasing raw.jsonl's lock, which stop then takes), or a stop that failed
// after its close and kept the recording going. Refusing it lost the bytes with
// no gap line while the close went on to claim a complete stream. Close drains
// the whole spool before it closes, and re-measures it afterwards (see finish).
//
// Oversized lines, lines that are not one JSON object, and lines past the
// unacknowledged-bytes cap become declared gap lines, never silent loss.
//
// A batch the spool cannot take (EACCES, ENOSPC, a read-only volume, a lock or
// state failure) is a loss too: its size is recorded in spool-lost, logged at
// Error, and the next write or the close declares it as a spool_write_failed
// gap line. The error is still returned.
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
		switch st.Status {
		case StatusPending, StatusStreaming, StatusClosing:
		default:
			return ErrNotStreaming
		}
		res, err = appendToSpool(dir, st.Acked, batch, opts)
		return err
	}
	err := withLock(ctx, dir, spoolLockWait, run)
	var busy *fileutil.ErrLockTimeout
	if errors.As(err, &busy) {
		err = run()
	}
	if err != nil && !errors.Is(err, ErrNotStreaming) {
		return res, spoolFailed(ctx, dir, int64(len(batch)), err)
	}
	return res, err
}

// DeclareLost records n bytes committed to raw.jsonl that never reached Spool
// (the capture could not read its batch back) exactly as Spool records a batch
// it could not write: logged at Error, and declared as a spool_write_failed gap
// line by the next write or the close. It returns ErrNotStreaming, recording
// nothing, when the session has no stream to declare them in.
func DeclareLost(ctx context.Context, dir string, n int64, cause error) error {
	if n <= 0 {
		return nil
	}
	st, err := ReadState(dir)
	if errors.Is(err, ErrNotStreaming) {
		return err
	}
	// a state that cannot be read may still be streaming: record, as Spool does
	if err == nil {
		switch st.Status {
		case StatusPending, StatusStreaming, StatusClosing:
		default:
			return ErrNotStreaming
		}
	}
	return declareLoss(ctx, dir, n, cause)
}

// spoolFailed records a batch the spool could not take so the stream declares
// it later, and logs the loss at Error: once the hook returns, nothing else
// would ever send these bytes.
func spoolFailed(ctx context.Context, dir string, n int64, cause error) error {
	if err := declareLoss(ctx, dir, n, cause); err != nil {
		return fmt.Errorf("spool %d bytes: %w (the loss could not be recorded: %w)", n, cause, err)
	}
	return fmt.Errorf("spool %d bytes (recorded as a gap for the next write): %w", n, cause)
}

// declareLoss adds n to the spool-lost count and logs the loss at Error.
func declareLoss(ctx context.Context, dir string, n int64, cause error) error {
	log := slog.Default().With("session", filepath.Base(dir))
	if st, err := readState(dir); err == nil {
		log = log.With("session_id", st.SessionID, "conversation_id", st.ConversationID, "layer_id", st.LayerID)
	}
	if err := recordLost(ctx, dir, n); err != nil {
		log.ErrorContext(ctx, "live_session_spool_lost", "bytes", n, "recorded", false, "error", cause, "record_error", err)
		return err
	}
	log.ErrorContext(ctx, "live_session_spool_lost", "bytes", n, "recorded", true, "error", cause)
	return nil
}

// lostLockWait bounds the wait on the spool-lost counter's lock. Its critical
// sections are one small file read and write, so a long wait means a stuck
// holder; failing then is logged as an unrecorded loss rather than hanging a hook.
const lostLockWait = 5 * time.Second

// settleLostReadHook, nil in production, lets a test act between settleLost
// reading the count and replacing it: the window the lost-count lock closes.
var settleLostReadHook func()

// withLostLock serializes every read-modify-write of the spool-lost count.
// recordLost and settleLost each read the count and write a new one; without
// one lock a loss recorded between settleLost's read and its removal vanished
// with no gap line. It is its own lock, not live.lock, so recording a loss
// never waits behind a drain (Spool may run unlocked; see Spool).
func withLostLock(ctx context.Context, dir string, fn func() error) error {
	return fileutil.WithFileLockTimeout(ctx, filepath.Join(dir, spoolLostFileName), lostLockWait, fn)
}

// recordLost adds n to the spool-lost count.
func recordLost(ctx context.Context, dir string, n int64) error {
	err := withLostLock(ctx, dir, func() error {
		prev, _ := readLost(dir)
		return writeLost(dir, prev+n)
	})
	if err != nil {
		return fmt.Errorf("record lost spool bytes: %w", err)
	}
	return nil
}

func writeLost(dir string, n int64) error {
	return fileutil.AtomicWriteBytes(filepath.Join(dir, spoolLostFileName), []byte(strconv.FormatInt(n, 10)), 0o600)
}

// readLost returns the recorded lost-byte count and whether a loss awaits its
// gap line. A count that cannot be read still means something was lost.
func readLost(dir string) (int64, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, spoolLostFileName))
	if errors.Is(err, fs.ErrNotExist) {
		return 0, false
	}
	n, perr := strconv.ParseInt(string(bytes.TrimSpace(raw)), 10, 64)
	if err != nil || perr != nil || n < 0 {
		return 0, true
	}
	return n, true
}

// settleLost clears the count a gap line just declared. A count that grew
// meanwhile (an unlocked Spool failing alongside this one) keeps the
// difference for the next write. A count left behind is declared again, which
// overstates the loss but never hides one.
func settleLost(ctx context.Context, dir string, declared int64) error {
	return withLostLock(ctx, dir, func() error {
		now, pending := readLost(dir)
		if settleLostReadHook != nil {
			settleLostReadHook()
		}
		switch {
		case !pending:
			return nil
		case now > declared:
			return writeLost(dir, now-declared)
		}
		return removeIfExists(filepath.Join(dir, spoolLostFileName))
	})
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
	lost, hasLost := readLost(dir)
	b := lineBuilder{acked: acked, cap: capBytes, size: size}
	b.dropping = healed == 0 && !hasLost && endsWith(f, size, gapSpoolFull)
	if healed > 0 {
		b.gap(fmt.Sprintf(`{"type":"gap","reason":"torn_tail","bytes":%d}`, healed))
	}
	if hasLost {
		b.gap(fmt.Sprintf(`{"type":"gap","reason":"spool_write_failed","bytes":%d}`, lost))
	}
	b.addBatch(batch)
	if len(b.out) == 0 {
		return SpoolResult{}, nil
	}
	if _, err := f.Write(b.out); err != nil {
		return SpoolResult{}, fmt.Errorf("append spool: %w", err)
	}
	if hasLost {
		if err := settleLost(context.Background(), dir, lost); err != nil {
			// not a spool failure: the batch and its gap line are written
			slog.Warn("live_session_spool_lost_settle_failed", "session", filepath.Base(dir), "bytes", lost, "error", err)
		}
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
