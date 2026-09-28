package session

import (
	"errors"
	"fmt"
)

// AppendCheckpointed syncs a batch before publishing its source cursor. On a
// reported failure it removes only this batch, so the old cursor can be retried
// without duplicating entries. All RawWriter appenders and carrier stamps share
// the append lock, so rollback cannot remove a concurrent footer or entry.
// checkpoint must return an error only if it did not commit, and must not write
// to this raw file (the append lock is held across the callback).
// This handles reported I/O failures, not a process crash between the two files.
func (w *RawWriter) AppendCheckpointed(entries []Entry, checkpoint func() error) error {
	return w.withAppendLock(func() error { return w.appendCheckpointed(entries, checkpoint) })
}

func (w *RawWriter) appendCheckpointed(entries []Entry, checkpoint func() error) error {
	info, err := w.file.Stat()
	if err != nil {
		return fmt.Errorf("inspect raw checkpoint: %w", err)
	}
	appendErr := func() error {
		for i := range entries {
			if err := w.writeEntry(&entries[i]); err != nil {
				return err
			}
		}
		if err := w.Sync(); err != nil {
			return err
		}
		return checkpoint()
	}()
	if appendErr == nil {
		return nil
	}
	if err := w.file.Truncate(info.Size()); err != nil {
		return errors.Join(appendErr, fmt.Errorf("rollback uncheckpointed raw batch: %w", err))
	}
	if err := w.Sync(); err != nil {
		return errors.Join(appendErr, fmt.Errorf("sync raw rollback: %w", err))
	}
	return appendErr
}
