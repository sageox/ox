package index

// treediff_git.go: stream tree diffs from one long-lived `git diff-tree`
// process instead of decoding trees in-process.
//
// Why: profiling the hot daemon showed go-git's Tree.Decode, compress/flate and
// the GC churn they cause dominating CPU while it diffed every commit. git
// already holds the object store open and diffs trees in C, so one process fed
// over a pipe replaces per-commit tree decoding with a few hundred bytes of
// text per changed file.
//
// Wire protocol (verified against git 2.54 and 2.56):
//
//	request:   "<old-tree> <new-tree>\n"
//	response:  "<old-tree> <new-tree>\n"            header, always '\n'-terminated even with -z
//	           ":<omode> <nmode> <osha> <nsha> <status>\0<path>\0"   zero or more raw records
//
// git block-buffers stdout when it is a pipe and does NOT flush after a diff
// request, so a response would sit in git's buffer until it fills. The only
// flush is on the pass-through path: a stdin line that does not start with an
// object id is echoed verbatim and stdout is flushed. Every request is
// therefore followed by an end-marker line (gitTreeDifferEndMarker); the
// response is complete when the marker comes back, and the flush it triggers
// pushes the whole response out ahead of it.
//
// Two ways git answers without a real diff, both turned into explicit errors:
//   - a request naming an unknown object is silently ignored: no header, so
//     the end marker arrives where the header should be;
//   - a request that does not parse as object ids (for example SHA-1 ids sent
//     to a SHA-256 repo) is treated as pass-through, so the request line itself
//     is echoed and looks like a header followed by an empty diff. Two
//     different trees always yield at least one raw record under -r, so an
//     empty response for different trees is rejected.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
)

const (
	// gitEmptyTreeSHA1 is the well-known empty tree. git resolves it without the
	// object being present, so a root commit diffs against it. Only SHA-1
	// repositories are supported; a SHA-256 repo fails the first real diff (see
	// the protocol notes) and the caller falls back to go-git.
	gitEmptyTreeSHA1 = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

	// gitTreeDifferEndMarker is the pass-through line that terminates and
	// flushes every response. It must not begin with a hex digit run that parses
	// as an object id, and must not begin with ':'.
	gitTreeDifferEndMarker = "ox-diff-tree-end"

	// gitTreeDifferCloseTimeout bounds how long Close waits for git to exit
	// after its stdin closes before killing it.
	gitTreeDifferCloseTimeout = 2 * time.Second

	// gitTreeDifferStderrLimit caps captured stderr, enough for git's one-line
	// fatal messages without letting a chatty process grow memory.
	gitTreeDifferStderrLimit = 4096

	// gitTreeDifferReadBufferSize holds several typical responses per read so a
	// large diff does not turn into one syscall per record.
	gitTreeDifferReadBufferSize = 64 << 10
)

var (
	errGitTreeDifferProtocol = errors.New("git diff-tree protocol violation")
	errGitTreeDifferClosed   = errors.New("git tree differ closed")

	rawFieldSeparator = []byte{' '}
)

var (
	_ treeDiffer = (*gitTreeDiffer)(nil)
	_ treeDiffer = (*fallbackTreeDiffer)(nil)
)

// gitTreeDiffer implements treeDiffer by driving a single long-lived
// `git diff-tree --stdin` process.
//
// It is NOT safe for concurrent use: requests and responses share one pipe
// pair and are matched purely by order, so callers must serialize DiffTrees
// calls and must not call Close while a DiffTrees call is in flight.
//
// After any failure (git died, protocol violation, canceled call) the process
// is killed and every later DiffTrees call fails fast; wrap it in a
// fallbackTreeDiffer to keep indexing.
type gitTreeDiffer struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr *cappedBuffer

	closeTimeout time.Duration

	// killed is set by terminate, which context.AfterFunc may call from another
	// goroutine; everything else below is touched only by the single caller.
	killed atomic.Bool

	failure error // first failure; the differ is unusable once set
	closed  bool
	reaped  bool
	waitErr error
}

