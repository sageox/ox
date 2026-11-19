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
	"math"
	"os"
	"path/filepath"
	"runtime"
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

// spoolSwapWait bounds a wait for the spool's swap lock (withSwapLock). Its
// holders do local file work only: an unlocked append, or a drain replacing a
// withheld batch. A hook's whole local budget is a second (liveLocalBound in
// cmd/ox), so waiting longer would only stall it; a timeout is a declared loss.
const spoolSwapWait = time.Second

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
// spoolLockWait the append proceeds without it. That is safe because the spool
// is append-only, one write call per batch, and a drain only reads up to the end
// it measured; the lock only orders the cap and torn-tail bookkeeping. The one
// exception, a drain replacing a withheld batch, cuts and rewrites the spool's
// unsent tail, so an append without live.lock holds the swap lock instead
// (withSwapLock).
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
		if spoolSwapWaitHook != nil {
			spoolSwapWaitHook()
		}
		err = withSwapLock(ctx, dir, run)
	}
	if err != nil && !errors.Is(err, ErrNotStreaming) {
		return res, spoolFailed(ctx, dir, int64(len(batch)), err)
	}
	return res, err
}

// withSwapLock holds the spool's swap lock. An append made without live.lock
// holds it from opening the spool to closing it, and replaceSpoolRange holds it
// (with live.lock) while it cuts and rewrites the spool's unsent tail. Without
// it, an append landing between the cut and the rewrite would be overwritten:
// lost, with no gap line. Like live.lock it is keyed by the session folder's
// resolved path (canonicalDir), so a hook naming the folder through a symlink
// and the daemon naming it directly take the same lock.
func withSwapLock(ctx context.Context, dir string, fn func() error) error {
	return fileutil.WithFileLockTimeout(ctx, filepath.Join(canonicalDir(dir), spoolFileName), spoolSwapWait, fn)
}

// spoolSwapWaitHook, nil in production, runs when an append that found
// live.lock busy is about to wait for the swap lock.
var spoolSwapWaitHook func()

// spoolRewriteHook, nil in production, lets a test act while replaceSpoolRange
// holds the swap lock, after it cut the spool and before it writes the new
// tail. An error it returns stands for that write failing.
var spoolRewriteHook func() error

// replaceSpoolRange replaces spool bytes [off, off+n) with line. The range is
// the unacknowledged batch at acked, so only the spool's tail from off is
// rewritten: cut there, then line and the rest of the tail written back.
// History the server already holds is never copied, so a withhold costs at
// most the unsent bytes (DefaultSpoolCap), however long the session ran.
//
// The caller holds live.lock; this also takes the swap lock, so no append made
// without live.lock writes between the cut and the rewrite. A crash in that
// window would drop the unsent tail with no gap line, so its size is recorded
// in spool-lost before the cut and settled only once the new tail is synced. A
// crash then leaves a spool_write_failed gap for the next write to declare
// (overstated if the rewrite had finished), never a silent loss.
func replaceSpoolRange(ctx context.Context, dir string, off, n int64, line []byte) error {
	return withSwapLock(ctx, dir, func() error {
		f, err := os.OpenFile(filepath.Join(dir, spoolFileName), os.O_RDWR, 0)
		if err != nil {
			return fmt.Errorf("open spool: %w", err)
		}
		defer func() { _ = f.Close() }() // synced below before anything relies on it
		tail, size, err := replacedTail(f, off, n, line)
		if err != nil {
			return err
		}
		declared, settle := readDeclared(dir)
		if err := recordLost(dir, size-off); err != nil {
			return err // nothing changed yet; the next drain withholds again
		}
		if err := f.Truncate(off); err != nil {
			return fmt.Errorf("cut spool at %d: %w", off, err)
		}
		if spoolRewriteHook != nil {
			if err := spoolRewriteHook(); err != nil {
				return err
			}
		}
		if _, err := f.WriteAt(tail, off); err != nil {
			return fmt.Errorf("rewrite spool tail: %w", err)
		}
		if err := f.Sync(); err != nil {
			return fmt.Errorf("sync spool: %w", err)
		}
		if settle {
			// nothing else settles while both locks are held, so declared + the
			// tail is exactly what spool-lost now records past older losses
			if err := settleLost(dir, declared+size-off); err != nil {
				// the spool is whole; the next write declares a gap for nothing lost
				slog.WarnContext(ctx, "live_session_spool_lost_settle_failed", "session", filepath.Base(dir), "bytes", size-off, "error", err)
			}
		}
		return nil
	})
}

