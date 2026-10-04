package index

import (
	"context"
	"errors"

	"github.com/sageox/ox/internal/codedb/store"
)

// IsCorruptionError reports whether err indicates on-disk codedb corruption that
// a discard-and-rebuild of the cache would recover from — the indexing-pass
// analog of the git checkout's discard-and-reclone recovery.
//
// SQLite damage counts as well (store.IsSQLiteDamage): the open path no longer
// scans the database for it, so a damaged page is first met as SQLITE_CORRUPT
// from whichever statement reads it, and that statement is as good a witness as
// a scan.
//
// It deliberately returns false for context cancellation and deadline errors: a
// timeout is not corruption. Discarding a large cache on every slow or canceled
// run would be wasteful and could mask the real problem — the exact
// "timed out vs. corrupt on disk" conflation flagged in review of the external
// shell workaround this replaces.
func IsCorruptionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return errors.Is(err, ErrBleveCorrupt) ||
		errors.Is(err, store.ErrCorrupt) ||
		errors.Is(err, store.ErrFullReindexRequired) ||
		store.IsSQLiteDamage(err)
}
