//go:build unix

package skillmanager

import "io/fs"

// modeDrift reports whether the on-disk permission bits differ from what ox
// wants. On POSIX systems the bits are meaningful — a bundled script must stay
// executable — so a difference is real drift worth correcting.
func modeDrift(actual, want fs.FileMode) bool {
	return actual.Perm() != want.Perm()
}
