package pipeline

import (
	"io/fs"
	"os"

	"github.com/sageox/ox/internal/fileutil"
)

// FileSystem abstracts file operations for testability.
// Production code uses OSFileSystem; tests can inject a mock.
type FileSystem interface {
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte, perm os.FileMode) error
	MkdirAll(path string, perm os.FileMode) error
	Stat(path string) (fs.FileInfo, error)
	Remove(path string) error
	RemoveAll(path string) error
}

// FileCopier is an optional streaming capability. Existing in-memory/test
// filesystems retain ReadFile/WriteFile compatibility; production never buffers
// a complete transcript merely to copy it between cache and Ledger.
type FileCopier interface {
	CopyFile(destination, source string, perm os.FileMode) error
}

// OSFileSystem delegates to the real os package.
type OSFileSystem struct{}

func (OSFileSystem) CopyFile(destination, source string, perm os.FileMode) error {
	return fileutil.AtomicCopyFile(destination, source, perm)
}

func (OSFileSystem) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (OSFileSystem) WriteFile(path string, data []byte, perm os.FileMode) error {
	return fileutil.AtomicWriteBytes(path, data, perm)
}

func (OSFileSystem) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

func (OSFileSystem) Stat(path string) (fs.FileInfo, error) {
	return os.Stat(path)
}

func (OSFileSystem) Remove(path string) error {
	return os.Remove(path)
}

func (OSFileSystem) RemoveAll(path string) error {
	return os.RemoveAll(path)
}
