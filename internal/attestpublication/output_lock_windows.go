//go:build windows

package attestpublication

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func lockPublicationFile(file *os.File) (bool, error) {
	var overlapped windows.Overlapped
	// One byte is an ownership token, matching the existing skills apply lock.
	err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return err == nil, err
}
