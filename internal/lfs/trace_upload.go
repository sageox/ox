package lfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/session/pipeline"
)

// PublishTraceFiles uploads from the private cache, then installs only pointers
// in the Ledger. Both uploads finish before either pointer is published. A
// failed upload leaves the cache intact and never writes trace content to git.
// Pointer-write failures restore the previous attachment before returning.
func PublishTraceFiles(cacheDir, sessionDir string, upload func([]byte) (UploadedRef, error)) (map[string]FileRef, error) {
	return publishTraceFiles(cacheDir, sessionDir, upload, WritePointerFile)
}

func publishTraceFiles(cacheDir, sessionDir string, upload func([]byte) (UploadedRef, error), writePointer func(string, UploadedRef) error) (map[string]FileRef, error) {
	if filepath.Clean(cacheDir) == filepath.Clean(sessionDir) {
		return nil, fmt.Errorf("trace cache must be separate from ledger session")
	}
	uploaded, err := UploadTraceFiles(cacheDir, upload)
	if err != nil {
		return nil, err
	}
	names := []string{pipeline.LedgerFileTraceSpans, pipeline.LedgerFileTraceEvents}
	// Save only existing pointers: rollback must never put content into Git.
	type previousPointer struct {
		data []byte
		mode os.FileMode
	}
	previous := make(map[string]previousPointer, len(names))
	for _, name := range names {
		path := filepath.Join(sessionDir, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect previous trace pointer: %w", err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("previous trace pointer is not regular: %s", name)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read previous trace pointer: %w", err)
		}
		if _, _, err := ParsePointer(string(data)); err != nil {
			return nil, fmt.Errorf("invalid previous trace pointer %s: %w", name, err)
		}
		previous[name] = previousPointer{data: data, mode: info.Mode().Perm()}
	}
	refs := make(map[string]FileRef, len(uploaded))
	for i, name := range names {
		ref := uploaded[name]
		if err := writePointer(filepath.Join(sessionDir, name), ref); err != nil {
			publicationErr := fmt.Errorf("publish trace pointer: %w", err)
			for _, written := range names[:i] {
				path := filepath.Join(sessionDir, written)
				var rollbackErr error
				if old, ok := previous[written]; ok {
					rollbackErr = fileutil.AtomicWriteBytes(path, old.data, old.mode)
				} else {
					rollbackErr = os.Remove(path)
				}
				if rollbackErr != nil {
					publicationErr = errors.Join(publicationErr, fmt.Errorf("restore trace pointer %s: %w", written, rollbackErr))
				}
			}
			return nil, publicationErr
		}
		refs[name] = ref.Ref()
	}
	return refs, nil
}

// UploadTraceFiles returns upload evidence while keeping the original cache
// intact. Daemon finalization uses it before staging, then writes pointers with
// the other confirmed uploads inside the commit transaction.
func UploadTraceFiles(cacheDir string, upload func([]byte) (UploadedRef, error)) (map[string]UploadedRef, error) {
	names := []string{pipeline.LedgerFileTraceSpans, pipeline.LedgerFileTraceEvents}
	uploaded := make(map[string]UploadedRef, len(names))
	for _, name := range names {
		path := filepath.Join(cacheDir, name)
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect trace cache: %w", err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("trace cache file is not regular: %s", name)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read trace cache: %w", err)
		}
		ref, err := upload(content)
		if err != nil {
			return nil, fmt.Errorf("upload %s: %w", name, err)
		}
		uploaded[name] = ref
	}
	return uploaded, nil
}
