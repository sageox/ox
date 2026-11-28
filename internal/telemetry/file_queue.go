package telemetry

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sageox/ox/internal/paths"
)

// FileQueue provides disk-based event queueing for telemetry.
//
// BEST EFFORT: Telemetry is intentionally "fire and forget". Events may be lost due to:
//   - CLI crash before flush
//   - Network unavailability during POST
//   - File truncation on overflow (>1MB)
//   - Disk write failures (silently ignored)
//
// This is acceptable - telemetry is for aggregate analytics, not critical data.
// We optimize for CLI responsiveness over delivery guarantees.
//
// Events are appended to a JSONL file and POSTed lazily on subsequent CLI invocations.
// This keeps each CLI command fast (no blocking network calls) while ensuring events
// are eventually transmitted when conditions allow.
//
// FUTURE: Consider moving telemetry transmission to a separate daemon process.
// A daemon would provide more reliable delivery without impacting CLI responsiveness,
// and could handle retries, backoff, and batching more intelligently. For now, the
// lazy flush approach is simpler and sufficient for our needs.
type FileQueue struct {
	path      string
	sageoxDir string
	mu        sync.Mutex
}

const (
	// flushThresholdCount triggers a flush when this many events are queued
	flushThresholdCount = 10

	// flushThresholdAge triggers a flush when oldest event is older than this
	flushThresholdAge = 1 * time.Hour

	// maxFileSize limits the telemetry file to prevent unbounded growth (1MB)
	maxFileSize = 1 * 1024 * 1024
)

// NewFileQueue creates a new file-based event queue.
// Queues live in the per-user cache, keyed by the canonical project root.
// Repository-local queues are deliberately neither read nor migrated.
// Returns nil if the project or cache location cannot be resolved safely.
func NewFileQueue(projectRoot string) *FileQueue {
	if projectRoot == "" {
		return nil
	}

	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil
	}
	cacheDir := paths.TelemetryCacheDir()
	if !filepath.IsAbs(cacheDir) {
		return nil // never fall back to a path relative to the checkout
	}
	key := sha256.Sum256([]byte(root))
	return &FileQueue{
		path:      filepath.Join(cacheDir, hex.EncodeToString(key[:])+".jsonl"),
		sageoxDir: filepath.Join(root, ".sageox"),
	}
}

// Append adds an event to the queue file.
// This is a fast, append-only operation that does not block on network I/O.
// Returns nil on success; errors are non-fatal (telemetry should never break the CLI).
func (q *FileQueue) Append(event Event) error {
	if q == nil {
		return nil
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	// Markers are project metadata, not evidence that a queue path is trusted.
	if !isSageoxInstalled(q.sageoxDir) {
		return nil // silently skip - SageOx not installed or was uninstalled
	}

	// set timestamp if not already set
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	// marshal event to JSON
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}

	// append to file
	f, err := openQueueFile(q.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY)
	if err != nil {
		return err
	}
	defer f.Close()

	// Inspect and truncate the same validated file that will receive the event.
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > maxFileSize {
		if err := f.Truncate(0); err != nil {
			return err
		}
	}

	_, err = f.Write(append(data, '\n'))
	return err
}

// ReadAll reads all queued events from the file.
// Returns an empty slice if the file doesn't exist or is empty.
func (q *FileQueue) ReadAll() ([]Event, error) {
	if q == nil {
		return nil, nil
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	f, err := openQueueFile(q.path, os.O_RDONLY)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var events []Event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			// skip malformed lines
			continue
		}
		events = append(events, event)
	}

	return events, scanner.Err()
}

// Truncate removes all events from the queue file.
// Called after successful POST to clear transmitted events.
func (q *FileQueue) Truncate() error {
	if q == nil {
		return nil
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	f, err := openQueueFile(q.path, os.O_WRONLY)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Truncate(0)
}

// ShouldFlush returns true if the queue should be flushed.
// Triggers when: event count > threshold OR oldest event > age threshold.
func (q *FileQueue) ShouldFlush() bool {
	if q == nil {
		return false
	}

	events, err := q.ReadAll()
	if err != nil || len(events) == 0 {
		return false
	}

	// check count threshold
	if len(events) >= flushThresholdCount {
		return true
	}

	// check age threshold - oldest event is first in the list
	if len(events) > 0 && !events[0].Timestamp.IsZero() {
		age := time.Since(events[0].Timestamp)
		if age >= flushThresholdAge {
			return true
		}
	}

	return false
}

// Count returns the number of queued events.
func (q *FileQueue) Count() int {
	if q == nil {
		return 0
	}

	events, _ := q.ReadAll()
	return len(events)
}

// Path returns the path to the queue file.
func (q *FileQueue) Path() string {
	if q == nil {
		return ""
	}
	return q.path
}

// isSageoxInstalled checks if SageOx is properly installed in the given .sageox directory.
// Returns true if README.md or any .repo_* marker file exists.
func isSageoxInstalled(sageoxDir string) bool {
	// check for README.md
	if _, err := os.Stat(filepath.Join(sageoxDir, "README.md")); err == nil {
		return true
	}

	// check for .repo_* marker files
	entries, err := os.ReadDir(sageoxDir)
	if err != nil {
		return false
	}

	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), ".repo_") {
			return true
		}
	}

	return false
}
