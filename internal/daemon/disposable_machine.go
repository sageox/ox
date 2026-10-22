package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/paths"
)

// clearLedgerLocksOnFirstStart removes every git lock file in the Ledger the
// first time a daemon starts for this Ledger on this machine. Under
// disposable_machine the Ledger outlives the machine, and a machine that went
// away mid-command leaves locks no process here holds. RemoveStaleLockFiles
// keeps those: an index.lock for an hour, a lock naming a PID for as long as
// some process here has that PID, and a ref lock forever.
func clearLedgerLocksOnFirstStart(ctx context.Context, ledgerPath string, logger *slog.Logger) {
	if _, err := os.Stat(ledgerLocksClearedMarker(ledgerPath)); err == nil {
		return
	}
	err := gitutil.WithRepoLock(ctx, ledgerPath, func() error {
		removed, errs := gitutil.RemoveAllLockFiles(filepath.Join(ledgerPath, ".git"))
		if len(removed) > 0 {
			logger.Info("removed git locks a previous machine left in the Ledger", "path", ledgerPath, "locks", removed)
		}
		return errors.Join(errs...)
	})
	if err != nil {
		logger.Warn("could not clear the Ledger's git locks on this machine's first start", "path", ledgerPath, "error", err)
		return
	}
	markLedgerLocksCleared(ledgerPath, logger)
}

// markLedgerLocksCleared records that no other machine's locks remain in the
// Ledger at ledgerPath, so later daemon starts on this machine leave its locks
// alone.
func markLedgerLocksCleared(ledgerPath string, logger *slog.Logger) {
	marker := ledgerLocksClearedMarker(ledgerPath)
	err := os.MkdirAll(filepath.Dir(marker), 0o700)
	if err == nil {
		err = os.WriteFile(marker, nil, 0o600)
	}
	if err != nil {
		logger.Warn("could not record that the Ledger's git locks were cleared", "marker", marker, "error", err)
	}
}

// ledgerLocksClearedMarker is in the daemon state dir (by default under
// $XDG_RUNTIME_DIR, else the temp dir), which a new machine starts without.
func ledgerLocksClearedMarker(ledgerPath string) string {
	sum := sha256.Sum256([]byte(ledgerPath))
	return filepath.Join(paths.DaemonStateDir(), "ledger-locks-cleared-"+hex.EncodeToString(sum[:8]))
}