// newGitTreeDiffer starts `git --git-dir=<gitDir> diff-tree --stdin -r --raw -z
// --no-renames` and performs one round trip to prove the process and the wire
// format work (bad git dir, unsafe ownership, a git too old to echo the
// header), so those problems surface here instead of on the first commit. ctx
// bounds the life of the process: canceling it kills git.
func newGitTreeDiffer(ctx context.Context, gitDir string) (*gitTreeDiffer, error) {
	cmd := exec.CommandContext(ctx, "git", "--git-dir="+gitDir,
		"diff-tree", "--stdin", "-r", "--raw", "-z", "--no-renames")
	// lazy fetch would turn a missing promisor object into a network call from
	// a background daemon; fail instead and let the caller fall back.
	cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0")
	return startGitTreeDiffer(ctx, cmd, true)
}

// startGitTreeDiffer starts cmd as the diff-tree process. It is separate from
// newGitTreeDiffer so tests can substitute scripted processes.
func startGitTreeDiffer(ctx context.Context, cmd *exec.Cmd, handshake bool) (*gitTreeDiffer, error) {
	d := &gitTreeDiffer{
		cmd:          cmd,
		stderr:       &cappedBuffer{limit: gitTreeDifferStderrLimit},
		closeTimeout: gitTreeDifferCloseTimeout,
	}
	cmd.Stderr = d.stderr
	// bound Wait if a grandchild keeps a pipe open after git itself is gone
	cmd.WaitDelay = gitTreeDifferCloseTimeout

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("git diff-tree stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("git diff-tree stdout pipe: %w", err)
	}
	d.stdin = stdin
	d.stdout = bufio.NewReaderSize(stdout, gitTreeDifferReadBufferSize)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start git diff-tree: %w", err)
	}

	if handshake {
		empty := plumbing.NewHash(gitEmptyTreeSHA1)
		if _, err := d.diff(ctx, empty, empty); err != nil {
			// diff reaps the process on most failures, but not when ctx was
			// already done on entry; terminate and wait are idempotent
			d.terminate()
			_ = d.wait()
			return nil, fmt.Errorf("git diff-tree handshake: %w", err)
		}
	}
	return d, nil
}

// DiffTrees returns the file-level changes between two trees in the form
// documented on treeChange. parent is plumbing.ZeroHash for a root commit.
// Canceling ctx kills the git process and returns ctx.Err(); the differ is
// unusable afterwards.
func (d *gitTreeDiffer) DiffTrees(ctx context.Context, parent, child plumbing.Hash) ([]treeChange, error) {
	if parent.IsZero() {
		parent = plumbing.NewHash(gitEmptyTreeSHA1)
	}
	changes, err := d.diff(ctx, parent, child)
	if err != nil {
		return nil, fmt.Errorf("git diff-tree %s %s: %w", parent, child, err)
	}
	return changes, nil
}

func (d *gitTreeDiffer) diff(ctx context.Context, oldTree, newTree plumbing.Hash) ([]treeChange, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch {
	case d.closed:
		return nil, errGitTreeDifferClosed
	case d.failure != nil:
		return nil, fmt.Errorf("git tree differ unusable: %w", d.failure)
	case d.killed.Load():
		// a previous call's context fired after it had already succeeded
		return nil, errors.New("git tree differ process was terminated")
	}

	// the blocking read below cannot select on ctx, so cancellation kills the
	// process, which closes the pipes and unblocks the read
	stop := context.AfterFunc(ctx, d.terminate)
	changes, err := d.exchange(oldTree, newTree)
	stop()
	if err != nil {
		return nil, d.fail(ctx, err)
	}
	return changes, nil
}

