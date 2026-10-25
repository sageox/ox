package index

import (
	"errors"
	"fmt"
	"runtime/debug"
)

// ErrBleveCorrupt marks an error whose root cause is a structurally corrupt
// bleve index — e.g. a nil FST left by a partial/interrupted write. Callers use
// errors.Is(err, ErrBleveCorrupt) (via IsCorruptionError) to decide whether
// discarding and rebuilding the codedb cache is the right recovery, rather than
// matching on a fragile error string.
var ErrBleveCorrupt = errors.New("bleve index corrupt")

// safeBatch runs fn (a bleve.Index.Batch call) with panic recovery.
//
// Why: a corrupt Bleve segment — observed in production as a nil FST left by
// partial-write disk corruption — panics deep inside vellum during batch
// processing. Without recovery the panic unwinds through the caller's
// deferred db.Close(), which contends for the same bbolt lock the batch was
// holding, producing a self-deadlock that pins the daemon (one incident:
// 39 minutes wedged in sync.Mutex.Lock while indexing_now stayed true).
//
// Recovering at the batch boundary converts the panic into a normal error
// return so the caller's Close path runs and the failure surfaces.
func safeBatch(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			// Wrap the corruption sentinel so callers can errors.Is on it and
			// trigger a discard-and-rebuild of the cache (see IsCorruptionError).
			err = fmt.Errorf("bleve batch panicked (likely corrupt index): %v\n%s: %w", r, debug.Stack(), ErrBleveCorrupt)
		}
	}()
	return fn()
}
