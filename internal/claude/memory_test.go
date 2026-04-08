package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectHash(t *testing.T) {
	t.Run("known path", func(t *testing.T) {
		// use a temp dir so we have a real absolute path
		dir := t.TempDir()
		hash, err := ProjectHash(dir)
		if err != nil {
			t.Fatalf("ProjectHash(%q): %v", dir, err)
		}

		// hash should be the absolute path with separators replaced by "-"
		want := strings.ReplaceAll(dir, string(filepath.Separator), "-")
		if hash != want {
			t.Errorf("ProjectHash(%q) = %q, want %q", dir, hash, want)
		}
	})

	t.Run("relative path resolved", func(t *testing.T) {
		hash, err := ProjectHash(".")
		if err != nil {
			t.Fatalf("ProjectHash(.): %v", err)
		}

		// should not contain any path separators (all replaced with "-")
		if strings.Contains(hash, string(filepath.Separator)) {
			t.Errorf("hash should not contain %q: got %q", string(filepath.Separator), hash)
		}

		// verify hash is non-empty and all separators are replaced
		if hash == "" {
			t.Error("hash should not be empty")
		}
	})
}

func TestProjectMemoryDir(t *testing.T) {
	dir := t.TempDir()
	memDir, err := ProjectMemoryDir(dir)
	if err != nil {
		t.Fatalf("ProjectMemoryDir(%q): %v", dir, err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}

	// should be under ~/.claude/projects/
	prefix := filepath.Join(home, ".claude", "projects")
	if !strings.HasPrefix(memDir, prefix) {
		t.Errorf("expected prefix %q, got %q", prefix, memDir)
	}

	// should end with /memory
	if filepath.Base(memDir) != "memory" {
		t.Errorf("expected path to end with 'memory', got %q", memDir)
	}

	// the project hash segment should match ProjectHash output
	hash, err := ProjectHash(dir)
	if err != nil {
		t.Fatalf("ProjectHash: %v", err)
	}
	wantDir := filepath.Join(prefix, hash, "memory")
	if memDir != wantDir {
		t.Errorf("ProjectMemoryDir = %q, want %q", memDir, wantDir)
	}
}

func TestReadMemoryFiles(t *testing.T) {
	t.Run("reads files from directory", func(t *testing.T) {
		dir := t.TempDir()

		// create some memory files
		if err := os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("# Memory\nkey decisions"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "debugging.md"), []byte("debug notes"), 0o644); err != nil {
			t.Fatal(err)
		}
		// create a hidden file that should be skipped
		if err := os.WriteFile(filepath.Join(dir, ".DS_Store"), []byte("junk"), 0o644); err != nil {
			t.Fatal(err)
		}

		files, err := ReadMemoryFiles(dir)
		if err != nil {
			t.Fatalf("ReadMemoryFiles: %v", err)
		}

		if len(files) != 2 {
			t.Fatalf("expected 2 files, got %d: %v", len(files), keys(files))
		}

		if string(files["MEMORY.md"]) != "# Memory\nkey decisions" {
			t.Errorf("MEMORY.md content = %q", string(files["MEMORY.md"]))
		}
		if string(files["debugging.md"]) != "debug notes" {
			t.Errorf("debugging.md content = %q", string(files["debugging.md"]))
		}
	})

	t.Run("non-existent directory returns empty map", func(t *testing.T) {
		files, err := ReadMemoryFiles(filepath.Join(t.TempDir(), "nonexistent"))
		if err != nil {
			t.Fatalf("ReadMemoryFiles: %v", err)
		}
		if len(files) != 0 {
			t.Errorf("expected empty map, got %d entries", len(files))
		}
	})
}

func keys(m map[string][]byte) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
