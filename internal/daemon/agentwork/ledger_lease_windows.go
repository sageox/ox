//go:build windows

package agentwork

// Windows fallback for acquireLedgerLease, matching the posture of
// daemon/global_lease_windows.go: daemon support on Windows is POSIX-first and
// single-daemon-per-host, so the duplicate-daemon race this lease prevents does
// not materialize there. Report acquired=true so anti-entropy still runs.

import (
	"fmt"
	"os"
)

func platformAcquireLedgerLease(path string) (*os.File, bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open ledger lease file %s: %w", path, err)
	}
	return f, true, nil
}

func platformReleaseLedgerLease(f *os.File) error {
	if f == nil {
		return nil
	}
	return f.Close()
}
