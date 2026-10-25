package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
)

// pragmaOverride changes a per-connection pragma for the length of one job and
// puts back what openSQLite configured.
type pragmaOverride struct {
	set     string
	restore string
}

var (
	// noForeignKeys lets a job empty or bulk-delete a table SQLite would
	// otherwise walk row by row. Only for jobs whose predicate already
	// guarantees nothing references what they delete.
	noForeignKeys = pragmaOverride{set: "PRAGMA foreign_keys=OFF", restore: "PRAGMA foreign_keys=ON"}

	// fileTempStore moves the temp store from RAM to disk. Pool connections run
	// with temp_store=MEMORY, which suits small sorts but not VACUUM (it builds
	// its whole rewritten copy there) or a stash of a full tree snapshot.
	fileTempStore = pragmaOverride{set: "PRAGMA temp_store=FILE", restore: "PRAGMA temp_store=MEMORY"}
)

// withConn runs fn on a connection reserved from the pool with overrides
// applied, then restores them. A connection whose settings cannot be restored is
// discarded rather than returned: handing back one with foreign keys off would
// quietly weaken every later writer.
func (s *Store) withConn(ctx context.Context, overrides []pragmaOverride, fn func(conn *sql.Conn) error) (err error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	// settings must be restored even when ctx is already canceled
	detached := context.WithoutCancel(ctx)
	applied := make([]pragmaOverride, 0, len(overrides))
	defer func() {
		for i := len(applied) - 1; i >= 0; i-- {
			if _, restoreErr := conn.ExecContext(detached, applied[i].restore); restoreErr != nil {
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
				err = errors.Join(err, fmt.Errorf("restore %q: %w", applied[i].restore, restoreErr))
			}
		}
	}()
	for _, o := range overrides {
		if _, setErr := conn.ExecContext(ctx, o.set); setErr != nil {
			return fmt.Errorf("%s: %w", o.set, setErr)
		}
		applied = append(applied, o)
	}
	return fn(conn)
}

// inImmediateTx runs fn inside BEGIN IMMEDIATE ... COMMIT on conn. Taking the
// write lock up front means nothing can change between the reads that choose
// what to delete and the deletes themselves, and a lock conflict surfaces at
// BEGIN (after busy_timeout) instead of as an unretryable snapshot error midway.
func inImmediateTx(ctx context.Context, conn *sql.Conn, fn func() error) error {
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()
	if err := fn(); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	committed = true
	return nil
}
