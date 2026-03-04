package fileutil

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteJSON_Success(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")

	data := map[string]string{"key": "value"}
	if err := AtomicWriteJSON(path, data, 0600); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}

	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["key"] != "value" {
		t.Errorf("expected value, got %q", got["key"])
	}
}

func TestAtomicWriteJSON_NoPartialOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fail.json")

	// channels cannot be marshaled to JSON
	err := AtomicWriteJSON(path, make(chan int), 0600)
	if err == nil {
		t.Fatal("expected error for unencodable value")
	}

	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("file should not exist after failed write")
	}

	// also verify no temp files left behind
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		t.Errorf("leftover file: %s", e.Name())
	}
}

func TestAtomicWriteJSON_Permissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "perms.json")

	if err := AtomicWriteJSON(path, "test", 0600); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("expected 0600, got %o", info.Mode().Perm())
	}
}
