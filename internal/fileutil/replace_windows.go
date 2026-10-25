package fileutil

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// Go readers do not open files with FILE_SHARE_DELETE on Windows. Give short
// reads and antivirus scans time to release the destination; never fall back to
// truncating it. A persistently blocked replacement still returns its error.
func replaceFile(oldPath, newPath string) error {
	return retryWindowsRename(func() error { return os.Rename(oldPath, newPath) }, time.Sleep)
}

func retryWindowsRename(rename func() error, wait func(time.Duration)) error {
	for attempt := 0; ; attempt++ {
		err := rename()
		blocked := errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
			errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_LOCK_VIOLATION)
		if !blocked || attempt == 19 {
			return err
		}
		wait(25 * time.Millisecond)
	}
}
