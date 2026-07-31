package agentwork

// Cross-process leader election for a ledger's anti-entropy work (the
// session-finalize detection scan and the agent invocations it queues).
//
// Two daemons for the same repo is a supported state — see the "Liveness
// Detection: Socket Ping" note in daemon.go, which reasons that a duplicate is
// harmless because one exits on its 1h inactivity timeout. That reasoning holds
// only for an idle daemon. A daemon with a session backlog is never idle: it
// re-runs the detection scan every 5 minutes and stays alive indefinitely.
//
// Observed cost of that gap: two daemons on one 5,400-session ledger, each
// walking every session directory every 5 minutes, each enforcing its own
// 60-invocations/hour rate limit — so the effective ceiling was twice the
// configured one, against a machine budget of nothing.
//
// The lease is scoped to the ledger rather than the daemon so that a duplicate
// daemon keeps doing its cheap per-repo work (sync, heartbeats, IPC) and only
// the expensive shared scan is serialized. Acquisition is attempted once per
// detection cycle and held for the process lifetime; the kernel releases the
// flock on process death, so a crashed owner strands nothing.

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
)

// ledgerLeaseFileName lives under the ledger's cache dir, which .sageox/.gitignore
// already excludes from git.
const ledgerLeaseFileName = ".anti-entropy.lock"

// ledgerLease is an advisory lock claiming ownership of one ledger's
// anti-entropy work. The zero value is not usable; construct via
// acquireLedgerLease.
type ledgerLease struct {
	file *os.File
	held atomic.Int32
}

// ledgerLeasePath returns the lock file path for a ledger, creating its parent
// directory if needed.
func ledgerLeasePath(ledgerPath string) (string, error) {
	if ledgerPath == "" {
		return "", fmt.Errorf("empty ledger path")
	}
	dir := filepath.Join(ledgerPath, ".sageox", "cache")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create ledger lease dir: %w", err)
	}
	return filepath.Join(dir, ledgerLeaseFileName), nil
}

// acquireLedgerLease attempts a non-blocking exclusive lock on the ledger's
// anti-entropy lease. Returns (nil, nil) when another process holds it — that is
// an ordinary outcome, not an error. A non-nil error means the filesystem, not
// contention, got in the way.
func acquireLedgerLease(ledgerPath string) (*ledgerLease, error) {
	path, err := ledgerLeasePath(ledgerPath)
	if err != nil {
		return nil, err
	}
	f, acquired, err := platformAcquireLedgerLease(path)
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, nil
	}
	l := &ledgerLease{file: f}
	l.held.Store(1)
	return l, nil
}

// Release drops the lock. Idempotent.
//
// A failed release is still a release: both platform backends close the
// underlying file before returning an error, and closing the fd is what drops
// the kernel's flock. Restoring held=1 to "allow a retry" would be wrong twice
// over — the retry could only ever fail with EBADF on the closed fd, and
// meanwhile this process would claim a lease the kernel has already handed on.
// The error is reported; ownership is not reasserted.
func (l *ledgerLease) Release() error {
	if l == nil || !l.held.CompareAndSwap(1, 0) {
		return nil
	}
	if err := platformReleaseLedgerLease(l.file); err != nil {
		return fmt.Errorf("release ledger lease: %w", err)
	}
	return nil
}
