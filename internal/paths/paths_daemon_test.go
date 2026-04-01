package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestDaemonPaths(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE")
	defer restoreEnv(saved)

	clearXDGEnv()
	wsID := "a1b2c3d4"

	t.Run("socket file", func(t *testing.T) {
		path := DaemonSocketFile(wsID)
		want := "daemon-" + wsID + ".sock"
		if !strings.HasSuffix(path, want) {
			t.Errorf("DaemonSocketFile(%q) = %q, want suffix %q", wsID, path, want)
		}
	})

	t.Run("pid file", func(t *testing.T) {
		path := DaemonPidFile(wsID)
		want := "daemon-" + wsID + ".pid"
		if !strings.HasSuffix(path, want) {
			t.Errorf("DaemonPidFile(%q) = %q, want suffix %q", wsID, path, want)
		}
	})

	t.Run("registry file", func(t *testing.T) {
		path := DaemonRegistryFile()
		if !strings.HasSuffix(path, "registry.json") {
			t.Errorf("DaemonRegistryFile() = %q, want suffix registry.json", path)
		}
	})

	t.Run("log file", func(t *testing.T) {
		repoID := "repo_test123"
		path := DaemonLogFile(repoID, wsID)
		want := fmt.Sprintf("daemon_%s_%s.log", repoID, wsID)
		if !strings.HasSuffix(path, want) {
			t.Errorf("DaemonLogFile(%q, %q) = %q, want suffix %q", repoID, wsID, path, want)
		}
	})
}

func TestGuidanceCacheDir(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "OX_XDG_DISABLE")
	defer restoreEnv(saved)

	clearXDGEnv()
	dir := GuidanceCacheDir()
	// In XDG mode (default), path ends with sageox/guidance
	// In legacy mode, path ends with cache/guidance
	if !strings.HasSuffix(dir, "guidance") {
		t.Errorf("GuidanceCacheDir() = %q, want suffix guidance", dir)
	}
}

func TestSessionCacheDir(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE")
	defer restoreEnv(saved)

	clearXDGEnv()

	t.Run("base dir", func(t *testing.T) {
		dir := SessionCacheDir("")
		if !strings.HasSuffix(dir, "sessions") {
			t.Errorf("SessionCacheDir(\"\") = %q, want suffix sessions", dir)
		}
	})

	t.Run("with repo id", func(t *testing.T) {
		dir := SessionCacheDir("repo123")
		if !strings.HasSuffix(dir, filepath.Join("sessions", "repo123")) {
			t.Errorf("SessionCacheDir(\"repo123\") = %q, want suffix sessions/repo123", dir)
		}
	})
}

func TestHeartbeatCacheDir(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "OX_XDG_DISABLE")
	defer restoreEnv(saved)
	clearXDGEnv()

	t.Run("valid endpoint", func(t *testing.T) {
		dir := HeartbeatCacheDir("https://sageox.ai")
		if !strings.HasSuffix(dir, filepath.Join("sageox.ai", "heartbeats")) {
			t.Errorf("HeartbeatCacheDir() = %q, want suffix sageox.ai/heartbeats", dir)
		}
		if !strings.HasPrefix(dir, CacheDir()) {
			t.Errorf("HeartbeatCacheDir() = %q, want prefix %q", dir, CacheDir())
		}
	})

	t.Run("normalizes api prefix", func(t *testing.T) {
		dir := HeartbeatCacheDir("https://api.sageox.ai")
		if !strings.Contains(dir, filepath.Join("sageox.ai", "heartbeats")) {
			t.Errorf("HeartbeatCacheDir(api.sageox.ai) = %q, want to contain sageox.ai/heartbeats", dir)
		}
	})

	t.Run("empty endpoint panics", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("HeartbeatCacheDir('') should panic when endpoint is empty")
			}
		}()
		HeartbeatCacheDir("")
	})
}

