package codedb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A failed promotion must never destroy the previous healthy cache. With the
// naive "remove finalDir, then rename staging on top" order, an injected rename
// failure loses both. The move-aside → rename → restore-on-failure sequence
// keeps the previous cache intact. White-box so it can inject renameDir.
func TestBuildCodeDBAtomic_PromotionFailurePreservesPreviousCache(t *testing.T) {
	finalDir := filepath.Join(t.TempDir(), "codedb")
	if err := os.MkdirAll(finalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(finalDir, "GOOD")
	if err := os.WriteFile(good, []byte("healthy"), 0o600); err != nil {
		t.Fatal(err)
	}

	orig := renameDir
	defer func() { renameDir = orig }()
	// Fail only the promoting rename (source is the staging dir); let the
	// move-aside and restore renames run normally.
	renameDir = func(oldpath, newpath string) error {
		if strings.Contains(filepath.Base(oldpath), ".building-") {
			return fmt.Errorf("injected promotion failure")
		}
		return orig(oldpath, newpath)
	}

	err := BuildCodeDBAtomic(context.Background(), finalDir, func(ctx context.Context, db *DB) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected BuildCodeDBAtomic to report the promotion failure")
	}

	// The previous healthy cache must have been restored, byte-for-byte.
	b, readErr := os.ReadFile(good)
	if readErr != nil {
		t.Fatalf("previous cache must be restored after a failed promotion, GOOD missing: %v", readErr)
	}
	if string(b) != "healthy" {
		t.Fatalf("previous cache corrupted after failed promotion, got %q", string(b))
	}
	// No staging or backup dirs left behind.
	if leftovers, _ := filepath.Glob(finalDir + ".*"); len(leftovers) != 0 {
		t.Fatalf("expected no staging/backup leftovers after failed promotion, found %v", leftovers)
	}
}

// When both the promotion and the restore fail because a concurrent builder
// already populated finalDir with a usable cache, our backup is redundant and
// must be discarded — otherwise a full index copy leaks into the shared ledger
// cache on every occurrence (and the concurrency test flakes on leftovers).
func TestBuildCodeDBAtomic_RestoreFailureWithConcurrentWinner_DiscardsBackup(t *testing.T) {
	finalDir := filepath.Join(t.TempDir(), "codedb")
	if err := os.MkdirAll(finalDir, 0o755); err != nil {
		t.Fatal(err)
	}

	orig := renameDir
	defer func() { renameDir = orig }()
	renameDir = func(oldpath, newpath string) error {
		b := filepath.Base(oldpath)
		if strings.Contains(b, ".building-") {
			// Promotion fails, but simulate a concurrent winner having placed a
			// usable cache at finalDir in the meantime.
			_ = os.MkdirAll(newpath, 0o755)
			_ = os.WriteFile(filepath.Join(newpath, "WINNER"), []byte("winner"), 0o600)
			return fmt.Errorf("injected promotion failure")
		}
		if strings.Contains(b, ".old-") {
			return fmt.Errorf("injected restore failure")
		}
		return orig(oldpath, newpath)
	}

	err := BuildCodeDBAtomic(context.Background(), finalDir, func(ctx context.Context, db *DB) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected an error when promotion fails")
	}
	// The concurrent winner's cache remains, and no backup leaks.
	if _, statErr := os.Stat(filepath.Join(finalDir, "WINNER")); statErr != nil {
		t.Fatalf("expected the concurrent winner's cache to remain at finalDir, err=%v", statErr)
	}
	if leftovers, _ := filepath.Glob(finalDir + ".old-*"); len(leftovers) != 0 {
		t.Fatalf("expected the redundant backup to be discarded, found %v", leftovers)
	}
}

// When both renames fail and finalDir is genuinely absent (no concurrent winner),
// the previous cache is the only surviving copy: it must be preserved in the
// backup dir and the error must name it, not silently abandon it.
func TestBuildCodeDBAtomic_RestoreFailureNoWinner_PreservesBackup(t *testing.T) {
	finalDir := filepath.Join(t.TempDir(), "codedb")
	if err := os.MkdirAll(finalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(finalDir, "GOOD"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	orig := renameDir
	defer func() { renameDir = orig }()
	renameDir = func(oldpath, newpath string) error {
		b := filepath.Base(oldpath)
		if strings.Contains(b, ".building-") {
			return fmt.Errorf("injected promotion failure") // no winner placed
		}
		if strings.Contains(b, ".old-") {
			return fmt.Errorf("injected restore failure")
		}
		return orig(oldpath, newpath)
	}

	err := BuildCodeDBAtomic(context.Background(), finalDir, func(ctx context.Context, db *DB) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected an error when both promotion and restore fail")
	}
	// finalDir is absent, but the previous cache survives in the backup.
	backups, _ := filepath.Glob(finalDir + ".old-*")
	if len(backups) != 1 {
		t.Fatalf("expected the previous cache preserved in exactly one backup dir, found %v", backups)
	}
	b, readErr := os.ReadFile(filepath.Join(backups[0], "GOOD"))
	if readErr != nil || string(b) != "original" {
		t.Fatalf("expected the previous cache intact in the backup, got %q err=%v", string(b), readErr)
	}
	// The error must name the backup location so it is recoverable, not abandoned.
	if !strings.Contains(err.Error(), backups[0]) {
		t.Fatalf("expected the error to name the preserved backup %q, got %q", backups[0], err.Error())
	}
}
