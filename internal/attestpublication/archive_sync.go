package attestpublication

import "os"

func syncArchiveParent(root *os.Root) {
	// Sync the renamed archive's directory entry through the same pinned root.
	// Like fileutil.AtomicWriteBytes, this is best-effort because Windows and
	// some network filesystems do not support syncing directory handles.
	if directory, err := root.Open("."); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
}
