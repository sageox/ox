//go:build windows

package fileutil

import "os"

// tryFlock is a no-op on Windows. We currently don't ship to Windows,
// but keep the build green so contributors on Windows can run tests.
// Cross-process serialization on Windows would need LockFileEx; until
// we have a concrete need, in-process serialization (see flock.go's
// acquireInProcess) is the only guarantee Windows callers get.
func tryFlock(*os.File) error { return nil }

func unlockFlock(*os.File) error { return nil }
