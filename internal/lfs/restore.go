package lfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/fileutil"
)

// RestoreSource names the evidence that justified turning hydrated session
// content back into an LFS pointer.
type RestoreSource string

const (
	// RestoreSourceCommitted: a pointer already committed (HEAD, or the remote's
	// tip) at the same path names the content's exact sha256 and size.
	RestoreSourceCommitted RestoreSource = "committed pointer"
	// RestoreSourceManifest: the session's meta.json Files manifest names the
	// content's exact sha256 and size.
	RestoreSourceManifest RestoreSource = "meta.json manifest"
)

// IsContentArtifact reports whether name (a path relative to its session
// directory) is a session artifact that belongs in the ledger as an LFS
// pointer rather than as raw content.
func IsContentArtifact(name string) bool {
	base := filepath.Base(name)
	for _, candidate := range ContentFiles {
		if base == candidate {
			return true
		}
	}
	return false
}

// StoredInGit reports whether the manifest declares name as Storage=git, the
// one case where raw content in the ledger is correct.
func (m *SessionMeta) StoredInGit(name string) bool {
	if m == nil {
		return false
	}
	ref, ok := m.Files[name]
	return ok && ref.IsGit()
}

// ResolveRestorableRef decides whether hydrated content may be replaced by a
// pointer, and if so which one. Content is restorable only when its sha256 and
// size equal a ref that is independently known: a pointer already committed at
// the same path, or the session manifest entry. A ref that disagrees with the
// bytes proves nothing about them, so it is never used (see guardPointerOverwrite).
//
// Returns ok=false when neither source vouches for the content; the caller
// must leave such a file alone and say so.
func ResolveRestorableRef(content []byte, name string, meta *SessionMeta, committed *FileRef) (FileRef, RestoreSource, bool) {
	digest := ComputeOID(content)
	size := int64(len(content))
	matches := func(ref FileRef) bool {
		return ref.IsLFS() && ref.OID != "" && ref.BareOID() == digest && ref.Size == size
	}
	restored := FileRef{Storage: StorageLFS, OID: "sha256:" + digest, Size: size}
	if committed != nil && matches(*committed) {
		return restored, RestoreSourceCommitted, true
	}
	if meta != nil {
		if ref, ok := meta.Files[name]; ok && matches(ref) {
			return restored, RestoreSourceManifest, true
		}
	}
	return FileRef{}, "", false
}

// RestorePointer replaces the hydrated file at path with the LFS pointer for
// ref. The content is first preserved at cachePath (the ledger's local
// hydration cache, where hydrated session content belongs) unless a cache copy
// already exists, so restoring a pointer never costs the coworker their local
// copy. ref MUST come from ResolveRestorableRef for these exact bytes.
//
// The pointer is minted with AssertUploaded: both restore sources name a blob
// that a prior upload registered (a committed pointer, or a manifest written
// only after upload). Should a blob nevertheless be missing from the store,
// the push-time reconcile uploads it from the cache copy preserved here.
func RestorePointer(path, cachePath string, content []byte, ref FileRef) error {
	if cachePath == "" {
		return fmt.Errorf("restore pointer %s: a cache path is required to keep the hydrated copy", filepath.Base(path))
	}
	if err := preserveInCache(cachePath, content); err != nil {
		return err
	}
	if err := WritePointerFile(path, AssertUploaded(ref)); err != nil {
		return fmt.Errorf("restore pointer %s: %w", filepath.Base(path), err)
	}
	return nil
}

// PreserveInCache keeps content at cachePath (the ledger's hydration cache) without
// overwriting a different copy already there, which fails closed.
func PreserveInCache(cachePath string, content []byte) error {
	return preserveInCache(cachePath, content)
}

func preserveInCache(cachePath string, content []byte) error {
	existing, err := os.ReadFile(cachePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read cache copy of %s: %w", filepath.Base(cachePath), err)
	}
	if err == nil && len(existing) > 0 {
		// never overwrite a cache copy, and never pointerize over one that is not these bytes:
		// the hydrated file would then have no copy anywhere but git history
		if ComputeOID(existing) != ComputeOID(content) {
			return fmt.Errorf("cache copy of %s differs from the hydrated file; refusing to replace it with a pointer", filepath.Base(cachePath))
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o700); err != nil {
		return fmt.Errorf("create cache dir for %s: %w", filepath.Base(cachePath), err)
	}
	if err := fileutil.AtomicWriteBytes(cachePath, content, 0o600); err != nil {
		return fmt.Errorf("preserve %s in cache: %w", filepath.Base(cachePath), err)
	}
	return nil
}
