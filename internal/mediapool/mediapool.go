// Package mediapool stages pasted-image bytes on disk between the capture path
// that sees them (the adapter read) and the live session drain that uploads
// them. The bytes never enter raw.jsonl or the spool: those carry only a media
// line naming the image by sha256.
//
// The pool is content-addressed and shared by every session on the machine, so
// the same image pasted twice is one file. Image bytes are content: nothing
// here logs them or their names.
package mediapool

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/paths"
)

const dirName = "live-media"

// maxAge is how long a staged file outlives its last write. It must cover the
// longest offline stretch a stream is expected to survive: a drain that finds
// its image swept declares a media_unavailable gap, so a week is the point past
// which an unsent turn is already a lost cause for other reasons (spool cap).
const maxAge = 7 * 24 * time.Hour

// ErrMissing means no staged file holds the digest: never staged, or swept.
var ErrMissing = errors.New("staged media not found")

// ErrCorrupt means a staged file no longer hashes to its name.
var ErrCorrupt = errors.New("staged media does not match its digest")

func dir(dataDir string) string {
	if dataDir == "" {
		dataDir = paths.DataDir()
	}
	return filepath.Join(dataDir, dirName)
}

// ValidDigest reports whether s is a lowercase hex sha256.
func ValidDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Put stages data under its sha256 in dataDir (empty: the ox data dir). The
// caller's digest must match: a mismatch is refused, never repaired. Staging
// the same bytes again is a no-op that refreshes the file's age.
func Put(dataDir, digest string, data []byte) error {
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != digest {
		return fmt.Errorf("stage media: digest does not match bytes")
	}
	d := dir(dataDir)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return fmt.Errorf("stage media: %w", err)
	}
	final := filepath.Join(d, digest)
	if _, err := os.Stat(final); err == nil {
		now := time.Now()
		_ = os.Chtimes(final, now, now) // age only; a failure just risks an earlier sweep
		return nil
	}
	tmp, err := os.CreateTemp(d, ".stage-*")
	if err != nil {
		return fmt.Errorf("stage media: %w", err)
	}
	_, werr := tmp.Write(data)
	serr := tmp.Sync()
	cerr := tmp.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("stage media: %w", err)
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("stage media: %w", err)
	}
	sweep(d)
	return nil
}

// Read returns the staged bytes for digest, verified against it.
func Read(dataDir, digest string) ([]byte, error) {
	if !ValidDigest(digest) {
		return nil, fmt.Errorf("read staged media: invalid digest")
	}
	b, err := os.ReadFile(filepath.Join(dir(dataDir), digest))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrMissing
	}
	if err != nil {
		return nil, fmt.Errorf("read staged media: %w", err)
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != digest {
		return nil, ErrCorrupt
	}
	return b, nil
}

// sweep removes files untouched for maxAge, and stage leftovers from a crash.
func sweep(d string) {
	entries, err := os.ReadDir(d)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(d, e.Name())) // best effort: it is a cache of a cache
	}
}