// exchange writes one request plus the end marker and reads the response.
func (d *gitTreeDiffer) exchange(oldTree, newTree plumbing.Hash) ([]treeChange, error) {
	request := oldTree.String() + " " + newTree.String()

	// both lines in one write; they are tiny and git is idle (the previous
	// response was drained through its end marker), so this cannot deadlock
	// against a full stdout pipe
	if _, err := io.WriteString(d.stdin, request+"\n"+gitTreeDifferEndMarker+"\n"); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	if err := d.expectHeader(request); err != nil {
		return nil, err
	}

	var (
		changes []treeChange
		records int
	)
	for {
		first, err := d.stdout.ReadByte()
		if err != nil {
			return nil, fmt.Errorf("read response: %w", unexpectedEOF(err))
		}
		if first != ':' {
			// a record starts with ':' and the marker with a letter
			if err := d.stdout.UnreadByte(); err != nil {
				return nil, fmt.Errorf("read response: %w", err)
			}
			if err := d.expectEndMarker(); err != nil {
				return nil, err
			}
			if records == 0 && oldTree != newTree {
				return nil, fmt.Errorf("%w: empty response for different trees (request treated as pass-through?)", errGitTreeDifferProtocol)
			}
			return changes, nil
		}
		records++
		change, keep, err := d.readRecord()
		if err != nil {
			return nil, err
		}
		if keep {
			changes = append(changes, change)
		}
	}
}

// expectHeader consumes the response header, which echoes the request.
func (d *gitTreeDiffer) expectHeader(request string) error {
	line, err := d.stdout.ReadSlice('\n')
	if err != nil {
		return fmt.Errorf("read header: %w", unexpectedEOF(err))
	}
	got := line[:len(line)-1]
	if string(got) == request {
		return nil
	}
	if string(got) == gitTreeDifferEndMarker {
		return fmt.Errorf("%w: git ignored the request (unknown tree object?)", errGitTreeDifferProtocol)
	}
	return fmt.Errorf("%w: header %q, want %q", errGitTreeDifferProtocol, got, request)
}

// expectEndMarker consumes the marker line that terminates a response.
func (d *gitTreeDiffer) expectEndMarker() error {
	line, err := d.stdout.ReadSlice('\n')
	if err != nil {
		return fmt.Errorf("read end marker: %w", unexpectedEOF(err))
	}
	if got := line[:len(line)-1]; string(got) != gitTreeDifferEndMarker {
		return fmt.Errorf("%w: end marker %q, want %q", errGitTreeDifferProtocol, got, gitTreeDifferEndMarker)
	}
	// anything already buffered after the marker means requests and
	// responses are out of step
	if n := d.stdout.Buffered(); n != 0 {
		return fmt.Errorf("%w: %d unexpected bytes after end marker", errGitTreeDifferProtocol, n)
	}
	return nil
}

// readRecord parses `<omode> <nmode> <osha> <nsha> <status>\0<path>\0`; the
// leading ':' is already consumed. keep is false for entries the indexer never
// sees (directories, submodules).
func (d *gitTreeDiffer) readRecord() (change treeChange, keep bool, err error) {
	meta, err := d.stdout.ReadSlice(0)
	if err != nil {
		return treeChange{}, false, fmt.Errorf("read record: %w", unexpectedEOF(err))
	}
	meta = meta[:len(meta)-1]

	oldMode, meta, ok1 := bytes.Cut(meta, rawFieldSeparator)
	newMode, meta, ok2 := bytes.Cut(meta, rawFieldSeparator)
	oldSHA, meta, ok3 := bytes.Cut(meta, rawFieldSeparator)
	newSHA, status, ok4 := bytes.Cut(meta, rawFieldSeparator)
	if !ok1 || !ok2 || !ok3 || !ok4 || len(status) != 1 {
		return treeChange{}, false, fmt.Errorf("%w: malformed raw record", errGitTreeDifferProtocol)
	}
	// every field is a view into the read buffer, which the path read below
	// may refill; copy out what outlives it
	statusByte := status[0]

	oldModeValue, err := parseRawMode(oldMode)
	if err != nil {
		return treeChange{}, false, err
	}
	newModeValue, err := parseRawMode(newMode)
	if err != nil {
		return treeChange{}, false, err
	}
	oldHash, err := parseRawHash(oldSHA)
	if err != nil {
		return treeChange{}, false, err
	}
	newHash, err := parseRawHash(newSHA)
	if err != nil {
		return treeChange{}, false, err
	}

	path, err := d.stdout.ReadString(0)
	if err != nil {
		return treeChange{}, false, fmt.Errorf("read record path: %w", unexpectedEOF(err))
	}
	path = strings.TrimSuffix(path, "\x00")

	return mapRawChange(statusByte, oldModeValue, newModeValue, oldHash, newHash, path)
}

