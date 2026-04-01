package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizePathComponent(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"with space", "with_space"},
		{"with/slash", "with_slash"},
		{"with\\backslash", "with_backslash"},
		{"with:colon", "with_colon"},
		{"with*asterisk", "with_asterisk"},
		{"with?question", "with_question"},
		{"with\"quote", "with_quote"},
		{"with<less", "with_less"},
		{"with>greater", "with_greater"},
		{"with|pipe", "with_pipe"},
		{"..traversal", "traversal"}, // ".." is replaced with "_", then leading "_" is trimmed
		{"", "unknown"},
		{"___", "unknown"},
		{"...", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizePathComponent(tt.input)
			if got != tt.want {
				t.Errorf("sanitizePathComponent(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestEnsureDir(t *testing.T) {
	tmpDir := t.TempDir()
	testDir := filepath.Join(tmpDir, "a", "b", "c")

	path, err := EnsureDir(testDir)
	if err != nil {
		t.Fatalf("EnsureDir() error = %v", err)
	}
	if path != testDir {
		t.Errorf("EnsureDir() returned %q, want %q", path, testDir)
	}

	info, err := os.Stat(testDir)
	if err != nil {
		t.Fatalf("os.Stat() error = %v", err)
	}
	if !info.IsDir() {
		t.Error("EnsureDir() did not create a directory")
	}
}

func TestEnsureDirForFile(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "a", "b", "file.txt")

	path, err := EnsureDirForFile(testFile)
	if err != nil {
		t.Fatalf("EnsureDirForFile() error = %v", err)
	}
	if path != testFile {
		t.Errorf("EnsureDirForFile() returned %q, want %q", path, testFile)
	}

	parentDir := filepath.Dir(testFile)
	info, err := os.Stat(parentDir)
	if err != nil {
		t.Fatalf("os.Stat() error = %v", err)
	}
	if !info.IsDir() {
		t.Error("EnsureDirForFile() did not create parent directory")
	}
}

func TestEndpointSlug(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "OX_XDG_DISABLE", "XDG_DATA_HOME")
	defer restoreEnv(saved)

	t.Run("extracts endpoint from teams path", func(t *testing.T) {
		clearXDGEnv()
		// construct a path using the actual DataDir to ensure consistency
		dataDir := DataDir()
		testPath := filepath.Join(dataDir, "sageox.ai", "teams", "team_abc")
		slug := EndpointSlug(testPath)
		if slug != "sageox.ai" {
			t.Errorf("EndpointSlug(%q) = %q, want sageox.ai", testPath, slug)
		}
	})

	t.Run("extracts endpoint from ledgers path", func(t *testing.T) {
		clearXDGEnv()
		dataDir := DataDir()
		testPath := filepath.Join(dataDir, "localhost", "ledgers", "xyz123")
		slug := EndpointSlug(testPath)
		if slug != "localhost" {
			t.Errorf("EndpointSlug(%q) = %q, want localhost", testPath, slug)
		}
	})

	t.Run("extracts endpoint from staging path", func(t *testing.T) {
		clearXDGEnv()
		dataDir := DataDir()
		testPath := filepath.Join(dataDir, "staging.sageox.ai", "teams", "myteam")
		slug := EndpointSlug(testPath)
		if slug != "staging.sageox.ai" {
			t.Errorf("EndpointSlug(%q) = %q, want staging.sageox.ai", testPath, slug)
		}
	})

	t.Run("returns empty for non-sageox path", func(t *testing.T) {
		clearXDGEnv()
		testPath := "/some/other/path"
		slug := EndpointSlug(testPath)
		if slug != "" {
			t.Errorf("EndpointSlug(%q) = %q, want empty", testPath, slug)
		}
	})

	t.Run("returns empty for empty path", func(t *testing.T) {
		clearXDGEnv()
		slug := EndpointSlug("")
		if slug != "" {
			t.Errorf("EndpointSlug(\"\") = %q, want empty", slug)
		}
	})

	t.Run("returns empty for data dir itself", func(t *testing.T) {
		clearXDGEnv()
		dataDir := DataDir()
		slug := EndpointSlug(dataDir)
		if slug != "" {
			t.Errorf("EndpointSlug(%q) = %q, want empty", dataDir, slug)
		}
	})

	t.Run("handles custom XDG_DATA_HOME", func(t *testing.T) {
		clearXDGEnv()
		os.Setenv("XDG_DATA_HOME", "/custom/data")
		testPath := "/custom/data/sageox/myendpoint/teams/team123"
		slug := EndpointSlug(testPath)
		if slug != "myendpoint" {
			t.Errorf("EndpointSlug(%q) = %q, want myendpoint", testPath, slug)
		}
	})
}
