package receiver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

// SessionIndex is atomically replaced after every successful append. Bytes counts
// the trace/log payloads including the appended newline, excluding metadata.
type SessionIndex struct {
	SessionID string    `json:"session_id"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Requests  int64     `json:"requests"`
	Bytes     int64     `json:"bytes"`
}

type receipt struct {
	RequestID  string    `json:"request_id"`
	ReceivedAt time.Time `json:"received_at"`
	Signal     string    `json:"signal"`
	Bytes      int       `json:"bytes"`
}

// SpoolStats summarizes local capture without reading trace content.
type SpoolStats struct {
	Sessions int       `json:"sessions"`
	Bytes    int64     `json:"bytes"`
	LastSeen time.Time `json:"last_seen,omitempty"`
}

// storageMu serializes local writers as well as taking the filesystem lock:
// flock behavior for repeated locks within a process varies across platforms.
var storageMu sync.Mutex

func safeEntry(root *os.Root, name string, directory bool) error {
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return fmt.Errorf("unsafe spool entry %q", name)
	}
	return nil
}

// openSpool refuses a linked spool itself and confines every later operation to
// an os.Root. Parent directories may include normal platform aliases (/var).
func openSpool(path string, create bool) (*os.Root, error) {
	if path == "" {
		return nil, errors.New("empty spool directory")
	}
	if create {
		if err := os.MkdirAll(path, 0700); err != nil {
			return nil, err
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("spool must be a directory, not a symlink")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	if create {
		dir, err := root.Open(".")
		if err == nil {
			err = dir.Chmod(0700)
			_ = dir.Close()
		}
		if err != nil {
			_ = root.Close()
			return nil, err
		}
	}
	return root, nil
}

func lockSpool(root *os.Root) (func(), error) {
	storageMu.Lock()
	if err := safeEntry(root, ".lock", false); err != nil {
		storageMu.Unlock()
		return nil, err
	}
	lock := flock.New(filepath.Join(root.Name(), ".lock"), flock.SetPermissions(0600))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil || !locked {
		storageMu.Unlock()
		if err == nil {
			err = errors.New("spool lock timeout")
		}
		return nil, err
	}
	return func() { _ = lock.Close(); storageMu.Unlock() }, nil
}

func readIndex(root *os.Root, id string) (SessionIndex, error) {
	var index SessionIndex
	if err := safeEntry(root, "index.json", false); err != nil {
		return index, err
	}
	b, err := root.ReadFile("index.json")
	if err != nil {
		return index, err
	}
	if err := json.Unmarshal(b, &index); err != nil {
		return index, err
	}
	if index.SessionID != id || index.LastSeen.IsZero() || index.FirstSeen.IsZero() || index.Requests < 1 || index.Bytes < 1 {
		return index, errors.New("invalid session index")
	}
	return index, nil
}

func appendPrivate(root *os.Root, name string, b []byte) error {
	if err := safeEntry(root, name, false); err != nil {
		return err
	}
	f, err := root.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	// One write and the spool lock prevent interleaved JSON records. An I/O error
	// can still leave a partial write; do not discard existing captured content.
	_, err = f.Write(append(b, '\n'))
	return err
}

func store(spool, signal, requestID string, parts map[string][]byte, now time.Time) error {
	root, err := openSpool(spool, true)
	if err != nil {
		return err
	}
	defer root.Close()
	unlock, err := lockSpool(root)
	if err != nil {
		return err
	}
	defer unlock()
	for id, b := range parts {
		if id != unattributed && !ValidSessionID(id) {
			return errors.New("invalid session directory")
		}
		if err := safeEntry(root, id, true); err != nil {
			return err
		}
		if err := root.Mkdir(id, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		session, err := root.OpenRoot(id)
		if err != nil {
			return err
		}
		err = storeSession(session, id, signal, requestID, b, now)
		_ = session.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func storeSession(root *os.Root, id, signal, requestID string, b []byte, now time.Time) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	err = dir.Chmod(0700)
	_ = dir.Close()
	if err != nil {
		return err
	}
	index, err := readIndex(root, id)
	if errors.Is(err, fs.ErrNotExist) {
		index = SessionIndex{SessionID: id, FirstSeen: now}
	} else if err != nil {
		return fmt.Errorf("read trace index: %w", err)
	}
	// Validate all destinations before the first append, including existing index
	// and temporary names, so a malformed local entry cannot redirect writes.
	for _, name := range []string{signal + ".jsonl", "receipts.jsonl", "index.json", "index.json.tmp"} {
		if err := safeEntry(root, name, false); err != nil {
			return err
		}
	}
	if err := appendPrivate(root, signal+".jsonl", b); err != nil {
		return err
	}
	if err := appendPrivate(root, "receipts.jsonl", encode(receipt{RequestID: requestID, ReceivedAt: now, Signal: signal, Bytes: len(b)})); err != nil {
		return err
	}
	index.LastSeen = now
	index.Requests++
	index.Bytes += int64(len(b) + 1)
	// O_EXCL prevents following even an in-root symlink substituted for the temp.
	if err := root.Remove("index.json.tmp"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := root.OpenFile("index.json.tmp", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(encode(index))
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	return root.Rename("index.json.tmp", "index.json")
}

func spoolEntries(root *os.Root) ([]fs.DirEntry, error) {
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	return dir.ReadDir(-1)
}

// Stats returns zero values before the first capture, and an error for corrupt
// metadata instead of presenting unreadable data as an empty spool.
func Stats(spoolDir string) (SpoolStats, error) {
	var stats SpoolStats
	root, err := openSpool(spoolDir, false)
	if errors.Is(err, fs.ErrNotExist) {
		return stats, nil
	}
	if err != nil {
		return stats, err
	}
	defer root.Close()
	unlock, err := lockSpool(root)
	if err != nil {
		return stats, err
	}
	defer unlock()
	entries, err := spoolEntries(root)
	if err != nil {
		return stats, err
	}
	var errs []error
	for _, entry := range entries {
		id := entry.Name()
		if id != unattributed && !ValidSessionID(id) {
			continue
		}
		if err := safeEntry(root, id, true); err != nil {
			errs = append(errs, err)
			continue
		}
		session, err := root.OpenRoot(id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		index, err := readIndex(session, id)
		if err == nil {
			stats.Sessions++
			if index.LastSeen.After(stats.LastSeen) {
				stats.LastSeen = index.LastSeen
			}
		} else {
			errs = append(errs, fmt.Errorf("session %s: %w", id, err))
		}
		files, err := spoolEntries(session)
		if err != nil {
			errs = append(errs, err)
		}
		for _, file := range files {
			if file.Type().IsRegular() {
				info, err := file.Info()
				if err != nil {
					errs = append(errs, err)
					continue
				}
				stats.Bytes += info.Size()
			}
		}
		_ = session.Close()
	}
	return stats, errors.Join(errs...)
}

// Prune removes only attributable, understood session folders older than 14
// days. Missing/corrupt indexes, fresh files after failed index writes, and IDs
// referenced by unfinalized recordings are conservatively retained.
func Prune(spoolDir string, now time.Time, protected map[string]bool) error {
	root, err := openSpool(spoolDir, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	unlock, err := lockSpool(root)
	if err != nil {
		return err
	}
	defer unlock()
	entries, err := spoolEntries(root)
	if err != nil {
		return err
	}
	cutoff := now.Add(-14 * 24 * time.Hour)
	var errs []error
	for _, entry := range entries {
		id := entry.Name()
		if (id != unattributed && !ValidSessionID(id)) || protected[id] {
			continue
		}
		if err := safeEntry(root, id, true); err != nil {
			errs = append(errs, err)
			continue
		}
		session, err := root.OpenRoot(id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		index, indexErr := readIndex(session, id)
		files, filesErr := spoolEntries(session)
		_ = session.Close()
		if err := errors.Join(indexErr, filesErr); err != nil {
			errs = append(errs, fmt.Errorf("retain session %s: %w", id, err))
			continue
		}
		if !index.LastSeen.Before(cutoff) {
			continue
		}
		safe := true
		for _, file := range files {
			info, err := file.Info()
			if err != nil {
				errs = append(errs, err)
				safe = false
				break
			}
			if !info.Mode().IsRegular() || info.ModTime().After(cutoff) {
				safe = false
				break
			}
		}
		if safe {
			if err := root.RemoveAll(id); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