// mapRawChange converts one git raw diff record to a treeChange, applying the
// same filter as indexCommit's go-git loop: inserts and modifies only when the
// new entry is a file (regular, executable, symlink), deletes only when the
// old entry is a file, never gitlinks (submodules).
//
// Type changes (status T) follow the same rule as modifications, matching
// go-git, which sees a changed mode as a modify: file <-> symlink is a modify,
// file -> gitlink reports nothing, and gitlink -> file is a modify whose
// OldHash is the old gitlink's commit id.
func mapRawChange(status byte, oldMode, newMode filemode.FileMode, oldHash, newHash plumbing.Hash, path string) (change treeChange, keep bool, err error) {
	switch status {
	case 'A':
		if !newMode.IsFile() {
			return treeChange{}, false, nil
		}
		return treeChange{Kind: treeChangeInsert, Path: path, OldHash: plumbing.ZeroHash, NewHash: newHash}, true, nil
	case 'M', 'T':
		if !newMode.IsFile() {
			return treeChange{}, false, nil
		}
		return treeChange{Kind: treeChangeModify, Path: path, OldHash: oldHash, NewHash: newHash}, true, nil
	case 'D':
		if !oldMode.IsFile() {
			return treeChange{}, false, nil
		}
		return treeChange{Kind: treeChangeDelete, Path: path, OldHash: oldHash, NewHash: plumbing.ZeroHash}, true, nil
	default:
		// --no-renames rules out R and C; anything else means the wire format
		// is not what this parser was written against
		return treeChange{}, false, fmt.Errorf("%w: unsupported status %q for %q", errGitTreeDifferProtocol, status, path)
	}
}

// parseRawMode parses a six digit octal git mode such as 100644.
func parseRawMode(field []byte) (filemode.FileMode, error) {
	if len(field) == 0 || len(field) > 7 {
		return 0, fmt.Errorf("%w: mode %q", errGitTreeDifferProtocol, field)
	}
	var mode uint32
	for _, digit := range field {
		if digit < '0' || digit > '7' {
			return 0, fmt.Errorf("%w: mode %q", errGitTreeDifferProtocol, field)
		}
		mode = mode<<3 | uint32(digit-'0')
	}
	return filemode.FileMode(mode), nil
}

// parseRawHash decodes a 40 digit SHA-1. Decoding into a stack array avoids a
// string allocation per hash on the per-record hot path.
func parseRawHash(field []byte) (plumbing.Hash, error) {
	var raw [20]byte
	if len(field) != hex.EncodedLen(len(raw)) {
		return plumbing.ZeroHash, fmt.Errorf("%w: object id %q", errGitTreeDifferProtocol, field)
	}
	if _, err := hex.Decode(raw[:], field); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("%w: object id %q: %w", errGitTreeDifferProtocol, field, err)
	}
	hash, _ := plumbing.FromBytes(raw[:])
	return hash, nil
}

