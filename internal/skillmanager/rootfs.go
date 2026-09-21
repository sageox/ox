package skillmanager

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrNonRegularFile marks a path ox declined to read because it is a symlink or
// otherwise not a regular file. Callers that are INSPECTING A FILE OX MANAGES
// must treat it as fatal — following a symlink out of the repo is the
// path-escape this refusal exists to prevent. Callers that are merely SCANNING
// a shared directory to discover which skills are ox's should skip it: a file
// ox cannot read is a file ox does not manage, which is an answer, not a
// failure.
var ErrNonRegularFile = errors.New("refusing non-regular or symlink file")

// openRepoDir returns a descriptor pinned to one real directory beneath root.
// Each component is checked with Lstat, then identity-checked after OpenRoot so
// a concurrent directory-to-symlink swap cannot redirect later operations.
func openRepoDir(root *os.Root, relative string, create bool) (*os.Root, error) {
	clean := filepath.Clean(filepath.FromSlash(relative))
	if filepath.IsAbs(clean) || filepath.VolumeName(clean) != "" || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("path escapes repository")
	}
	current, err := root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		info, statErr := current.Lstat(part)
		if os.IsNotExist(statErr) && create {
			if mkErr := current.Mkdir(part, 0o755); mkErr != nil && !os.IsExist(mkErr) {
				_ = current.Close()
				return nil, mkErr
			}
			info, statErr = current.Lstat(part)
		}
		if statErr != nil {
			_ = current.Close()
			return nil, statErr
		}
		if !info.IsDir() {
			_ = current.Close()
			return nil, fmt.Errorf("refusing non-directory or symlink path component %s", part)
		}
		child, openErr := current.OpenRoot(part)
		if openErr != nil {
			_ = current.Close()
			return nil, openErr
		}
		actual, actualErr := child.Stat(".")
		if actualErr != nil || !os.SameFile(info, actual) {
			_ = child.Close()
			_ = current.Close()
			if actualErr != nil {
				return nil, actualErr
			}
			return nil, fmt.Errorf("repository directory changed while opening %s", part)
		}
		_ = current.Close()
		current = child
	}
	return current, nil
}

func openRepoParent(root *os.Root, relative string, create bool) (*os.Root, string, error) {
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || filepath.IsAbs(clean) || filepath.VolumeName(clean) != "" || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, "", fmt.Errorf("path escapes repository")
	}
	base := filepath.Base(clean)
	if base == "." || base == ".." || base == "" {
		return nil, "", fmt.Errorf("invalid repository file path %q", relative)
	}
	parent, err := openRepoDir(root, filepath.Dir(clean), create)
	return parent, base, err
}

func inspectRootFile(root *os.Root, relative string) ([]byte, fs.FileMode, error) {
	parent, base, err := openRepoParent(root, relative, false)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = parent.Close() }()
	info, err := parent.Lstat(base)
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("%w: %s", ErrNonRegularFile, relative)
	}
	file, err := parent.Open(base)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = file.Close() }()
	actual, err := file.Stat()
	if err != nil {
		return nil, 0, err
	}
	if !actual.Mode().IsRegular() || !os.SameFile(info, actual) {
		return nil, 0, fmt.Errorf("%w: %s changed while opening", ErrNonRegularFile, relative)
	}
	data, err := io.ReadAll(file)
	return data, actual.Mode(), err
}

func createRootTemp(root *os.Root) (*os.File, string, error) {
	for range 10 {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return nil, "", err
		}
		name := ".ox-skill-" + hex.EncodeToString(nonce[:])
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if os.IsExist(err) {
			continue
		}
		return file, name, err
	}
	return nil, "", fmt.Errorf("skill temporary file name collision")
}

func atomicWriteInRoot(root *os.Root, relative string, content []byte, mode fs.FileMode) error {
	parent, base, err := openRepoParent(root, relative, true)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	if info, statErr := parent.Lstat(base); statErr == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("refusing non-regular or symlink file %s", relative)
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return statErr
	}
	tmp, tmpName, err := createRootTemp(parent)
	if err != nil {
		return err
	}
	success := false
	defer func() {
		if !success {
			_ = parent.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode.Perm()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := parent.Rename(tmpName, base); err != nil {
		return err
	}
	if dir, err := parent.Open("."); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	success = true
	return nil
}

func removeRootFile(root *os.Root, relative string) error {
	parent, base, err := openRepoParent(root, relative, false)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	return parent.Remove(base)
}

func checkDir(root, dir string) error {
	if err := ensureWithin(root, dir); err != nil {
		return err
	}
	rel, _ := filepath.Rel(root, dir)
	cur := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("refusing non-directory or symlink path %s", cur)
		}
	}
	return nil
}

func ensureWithin(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("path escapes repository")
	}
	return nil
}

func removeEmptyParentsInRoot(root *os.Root, relative string) {
	dir := filepath.Clean(relative)
	if filepath.IsAbs(dir) || dir == ".." || strings.HasPrefix(dir, ".."+string(filepath.Separator)) {
		return
	}
	for dir != "." && dir != "" {
		info, err := root.Lstat(dir)
		if err != nil || !info.IsDir() {
			return
		}
		if err := root.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}
