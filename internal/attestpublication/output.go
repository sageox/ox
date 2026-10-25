package attestpublication

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// publicationState owns the directory and archive handles for one locked attempt.
// Persistent paths are informational; active I/O never re-resolves their parents.
type publicationState struct {
	root    *os.Root
	archive *os.File
}

func openPublicationRoot(path string) (*os.Root, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// macOS exposes its system temporary directory through /var -> /private/var.
	// Resolve that trusted OS prefix once; user-supplied output components below
	// it still go through the same no-symlink, identity-checked walk.
	temporary := filepath.Clean(os.TempDir())
	if absolute == temporary || strings.HasPrefix(absolute, temporary+string(filepath.Separator)) {
		canonical, resolveErr := filepath.EvalSymlinks(temporary)
		if resolveErr != nil {
			return nil, resolveErr
		}
		absolute = canonical + strings.TrimPrefix(absolute, temporary)
	}
	volume := filepath.VolumeName(absolute) + string(filepath.Separator)
	root, err := os.OpenRoot(volume)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(absolute, volume), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		next, err := openPublicationChild(root, part)
		_ = root.Close()
		if err != nil {
			return nil, err
		}
		root = next
	}
	return root, nil
}

func openPublicationChild(parent *os.Root, name string) (*os.Root, error) {
	info, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		if err = parent.Mkdir(name, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		info, err = parent.Lstat(name)
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("publication output component %q is not a real directory", name)
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	actual, err := child.Stat(".")
	// An attacker can swap a component between Lstat and OpenRoot. Comparing
	// inode identities rejects that race while retaining the pinned descriptor.
	if err != nil || !os.SameFile(info, actual) {
		_ = child.Close()
		return nil, errors.New("publication output directory changed while opening")
	}
	return child, nil
}

func createPublicationTemp(root *os.Root, prefix string) (*os.File, string, error) {
	for range 10 {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return nil, "", err
		}
		name := prefix + hex.EncodeToString(nonce[:])
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return file, name, err
	}
	return nil, "", errors.New("publication temporary name collision")
}

func (state *publicationState) save(path string, journal Journal) error {
	file, name, err := createPublicationTemp(state.root, ".attest-journal-")
	if err != nil {
		return err
	}
	defer func() { _ = state.root.Remove(name) }()
	defer file.Close()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(journal); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := state.root.Rename(name, filepath.Base(path)); err != nil {
		return err
	}
	syncArchiveParent(state.root)
	return nil
}