func TestTempDir_UsernameFallbacks(t *testing.T) {
	t.Run("uses USER env var", func(t *testing.T) {
		t.Setenv("USER", "testperson")
		t.Setenv("USERNAME", "")
		dir := TempDir()
		if !strings.Contains(dir, "testperson") {
			t.Errorf("TempDir() = %q, want to contain 'testperson'", dir)
		}
	})

	t.Run("falls back to USERNAME when USER is empty", func(t *testing.T) {
		t.Setenv("USER", "")
		t.Setenv("USERNAME", "winuser")
		dir := TempDir()
		if !strings.Contains(dir, "winuser") {
			t.Errorf("TempDir() = %q, want to contain 'winuser'", dir)
		}
	})

	t.Run("falls back to sageox when both empty", func(t *testing.T) {
		t.Setenv("USER", "")
		t.Setenv("USERNAME", "")
		dir := TempDir()
		// the path should contain "sageox" as the username fallback
		// path is /tmp/sageox/sageox — the first sageox is the username, second is the app dir
		parts := strings.Split(dir, string(filepath.Separator))
		// find the username part (after /tmp/ or base temp dir)
		found := false
		for i, p := range parts {
			if p == "sageox" && i > 0 && i < len(parts)-1 {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("TempDir() = %q, want 'sageox' as fallback username in path", dir)
		}
	})
}

// TestAlternateSessionCacheDirsCoverAllLocations verifies that the union of
// SessionCacheDir + AlternateSessionCacheDirs always covers all known cache
// locations regardless of XDG_CACHE_HOME setting.
func TestAlternateSessionCacheDirsCoverAllLocations(t *testing.T) {
	repoID := "repo-test-123"

	tests := []struct {
		name            string
		xdgCacheHome    string   // empty means unset
		expectedInUnion []string // path suffixes that must appear in the union
	}{
		{
			name:         "XDG_CACHE_HOME unset — default is ~/.cache",
			xdgCacheHome: "",
			expectedInUnion: []string{
				filepath.Join(".cache", "sageox", "sessions", repoID),
				filepath.Join("Library", "Caches", "sageox", "sessions", repoID),
			},
		},
		{
			name:         "XDG_CACHE_HOME set to macOS native",
			xdgCacheHome: "LIBRARY_CACHES", // placeholder, resolved below
			expectedInUnion: []string{
				filepath.Join("Library", "Caches", "sageox", "sessions", repoID),
				filepath.Join(".cache", "sageox", "sessions", repoID),
			},
		},
		{
			name:         "XDG_CACHE_HOME set to custom path",
			xdgCacheHome: "CUSTOM", // placeholder, resolved below
			expectedInUnion: []string{
				filepath.Join("custom-cache", "sageox", "sessions", repoID),
				filepath.Join(".cache", "sageox", "sessions", repoID),
				filepath.Join("Library", "Caches", "sageox", "sessions", repoID),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpHome := t.TempDir()
			t.Setenv("HOME", tmpHome)
			resetHomeDir(t) // reset sync.Once cache so getHomeDir() picks up new HOME

			// create all candidate directories on disk so AlternateSessionCacheDirs finds them
			for _, suffix := range []string{
				filepath.Join(".cache", "sageox", "sessions", repoID),
				filepath.Join("Library", "Caches", "sageox", "sessions", repoID),
			} {
				if err := os.MkdirAll(filepath.Join(tmpHome, suffix), 0755); err != nil {
					t.Fatal(err)
				}
			}

			// resolve XDG_CACHE_HOME
			switch tc.xdgCacheHome {
			case "":
				t.Setenv("XDG_CACHE_HOME", "")
			case "LIBRARY_CACHES":
				t.Setenv("XDG_CACHE_HOME", filepath.Join(tmpHome, "Library", "Caches"))
			case "CUSTOM":
				customCache := filepath.Join(tmpHome, "custom-cache")
				if err := os.MkdirAll(filepath.Join(customCache, "sageox", "sessions", repoID), 0755); err != nil {
					t.Fatal(err)
				}
				t.Setenv("XDG_CACHE_HOME", customCache)
			}

			// collect union: current + alternates
			current := SessionCacheDir(repoID)
			alternates := AlternateSessionCacheDirs(repoID)
			union := append([]string{current}, alternates...)

			// verify all expected paths appear
			for _, suffix := range tc.expectedInUnion {
				expected := filepath.Join(tmpHome, suffix)
				found := false
				for _, p := range union {
					if p == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected %s in union, got %v", expected, union)
				}
			}
		})
	}
}

// resetHomeDir resets the cached home directory for testing.
// Must be called in tests that override HOME to ensure getHomeDir() picks up the new value.
func resetHomeDir(t *testing.T) {
	t.Helper()
	old := homeDir
	homeDirOnce = sync.Once{}
	homeDir = ""
	t.Cleanup(func() {
		homeDirOnce = sync.Once{}
		homeDir = old
	})
}