// fail kills and reaps the process after an error and returns the error to
// report. A canceled ctx wins so callers can tell cancellation from breakage.
func (d *gitTreeDiffer) fail(ctx context.Context, cause error) error {
	d.terminate()
	waitErr := d.wait()

	if ctxErr := ctx.Err(); ctxErr != nil {
		d.failure = ctxErr
		return ctxErr
	}

	err := cause
	// an exit code means git quit on its own before the kill; a signal (-1)
	// means we killed a live process, which is not worth reporting
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) && exitErr.ExitCode() >= 0 {
		err = fmt.Errorf("%w (git exited with status %d)", err, exitErr.ExitCode())
	}
	if stderr := strings.TrimSpace(d.stderr.String()); stderr != "" {
		err = fmt.Errorf("%w (git stderr: %s)", err, stderr)
	}
	d.failure = err
	return err
}

// terminate kills the process. It is safe to call from any goroutine and more
// than once.
func (d *gitTreeDiffer) terminate() {
	d.killed.Store(true)
	if d.cmd.Process != nil {
		_ = d.cmd.Process.Kill()
	}
}

// wait reaps the process once and caches the result. Single caller only.
func (d *gitTreeDiffer) wait() error {
	if !d.reaped {
		d.waitErr = d.cmd.Wait()
		d.reaped = true
	}
	return d.waitErr
}

// Close ends the git process: it closes stdin, which makes git exit cleanly,
// waits up to a bounded time, then kills it. Safe to call more than once; not
// safe to call concurrently with DiffTrees.
func (d *gitTreeDiffer) Close() error {
	if d.closed {
		return nil
	}
	d.closed = true
	if d.reaped {
		// a failure already killed and reaped it and was reported then
		return nil
	}

	_ = d.stdin.Close()

	done := make(chan error, 1)
	go func() { done <- d.wait() }()

	timer := time.NewTimer(d.closeTimeout)
	defer timer.Stop()

	select {
	case err := <-done:
		if err != nil && !d.killed.Load() {
			return fmt.Errorf("git diff-tree exited uncleanly: %w", err)
		}
		return nil
	case <-timer.C:
		d.terminate()
		<-done
		return fmt.Errorf("git diff-tree did not exit within %s of stdin close; killed", d.closeTimeout)
	}
}

// unexpectedEOF reports a stream that ended mid-response as such; a plain EOF
// would read like a clean end.
func unexpectedEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// cappedBuffer keeps the first limit bytes written to it. exec copies stderr
// on its own goroutine, so reads and writes need a lock.
type cappedBuffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if room := c.limit - len(c.buf); room > 0 {
		c.buf = append(c.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.buf)
}

// fallbackTreeDiffer runs primary until it fails, then switches to fallback for
// the rest of its life. The failed diff is retried on fallback so the caller
// never sees the primary's error. A canceled or expired ctx is not a primary
// failure: the error is returned as is and nothing switches.
//
// Not safe for concurrent use, like the differs it wraps.
type fallbackTreeDiffer struct {
	primary  treeDiffer
	fallback treeDiffer
	// onFallback is called once, with the primary's error, at the switch. May be nil.
	onFallback func(err error)

	switched      bool
	primaryClosed bool
}

func (f *fallbackTreeDiffer) DiffTrees(ctx context.Context, parent, child plumbing.Hash) ([]treeChange, error) {
	if !f.switched {
		changes, err := f.primary.DiffTrees(ctx, parent, child)
		if err == nil {
			return changes, nil
		}
		if ctx.Err() != nil {
			return nil, err
		}
		f.switched = true
		// release the broken primary now; its error is already in hand
		f.primaryClosed = true
		_ = f.primary.Close()
		if f.onFallback != nil {
			f.onFallback(err)
		}
	}
	changes, err := f.fallback.DiffTrees(ctx, parent, child)
	if err != nil {
		return nil, fmt.Errorf("fallback tree diff: %w", err)
	}
	return changes, nil
}

// Close closes both differs; the primary only once even after a switch.
func (f *fallbackTreeDiffer) Close() error {
	var errs []error
	if !f.primaryClosed {
		f.primaryClosed = true
		if err := f.primary.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close primary tree differ: %w", err))
		}
	}
	if err := f.fallback.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close fallback tree differ: %w", err))
	}
	return errors.Join(errs...)
}
