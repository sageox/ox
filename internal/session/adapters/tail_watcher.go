package adapters

import (
	"bufio"
	"context"
	"os"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

// debounceDelay is the time to wait after the last write event before reading
// the file. This prevents reading partial writes when multiple events fire rapidly.
const debounceDelay = 100 * time.Millisecond

// ParseLineFunc converts a raw JSONL line into zero or more RawEntries.
// Each adapter provides its own implementation.
type ParseLineFunc func([]byte) ([]RawEntry, error)

// BatchTransformFunc post-processes a batch of entries before they are sent
// through the Watch channel. Used by adapters that need cross-entry correlation
// (e.g. Codex merges function_call + function_call_output by CallID).
type BatchTransformFunc func([]RawEntry) []RawEntry

// TailWatcher provides file-tailing with fsnotify, debounce, and offset tracking.
// Used by daemon for hookless agents and by adapter Watch() methods.
// The caller provides a parseLine function; TailWatcher handles file I/O plumbing.
type TailWatcher struct {
	path           string
	offset         atomic.Int64
	debounce       time.Duration
	parseLine      ParseLineFunc
	batchTransform BatchTransformFunc
}

// NewTailWatcher creates a TailWatcher that tails the given file starting at offset.
// parseLine converts each JSONL line into adapter-specific RawEntries.
func NewTailWatcher(path string, offset int64, parseLine ParseLineFunc) *TailWatcher {
	tw := &TailWatcher{
		path:      path,
		debounce:  debounceDelay,
		parseLine: parseLine,
	}
	tw.offset.Store(offset)
	return tw
}

// WithBatchTransform sets a post-processing function applied to each batch
// of entries before they are sent through the Watch channel or returned by
// ReadFromOffset. Returns the TailWatcher for chaining.
func (tw *TailWatcher) WithBatchTransform(fn BatchTransformFunc) *TailWatcher {
	tw.batchTransform = fn
	return tw
}

// Watch starts tailing the file and sends new entries to the returned channel.
// The channel is closed when ctx is canceled or the watcher encounters an error.
func (tw *TailWatcher) Watch(ctx context.Context) (<-chan RawEntry, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	if err := watcher.Add(tw.path); err != nil {
		watcher.Close()
		return nil, err
	}

	ch := make(chan RawEntry, 100)

	go func() {
		defer close(ch)
		defer watcher.Close()

		debounceTimer := time.NewTimer(0)
		if !debounceTimer.Stop() {
			<-debounceTimer.C
		}
		pendingRead := false

		for {
			select {
			case <-ctx.Done():
				debounceTimer.Stop()
				return

			case <-debounceTimer.C:
				if pendingRead {
					entries, newOffset, err := tw.readRaw(tw.offset.Load())
					if err == nil {
						tw.offset.Store(newOffset)
						if tw.batchTransform != nil {
							entries = tw.batchTransform(entries)
						}
						for _, entry := range entries {
							select {
							case ch <- entry:
							case <-ctx.Done():
								return
							}
						}
					}
					pendingRead = false
				}

			case event, ok := <-watcher.Events:
				if !ok {
					debounceTimer.Stop()
					return
				}
				if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
					pendingRead = true
					if !debounceTimer.Stop() {
						select {
						case <-debounceTimer.C:
						default:
						}
					}
					debounceTimer.Reset(tw.debounce)
				}

			case _, ok := <-watcher.Errors:
				if !ok {
					debounceTimer.Stop()
					return
				}
			}
		}
	}()

	return ch, nil
}

// ReadFromOffset reads new entries starting at the given byte offset,
// applying batchTransform if set. Returns entries and the new offset position.
func (tw *TailWatcher) ReadFromOffset(offset int64) ([]RawEntry, int64, error) {
	entries, newOffset, err := tw.readRaw(offset)
	if err != nil {
		return nil, offset, err
	}
	if tw.batchTransform != nil {
		entries = tw.batchTransform(entries)
	}
	return entries, newOffset, nil
}

// readRaw reads new entries starting at the given byte offset without applying transforms.
func (tw *TailWatcher) readRaw(offset int64) ([]RawEntry, int64, error) {
	f, err := os.Open(tw.path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()

	// clamp offset to file size to handle file truncation
	if fi, err := f.Stat(); err == nil && offset > fi.Size() {
		offset = fi.Size()
	}

	if _, err := f.Seek(offset, 0); err != nil {
		return nil, offset, err
	}

	var entries []RawEntry
	consumed := offset
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		// advance past this line (+1 for the newline delimiter)
		consumed += int64(len(line)) + 1
		if len(line) == 0 {
			continue
		}
		parsed, parseErr := tw.parseLine(line)
		if parseErr != nil {
			continue // skip unparseable lines but still advance offset
		}
		entries = append(entries, parsed...)
	}
	if err := scanner.Err(); err != nil {
		return entries, consumed, err
	}

	// bufio.Scanner returns partial trailing lines (no final newline), but our
	// consumed calculation adds +1 for a newline that may not exist. Clamp to
	// actual file position so partial lines are re-read when complete.
	pos, seekErr := f.Seek(0, 1)
	if seekErr == nil && consumed > pos {
		consumed = pos
	}

	return entries, consumed, nil
}

// Offset returns the current read position.
func (tw *TailWatcher) Offset() int64 {
	return tw.offset.Load()
}
