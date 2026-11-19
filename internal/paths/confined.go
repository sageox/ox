package paths

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrSymlinkInCache means a local-cache path inside a checkout reaches outside
// it through a symlink (or a non-directory sits where a directory belongs).
var ErrSymlinkInCache = errors.New("cache path passes through a symlink")

// CheckNoSymlinksBelow reports ErrSymlinkInCache unless target, which must lie
// under root, is reached from root through real directories only and holds no
// symlink anywhere beneath it. Call it before opening a database or other
// state under a checkout's .sageox/cache: the Ledger and team contexts are
// repos anyone on the team can push to, and a tracked symlink checked out at
// .sageox/cache/whisper would aim the daemon's open anywhere on disk.
// ledger.DisableSymlinks neutralizes tracked links, but it runs git and may
// not have run yet, so readers must not rely on it alone.
//
// root itself may be a symlink (a teammate's relocated checkout). A component
// that does not exist yet passes: MkdirAll creates it as a real directory.
func CheckNoSymlinksBelow(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: %s is not under %s", ErrSymlinkInCache, target, root)
	}
	p := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "." {
			continue
		}
		p = filepath.Join(p, part)
		info, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.IsDir() { // a symlink is not a directory to Lstat
			return fmt.Errorf("%w: %s", ErrSymlinkInCache, p)
		}
	}
	return filepath.WalkDir(target, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrSymlinkInCache, path)
		}
		return nil
	})
}
