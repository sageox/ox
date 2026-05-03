package daemon

import "os"

// CurrentProcessFDCount returns the number of open file descriptors for the
// current process, or -1 if it cannot be determined. Portable across macOS
// (/dev/fd) and Linux (/proc/self/fd); on Windows neither path exists and
// the function returns -1 (the doctor check skips silently in that case).
//
// Used by the daemon status to surface FD growth as a first-class metric.
// The whole reason the project_watcher FD leak got bad enough in production
// to hang lsof was that nothing surfaced the growth — make it visible so
// future regressions in our deps (or our code) get caught in hours, not
// months.
func CurrentProcessFDCount() int {
	paths := []string{"/dev/fd", "/proc/self/fd"}
	for _, p := range paths {
		if n, ok := countFDsAt(p); ok {
			return n
		}
	}
	return -1
}

// countFDsAt enumerates entries via Readdirnames so we don't fstat each FD
// (some macOS kernel-only FD types fail fstatat with "bad file descriptor").
// Returns the count and true on success, false if the dir can't be opened.
func countFDsAt(path string) (int, bool) {
	d, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return 0, false
	}
	// Subtract 1 for the FD held by 'd' itself.
	n := len(names) - 1
	if n < 0 {
		n = 0
	}
	return n, true
}