// replacedTail reads the spool from off with bytes [off, off+n) replaced by
// line, into one buffer of the new tail's size. It returns the spool's size too.
func replacedTail(f *os.File, off, n int64, line []byte) ([]byte, int64, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, 0, fmt.Errorf("stat spool: %w", err)
	}
	size := info.Size()
	if off < 0 || n <= 0 || off+n > size {
		return nil, 0, fmt.Errorf("replace spool bytes [%d, %d): the spool holds %d", off, off+n, size)
	}
	tail := make([]byte, int64(len(line))+size-off-n)
	copy(tail, line)
	if _, err := f.ReadAt(tail[len(line):], off+n); err != nil {
		return nil, 0, fmt.Errorf("read spool: %w", err)
	}
	return tail, size, nil
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

// declareLoss records n lost bytes in spool-lost and logs the loss at Error.
// ctx only carries the log: a loss is often recorded because the operation was
// canceled, and the batch is already committed, so nothing would replay it.
func declareLoss(ctx context.Context, dir string, n int64, cause error) error {
	log := slog.Default().With("session", filepath.Base(dir))
	if st, err := readState(dir); err == nil {
		log = log.With("session_id", st.SessionID, "conversation_id", st.ConversationID, "layer_id", st.LayerID)
	}
	if err := recordLost(dir, n); err != nil {
		log.ErrorContext(ctx, "live_session_spool_lost", "bytes", n, "recorded", false, "error", cause, "record_error", err)
		return err
	}
	log.ErrorContext(ctx, "live_session_spool_lost", "bytes", n, "recorded", true, "error", cause)
	return nil
}

// recordLost appends one line for n lost bytes to spool-lost, in a single
// O_APPEND write. It takes no lock: a loss is often recorded because a lock just
// failed (an unusable lock directory, a canceled wait), and a record that needed
// one would fail the same way, leaving the next write no gap to declare. The
// line starts with a newline so it never fuses with a count the previous
// version wrote without one.
func recordLost(dir string, n int64) error {
	path := filepath.Join(dir, spoolLostFileName)
	_, statErr := os.Stat(path)
	created := errors.Is(statErr, fs.ErrNotExist)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("record lost spool bytes: %w", err)
	}
	_, werr := f.Write([]byte("\n" + strconv.FormatInt(n, 10) + "\n"))
	serr := f.Sync() // as durable as the atomic write it replaced: a loss forgotten in a crash is hidden
	if err := errors.Join(werr, serr, f.Close()); err != nil {
		return fmt.Errorf("record lost spool bytes: %w", err)
	}
	// A file fsync does not persist a new directory entry on every filesystem;
	// without this a crash could drop the whole record and with it the gap.
	if created {
		if err := syncDirEntry(dir); err != nil {
			return fmt.Errorf("record lost spool bytes: persist new %s: %w", spoolLostFileName, err)
		}
	}
	return nil
}

