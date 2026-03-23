package daemon

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/paths"
)

// StoredError represents a daemon error that needs user attention.
// These are persisted to disk so they survive daemon restarts.
type StoredError struct {
	ID        string    `json:"id"`
	Message   string    `json:"message"`
	Code      string    `json:"code"`
	Timestamp time.Time `json:"timestamp"`
	Viewed    bool      `json:"viewed"`
	Severity  string    `json:"severity"` // "warning", "error"
}

// ErrorStore manages daemon errors for user notification.
// Errors are persisted to disk and survive daemon restarts.
//
// Thread safety: RWMutex allows concurrent reads from IPC handlers.
type ErrorStore struct {
	mu     sync.RWMutex
	errors []StoredError
	path   string
}

// NewErrorStore creates a new error store at the given path.
// If path is empty, uses the default daemon state directory.
func NewErrorStore(path string) *ErrorStore {
	if path == "" {
		path = ErrorStorePath()
	}
	store := &ErrorStore{
		errors: make([]StoredError, 0),
		path:   path,
	}
	// try to load existing errors (ignore errors, start fresh if needed)
	_ = store.Load()
	return store
}

// ErrorStorePath returns the default path to the error store file.
func ErrorStorePath() string {
	return filepath.Join(paths.DaemonStateDir(), "errors.json")
}

// ErrorStorePathForWorkspace returns the error store path for a specific workspace.
func ErrorStorePathForWorkspace(workspaceID string) string {
	return filepath.Join(paths.DaemonStateDir(), "errors-"+workspaceID+".json")
}

// Add adds an error to the store.
// If an error with the same code already exists, it updates the existing entry.
// Automatically saves to disk.
func (e *ErrorStore) Add(err StoredError) {
	e.mu.Lock()
	defer e.mu.Unlock()

	// generate ID if not provided
	if err.ID == "" {
		err.ID = generateErrorID()
	}

	// set timestamp if not provided
	if err.Timestamp.IsZero() {
		err.Timestamp = time.Now()
	}

	// check for existing error with same code (dedup)
	for i, existing := range e.errors {
		if existing.Code == err.Code && err.Code != "" {
			// update existing entry, preserve ID and original timestamp
			err.ID = existing.ID
			if err.Timestamp.IsZero() {
				err.Timestamp = existing.Timestamp
			}
			e.errors[i] = err
			if saveErr := e.saveUnlocked(); saveErr != nil {
				slog.Debug("error store save failed", "err", saveErr)
			}
			return
		}
	}

	// add new error
	e.errors = append(e.errors, err)
	if saveErr := e.saveUnlocked(); saveErr != nil {
		slog.Debug("error store save failed", "err", saveErr)
	}
}

// GetUnviewed returns all errors that haven't been viewed.
// Returns a copy sorted by timestamp (most recent first).
func (e *ErrorStore) GetUnviewed() []StoredError {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var unviewed []StoredError
	for _, err := range e.errors {
		if !err.Viewed {
			unviewed = append(unviewed, err)
		}
	}

	// sort by timestamp descending (most recent first)
	slices.SortFunc(unviewed, func(a, b StoredError) int {
		return b.Timestamp.Compare(a.Timestamp)
	})

	return unviewed
}

// GetAll returns all errors (viewed and unviewed).
// Returns a copy sorted by timestamp (most recent first).
func (e *ErrorStore) GetAll() []StoredError {
	e.mu.RLock()
	defer e.mu.RUnlock()

	result := make([]StoredError, len(e.errors))
	copy(result, e.errors)

	// sort by timestamp descending (most recent first)
	slices.SortFunc(result, func(a, b StoredError) int {
		return b.Timestamp.Compare(a.Timestamp)
	})

	return result
}

// MarkViewed marks an error as viewed by its ID.
// Automatically saves to disk.
func (e *ErrorStore) MarkViewed(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	for i := range e.errors {
		if e.errors[i].ID == id {
			e.errors[i].Viewed = true
			if saveErr := e.saveUnlocked(); saveErr != nil {
				slog.Debug("error store save failed", "err", saveErr)
			}
			return
		}
	}
}

// MarkAllViewed marks all errors as viewed.
// Automatically saves to disk.
func (e *ErrorStore) MarkAllViewed() {
	e.mu.Lock()
	defer e.mu.Unlock()

	changed := false
	for i := range e.errors {
		if !e.errors[i].Viewed {
			e.errors[i].Viewed = true
			changed = true
		}
	}
	if changed {
		if saveErr := e.saveUnlocked(); saveErr != nil {
			slog.Debug("error store save failed", "err", saveErr)
		}
	}
}

// Cleanup removes errors older than maxAge.
// Automatically saves to disk if any errors were removed.
func (e *ErrorStore) Cleanup(maxAge time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	var remaining []StoredError
	for _, err := range e.errors {
		if err.Timestamp.After(cutoff) {
			remaining = append(remaining, err)
		}
	}

	if len(remaining) != len(e.errors) {
		e.errors = remaining
		if saveErr := e.saveUnlocked(); saveErr != nil {
			slog.Debug("error store save failed", "err", saveErr)
		}
	}
}

// Clear removes all errors.
// Automatically saves to disk.
func (e *ErrorStore) Clear() {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.errors = e.errors[:0]
	if saveErr := e.saveUnlocked(); saveErr != nil {
		slog.Debug("error store save failed", "err", saveErr)
	}
}

// Count returns the total number of errors.
func (e *ErrorStore) Count() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.errors)
}

// UnviewedCount returns the number of unviewed errors.
func (e *ErrorStore) UnviewedCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()

	count := 0
	for _, err := range e.errors {
		if !err.Viewed {
			count++
		}
	}
	return count
}

// Save persists the error store to disk.
func (e *ErrorStore) Save() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.saveUnlocked()
}

// saveUnlocked saves without acquiring the lock (caller must hold lock).
func (e *ErrorStore) saveUnlocked() error {
	// ensure directory exists
	dir := filepath.Dir(e.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(e.errors, "", "  ")
	if err != nil {
		return err
	}

	// write atomically via temp file
	tmpPath := e.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmpPath, e.path)
}

// Load reads the error store from disk.
func (e *ErrorStore) Load() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	data, err := os.ReadFile(e.path)
	if err != nil {
		if os.IsNotExist(err) {
			e.errors = make([]StoredError, 0)
			return nil
		}
		return err
	}

	var errors []StoredError
	if err := json.Unmarshal(data, &errors); err != nil {
		// corrupt file, start fresh
		e.errors = make([]StoredError, 0)
		return nil
	}

	e.errors = errors
	return nil
}

// generateErrorID creates a unique error ID using UUIDv7.
func generateErrorID() string {
	u, err := uuid.NewV7()
	if err != nil {
		// fallback to random UUID if v7 fails
		return uuid.New().String()[:8]
	}
	return u.String()[:8]
}

// Error codes for common daemon errors.
const (
	ErrorCodeSyncFailed     = "sync_failed"
	ErrorCodeAuthExpired    = "auth_expired"
	ErrorCodeGitConflict    = "git_conflict"
	ErrorCodeNetworkError   = "network_error"
	ErrorCodeDiskFull       = "disk_full"
	ErrorCodePermissionDeny = "permission_denied"
)

// NewStoredError creates a new StoredError with the given code and message.
func NewStoredError(code, message string, severity string) StoredError {
	return StoredError{
		ID:        generateErrorID(),
		Code:      code,
		Message:   message,
		Severity:  severity,
		Timestamp: time.Now(),
		Viewed:    false,
	}
}
