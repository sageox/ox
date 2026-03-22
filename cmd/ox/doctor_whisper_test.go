package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestIsWhisperDBCorrupt_HealthyDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "whisper.db")

	// create a valid SQLite DB
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY)")
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	db.Close()

	if isWhisperDBCorrupt(dbPath) {
		t.Error("expected healthy DB to not be reported as corrupt")
	}
}

func TestIsWhisperDBCorrupt_CorruptDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "whisper.db")

	// write garbage to simulate corruption
	if err := os.WriteFile(dbPath, []byte("this is not a valid sqlite database"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if !isWhisperDBCorrupt(dbPath) {
		t.Error("expected corrupt DB to be detected")
	}
}

func TestIsWhisperDBCorrupt_MissingDB(t *testing.T) {
	// SQLite lazily creates missing files, so isWhisperDBCorrupt returns false
	// for non-existent paths. The caller (checkWhisperDBIntegrity) handles
	// missing files via os.Stat before calling this function.
	dbPath := filepath.Join(t.TempDir(), "nonexistent.db")

	// not corrupt — SQLite creates a fresh empty DB on open
	if isWhisperDBCorrupt(dbPath) {
		t.Error("expected missing DB to not be reported as corrupt (SQLite creates on open)")
	}

	// clean up the auto-created file
	os.Remove(dbPath)
}

func TestRemoveWhisperSQLiteFiles(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "whisper.db")

	// create all three files
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.WriteFile(dbPath+suffix, []byte("data"), 0o600); err != nil {
			t.Fatalf("write %s: %v", suffix, err)
		}
	}

	removeWhisperSQLiteFiles(dbPath)

	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(dbPath + suffix); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed", dbPath+suffix)
		}
	}
}

func TestRemoveWhisperSQLiteFiles_PartialFiles(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "whisper.db")

	// only create the main file (no WAL or SHM)
	if err := os.WriteFile(dbPath, []byte("data"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// should not panic or error on missing WAL/SHM
	removeWhisperSQLiteFiles(dbPath)

	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Error("expected main db file to be removed")
	}
}

func TestCheckSlugWhisperDB_Registered(t *testing.T) {
	check := GetDoctorCheck(CheckSlugWhisperDB)
	if check == nil {
		t.Fatal("whisper-db check not registered in DoctorCheckRegistry")
	}
	if check.FixLevel != FixLevelAuto {
		t.Errorf("expected FixLevelAuto, got %s", check.FixLevel)
	}
	if !check.IsAutoFixable() {
		t.Error("whisper-db check should be auto-fixable")
	}
}