// syncDirEntry fsyncs dir so a file just created in it survives a crash.
// Windows cannot fsync a directory, and NTFS journals the entry itself.
func syncDirEntry(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

// lostTotals is what spool-lost and spool-lost-declared say, read together.
type lostTotals struct {
	pending  bool  // a loss awaits its gap line
	bytes    int64 // what that gap line declares; 0 when the amount is unknown
	recorded int64 // spool-lost's sum as read
	// settle is true when spool-lost could be read, so recorded can be stored
	// as declared once the gap line is written.
	settle bool
}

// readLost totals the lost bytes no gap line has declared yet: the sum of
// spool-lost minus spool-lost-declared. A file that cannot be read or parsed,
// or a declared total above the recorded one (a previous version removed
// spool-lost once it declared it), still means a loss may be undeclared, so it
// reads as pending: overstating a loss is safe, hiding one is not. A line that
// is not a count stays in spool-lost, so every later write declares a gap
// until the session ends.
func readLost(dir string) lostTotals {
	raw, err := os.ReadFile(filepath.Join(dir, spoolLostFileName))
	if errors.Is(err, fs.ErrNotExist) {
		return lostTotals{}
	}
	if err != nil {
		return lostTotals{pending: true}
	}
	recorded, clean := sumLost(raw)
	declared, ok := readDeclared(dir)
	if !ok || declared > recorded {
		declared, clean = 0, false
	}
	return lostTotals{
		pending:  !clean || recorded > declared,
		bytes:    recorded - declared,
		recorded: recorded,
		settle:   true,
	}
}

// sumLost adds up spool-lost's lines. clean is false when a line is not a
// byte count; the sum still holds every line that is. The previous version's
// file, one count with no newline, reads as one line.
func sumLost(raw []byte) (sum int64, clean bool) {
	clean = true
	for line := range bytes.Lines(raw) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		n, err := strconv.ParseInt(string(line), 10, 64)
		if err != nil || n < 0 || n > math.MaxInt64-sum {
			clean = false
			continue
		}
		sum += n
	}
	return sum, clean
}

// readDeclared returns spool-lost-declared's total. ok is false when the file
// exists but cannot be read or parsed; a missing file means nothing was
// declared yet.
func readDeclared(dir string) (int64, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, spoolLostDeclaredFileName))
	if errors.Is(err, fs.ErrNotExist) {
		return 0, true
	}
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseInt(string(bytes.TrimSpace(raw)), 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// lostDeclareHook, nil in production, lets a test act after a write read the
// loss totals and before it stores them as declared: a loss recorded in that
// window must stay pending.
var lostDeclareHook func()

// settleLost stores the spool-lost sum a gap line just declared. Only the
// spool writer calls it. A loss appended after that sum was read stays above
// it, so the next write declares it. If two writers race (one may append
// without live.lock; see Spool) the smaller sum may land last, which declares
// a loss twice but never hides one.
func settleLost(dir string, recorded int64) error {
	if lostDeclareHook != nil {
		lostDeclareHook()
	}
	return fileutil.AtomicWriteBytes(filepath.Join(dir, spoolLostDeclaredFileName), []byte(strconv.FormatInt(recorded, 10)), 0o600)
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
	lost := readLost(dir)
	b := lineBuilder{acked: acked, cap: capBytes, size: size}
	b.dropping = healed == 0 && !lost.pending && endsWith(f, size, gapSpoolFull)
	if healed > 0 {
		b.gap(fmt.Sprintf(`{"type":"gap","reason":"torn_tail","bytes":%d}`, healed))
	}
	if lost.pending {
		b.gap(fmt.Sprintf(`{"type":"gap","reason":"spool_write_failed","bytes":%d}`, lost.bytes))
	}
	b.addBatch(batch)
	if len(b.out) == 0 {
		return SpoolResult{}, nil
	}
	if _, err := f.Write(b.out); err != nil {
		return SpoolResult{}, fmt.Errorf("append spool: %w", err)
	}
	if lost.pending && lost.settle {
		if err := settleLost(dir, lost.recorded); err != nil {
			// not a spool failure: the batch and its gap line are written, and
			// the loss is declared again by the next write (overstated, not hidden)
			slog.Warn("live_session_spool_lost_settle_failed", "session", filepath.Base(dir), "bytes", lost.bytes, "error", err)
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
