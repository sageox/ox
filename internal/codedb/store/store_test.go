package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenCreatesStructure(t *testing.T) {
	tmp := t.TempDir()
	s, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if _, err := os.Stat(filepath.Join(tmp, "metadata.db")); err != nil {
		t.Error("metadata.db not created")
	}
	if _, err := os.Stat(filepath.Join(tmp, "repos")); err != nil {
		t.Error("repos dir not created")
	}

	// Can insert into repos table
	_, err = s.Exec("INSERT INTO repos (name, path) VALUES ('test', '/tmp/test')")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	var count int
	err = s.QueryRow("SELECT COUNT(*) FROM repos").Scan(&count)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 repo, got %d", count)
	}
}

func TestOpenIdempotent(t *testing.T) {
	tmp := t.TempDir()
	s1, err := Open(tmp)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	s1.Close()

	s2, err := Open(tmp)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	s2.Close()
}
