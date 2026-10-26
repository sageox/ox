// Package sqliteerr classifies SQLite errors for the stores that rebuild a
// database they can afford to lose. Damage licenses deleting its files;
// contention never does, because another connection, in this process or
// another, may be using them (#1339).
package sqliteerr

import (
	"errors"
	"slices"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// IsDamage reports whether err carries SQLITE_CORRUPT or SQLITE_NOTADB — the
// result codes SQLite returns when a statement runs into a damaged database
// image. Every other failure (I/O, ENFILE, out of memory, BUSY, a canceled
// context) names a condition of this process or this moment, not of the
// database, and must never be treated as damage (#875).
func IsDamage(err error) bool {
	return isCode(err, sqlite3.SQLITE_CORRUPT, sqlite3.SQLITE_NOTADB)
}

// IsBusy reports whether err is SQLITE_BUSY or SQLITE_LOCKED: another
// connection holds the lock right now. It says nothing about the database's
// health, so callers retry rather than discard. busy_timeout does not cover
// the exclusive lock taken while a new database is converted to WAL, so
// openers racing to create one can get BUSY at once (#758).
func IsBusy(err error) bool {
	return isCode(err, sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED)
}

// IsReadOnly reports whether err is SQLITE_READONLY or one of its extended
// codes: SQLite needed to write (the database, its WAL index, or a journal) and
// could not, because the connection, file, or directory is read-only, or because
// PRAGMA query_only is set.
func IsReadOnly(err error) bool {
	return isCode(err, sqlite3.SQLITE_READONLY)
}

// isCode reports whether err is a SQLite error whose primary result code is
// one of want. Extended codes carry the primary in the low byte
// (SQLITE_BUSY_SNAPSHOT is SQLITE_BUSY | 2<<8), so the comparison masks it off
// rather than matching the code exactly.
func isCode(err error, want ...int) bool {
	var sqErr *sqlite.Error
	if !errors.As(err, &sqErr) {
		return false
	}
	return slices.Contains(want, sqErr.Code()&0xff)
}
