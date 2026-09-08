//go:build windows

package skillmanager

import "io/fs"

// modeDrift always reports false on Windows.
//
// NTFS has no POSIX permission bits: Go synthesizes a mode, and os.Chmod can only
// toggle the read-only attribute. A bundled script that ox wants at 0755 is read
// back as 0644 no matter what ox writes, so comparing permissions would report
// drift that can never be corrected — and the installer would rewrite every
// scripts/ file on every single reconcile, forever, on every Windows machine.
//
// Content is still compared by digest, which is the property that actually
// matters. The executable bit is simply not a thing this filesystem has.
func modeDrift(actual, want fs.FileMode) bool { return false }
