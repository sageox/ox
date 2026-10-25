package fileutil

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// AtomicWriteBytes writes raw bytes to filePath atomically using temp+rename,
// fsync'd before rename and the parent directory fsync'd after rename so the
// new inode entry survives a hard crash. Intended for user-facing files where
// a crash-window partial write would destroy content — instruction files
// (AGENTS.md / CONVENTIONS.md), env files, etc.
//
// If filePath is a symlink, the rename follows the symlink to its target so
// the link itself is preserved and the underlying file is updated. The
// CLAUDE.md → AGENTS.md pattern that ox init creates depends on this — a
// naive rename would replace the symlink with a regular file and break the
// link. Ordinary (non-symlink) paths are written directly as before.
func AtomicWriteBytes(filePath string, data []byte, perm os.FileMode) error {
	// Follow symlinks so the link structure survives the write (e.g.
	// CLAUDE.md → AGENTS.md). Lstat tells us whether filePath is itself a
	// symlink; if it is, resolve to the real inode and write there.
	writePath := filePath
	if info, err := os.Lstat(filePath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		resolved, resolveErr := filepath.EvalSymlinks(filePath)
		if resolveErr != nil {
			return fmt.Errorf("resolve symlink %q: %w", filePath, resolveErr)
		}
		writePath = resolved
	}

	dir := filepath.Dir(writePath)

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	success := false
	defer func() {
		if !success {
			os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write bytes: %w", err)
	}

	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}

	if err := os.Chmod(tmpPath, perm); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}

	if err := replaceFile(tmpPath, writePath); err != nil {
		return fmt.Errorf("rename: %w", err)
	}

	// fsync the parent directory so the rename's dirent update is durable
	// even across a hard crash. Best-effort: directory fsync is not supported
	// on every platform/filesystem (Windows, some network mounts), so log-and-
	// continue rather than failing the write.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}

	success = true
	return nil
}

// AtomicWriteJSON writes data as JSON to filePath atomically using temp+rename.
// This prevents partial/corrupt files from concurrent reads during write.
// The file is fsync'd before rename to ensure durability.
func AtomicWriteJSON(filePath string, data any, perm os.FileMode) error {
	dir := filepath.Dir(filePath)

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	success := false
	defer func() {
		if !success {
			os.Remove(tmpPath)
		}
	}()

	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(data); err != nil {
		tmp.Close()
		return fmt.Errorf("encode JSON: %w", err)
	}

	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}

	if err := os.Chmod(tmpPath, perm); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}

	if err := replaceFile(tmpPath, filePath); err != nil {
		return fmt.Errorf("rename: %w", err)
	}

	success = true
	return nil
}
