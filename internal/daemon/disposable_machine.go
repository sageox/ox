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
	marker := ledgerLocksClearedMarker(ledgerPath)
	if _, err := os.Stat(marker); err == nil {
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		logger.Warn("could not inspect the Ledger lock marker", "marker", marker, "error", err)
		return
	}
	err := gitutil.WithRepoLock(ctx, ledgerPath, func() error {
		// Checked again under the lock: another daemon for this Ledger may have
		// cleared them while this one waited, and its git may hold new ones.
		if _, err := os.Stat(marker); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		// Prepare the marker before deletion, but publish completion only
		// afterwards. A start interrupted here is retried under the repo lock.
		pending := marker + ".pending"
		if err := writeLedgerLockMarker(pending); err != nil {
			return err
		}
		removed, errs := gitutil.RemoveAllLockFiles(filepath.Join(ledgerPath, ".git"))
		if len(removed) > 0 {
			logger.Info("removed git locks a previous machine left in the Ledger", "path", ledgerPath, "locks", removed)
		}
		if err := errors.Join(errs...); err != nil {
			return err
		}
		return os.Rename(pending, marker)
	})
	if err != nil {
		logger.Warn("could not clear the Ledger's git locks on this machine's first start", "path", ledgerPath, "error", err)
	}
}

func writeLedgerLockMarker(marker string) error {
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		return err
	}
	return os.WriteFile(marker, nil, 0o600)
}

// ledgerLocksClearedMarker is in the daemon state dir (by default under
// $XDG_RUNTIME_DIR, else the temp dir), which a new machine starts without.
func ledgerLocksClearedMarker(ledgerPath string) string {
	sum := sha256.Sum256([]byte(ledgerPath))
	return filepath.Join(paths.DaemonStateDir(), "ledger-locks-cleared-"+hex.EncodeToString(sum[:8]))
}
