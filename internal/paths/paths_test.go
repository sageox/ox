package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/endpoint"
)

// saveEnv saves environment variables for restoration
func saveEnv(keys ...string) map[string]string {
	saved := make(map[string]string)
	for _, k := range keys {
		saved[k] = os.Getenv(k)
	}
	return saved
}

// restoreEnv restores environment variables
func restoreEnv(saved map[string]string) {
	for k, v := range saved {
		if v == "" {
			os.Unsetenv(k)
		} else {
			os.Setenv(k, v)
		}
	}
}

// clearXDGEnv clears all XDG-related environment variables
func clearXDGEnv() {
	os.Unsetenv("OX_XDG_ENABLE")
	os.Unsetenv("OX_XDG_DISABLE")
	os.Unsetenv("XDG_CONFIG_HOME")
	os.Unsetenv("XDG_DATA_HOME")
	os.Unsetenv("XDG_CACHE_HOME")
	os.Unsetenv("XDG_STATE_HOME")
	os.Unsetenv("XDG_RUNTIME_DIR")
}

// setLegacyMode enables legacy mode (non-XDG paths)
func setLegacyMode() {
	clearXDGEnv()
	os.Setenv("OX_XDG_DISABLE", "1")
}

func TestSageoxDir(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "OX_XDG_DISABLE")
	defer restoreEnv(saved)

	t.Run("default mode returns empty (XDG is default)", func(t *testing.T) {
		clearXDGEnv()
		dir := SageoxDir()
		// XDG is now the default, so SageoxDir returns empty
		if dir != "" {
			t.Errorf("SageoxDir() = %q in default (XDG) mode, want empty", dir)
		}
	})

	t.Run("legacy mode returns .sageox", func(t *testing.T) {
		setLegacyMode()
		dir := SageoxDir()
		if dir == "" {
			t.Error("SageoxDir() returned empty string in legacy mode")
		}
		if !strings.HasSuffix(dir, ".sageox") {
			t.Errorf("SageoxDir() = %q, want suffix .sageox", dir)
		}
	})

	t.Run("OX_XDG_ENABLE still works for compatibility", func(t *testing.T) {
		clearXDGEnv()
		os.Setenv("OX_XDG_ENABLE", "1")
		dir := SageoxDir()
		if dir != "" {
			t.Errorf("SageoxDir() with OX_XDG_ENABLE=1 = %q, want empty", dir)
		}
	})
}

func TestConfigDir(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "OX_XDG_DISABLE", "XDG_CONFIG_HOME")
	defer restoreEnv(saved)

	t.Run("default mode uses XDG", func(t *testing.T) {
		clearXDGEnv()
		dir := ConfigDir()
		// XDG is now the default
		if !strings.Contains(dir, ".config") || !strings.HasSuffix(dir, "sageox") {
			t.Errorf("ConfigDir() = %q, want ~/.config/sageox", dir)
		}
	})

	t.Run("default mode respects XDG_CONFIG_HOME", func(t *testing.T) {
		clearXDGEnv()
		os.Setenv("XDG_CONFIG_HOME", "/custom/config")
		dir := ConfigDir()
		want := "/custom/config/sageox"
		if dir != want {
			t.Errorf("ConfigDir() = %q, want %q", dir, want)
		}
	})

	t.Run("legacy mode uses .sageox", func(t *testing.T) {
		setLegacyMode()
		dir := ConfigDir()
		if !strings.Contains(dir, ".sageox") || !strings.HasSuffix(dir, "config") {
			t.Errorf("ConfigDir() = %q in legacy mode, want ~/.sageox/config", dir)
		}
	})

	t.Run("legacy mode ignores XDG_CONFIG_HOME", func(t *testing.T) {
		setLegacyMode()
		os.Setenv("XDG_CONFIG_HOME", "/custom/config")
		dir := ConfigDir()
		// in legacy mode, XDG_CONFIG_HOME should be ignored
		if strings.Contains(dir, "/custom/") {
			t.Errorf("ConfigDir() = %q, should ignore XDG_CONFIG_HOME in legacy mode", dir)
		}
		if !strings.Contains(dir, ".sageox/config") {
			t.Errorf("ConfigDir() = %q, want ~/.sageox/config", dir)
		}
	})
}

func TestXDGPartialConfiguration(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR")
	defer restoreEnv(saved)

	t.Run("XDG mode with only some vars set", func(t *testing.T) {
		clearXDGEnv()
		os.Setenv("OX_XDG_ENABLE", "1")
		// only set XDG_CONFIG_HOME, leave others unset
		os.Setenv("XDG_CONFIG_HOME", "/custom/config")

		// config should use custom path
		configDir := ConfigDir()
		if configDir != "/custom/config/sageox" {
			t.Errorf("ConfigDir() = %q, want /custom/config/sageox", configDir)
		}

		// data should use default XDG path
		dataDir := DataDir()
		if !strings.Contains(dataDir, ".local/share/sageox") {
			t.Errorf("DataDir() = %q, want to contain .local/share/sageox", dataDir)
		}

		// cache should use default XDG path
		cacheDir := CacheDir()
		if !strings.Contains(cacheDir, ".cache/sageox") {
			t.Errorf("CacheDir() = %q, want to contain .cache/sageox", cacheDir)
		}
	})

	t.Run("XDG mode with mixed custom paths", func(t *testing.T) {
		clearXDGEnv()
		os.Setenv("OX_XDG_ENABLE", "1")
		os.Setenv("XDG_CONFIG_HOME", "/a/config")
		os.Setenv("XDG_DATA_HOME", "/b/data")
		// leave cache and runtime unset

		if got := ConfigDir(); got != "/a/config/sageox" {
			t.Errorf("ConfigDir() = %q, want /a/config/sageox", got)
		}
		if got := DataDir(); got != "/b/data/sageox" {
			t.Errorf("DataDir() = %q, want /b/data/sageox", got)
		}
		// cache uses default
		if got := CacheDir(); !strings.Contains(got, ".cache/sageox") {
			t.Errorf("CacheDir() = %q, want to contain .cache/sageox", got)
		}
	})

	t.Run("XDG runtime dir for daemon state", func(t *testing.T) {
		clearXDGEnv()
		os.Setenv("OX_XDG_ENABLE", "1")
		os.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

		stateDir := StateDir()
		if stateDir != "/run/user/1000/sageox" {
			t.Errorf("StateDir() = %q, want /run/user/1000/sageox", stateDir)
		}
	})

	t.Run("XDG runtime dir fallback to tmp", func(t *testing.T) {
		clearXDGEnv()
		os.Setenv("OX_XDG_ENABLE", "1")
		// XDG_RUNTIME_DIR not set

		stateDir := StateDir()
		// should fall back to temp dir
		if !strings.Contains(stateDir, "sageox") {
			t.Errorf("StateDir() = %q, want to contain sageox", stateDir)
		}
	})
}

func TestConsistencyBetweenModes(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR")
	defer restoreEnv(saved)

	t.Run("all paths use consistent base in default mode", func(t *testing.T) {
		clearXDGEnv()

		baseDir := SageoxDir()
		configDir := ConfigDir()
		dataDir := DataDir()
		cacheDir := CacheDir()
		stateDir := StateDir()

		// all should be under ~/.sageox/
		for name, dir := range map[string]string{
			"config": configDir,
			"data":   dataDir,
			"cache":  cacheDir,
			"state":  stateDir,
		} {
			if !strings.HasPrefix(dir, baseDir) {
				t.Errorf("%sDir() = %q, want prefix %q", name, dir, baseDir)
			}
		}
	})

	t.Run("specific files resolve under correct dirs", func(t *testing.T) {
		clearXDGEnv()

		// config files
		if !strings.HasPrefix(UserConfigFile(), ConfigDir()) {
			t.Errorf("UserConfigFile() not under ConfigDir()")
		}
		if !strings.HasPrefix(AuthFile(), ConfigDir()) {
			t.Errorf("AuthFile() not under ConfigDir()")
		}

		// cache files
		if !strings.HasPrefix(GuidanceCacheDir(), CacheDir()) {
			t.Errorf("GuidanceCacheDir() not under CacheDir()")
		}

		// data files (production endpoint - should be under DataDir/<endpoint>/teams/)
		teamsDir := TeamsDataDir(endpoint.Production)
		if !strings.HasPrefix(teamsDir, DataDir()) {
			t.Errorf("TeamsDataDir() not under DataDir()")
		}
		// verify endpoint slug is included in path
		if !strings.Contains(teamsDir, "sageox.ai") {
			t.Errorf("TeamsDataDir() = %q, should contain endpoint slug sageox.ai", teamsDir)
		}

		// ledger files (production endpoint - should be under DataDir/<endpoint>/ledgers/)
		ledgersDir := LedgersDataDir("repo123", endpoint.Production)
		if !strings.HasPrefix(ledgersDir, DataDir()) {
			t.Errorf("LedgersDataDir() not under DataDir()")
		}
		if !strings.Contains(ledgersDir, "sageox.ai") {
			t.Errorf("LedgersDataDir() = %q, should contain endpoint slug sageox.ai", ledgersDir)
		}

		// state files
		if !strings.HasPrefix(DaemonStateDir(), StateDir()) {
			t.Errorf("DaemonStateDir() not under StateDir()")
		}
	})
}

func TestDataDir(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "OX_XDG_DISABLE", "XDG_DATA_HOME")
	defer restoreEnv(saved)

	t.Run("default mode uses XDG", func(t *testing.T) {
		clearXDGEnv()
		dir := DataDir()
		// XDG is now the default
		if !strings.Contains(dir, ".local/share") || !strings.HasSuffix(dir, "sageox") {
			t.Errorf("DataDir() = %q, want ~/.local/share/sageox", dir)
		}
	})

	t.Run("default mode respects XDG_DATA_HOME", func(t *testing.T) {
		clearXDGEnv()
		os.Setenv("XDG_DATA_HOME", "/custom/data")
		dir := DataDir()
		want := "/custom/data/sageox"
		if dir != want {
			t.Errorf("DataDir() = %q, want %q", dir, want)
		}
	})

	t.Run("legacy mode uses .sageox", func(t *testing.T) {
		setLegacyMode()
		dir := DataDir()
		if !strings.Contains(dir, ".sageox") || !strings.HasSuffix(dir, "data") {
			t.Errorf("DataDir() = %q in legacy mode, want ~/.sageox/data", dir)
		}
	})
}

func TestCacheDir(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "OX_XDG_DISABLE", "XDG_CACHE_HOME")
	defer restoreEnv(saved)

	t.Run("default mode uses XDG", func(t *testing.T) {
		clearXDGEnv()
		dir := CacheDir()
		// XDG is now the default
		if !strings.Contains(dir, ".cache") || !strings.HasSuffix(dir, "sageox") {
			t.Errorf("CacheDir() = %q, want ~/.cache/sageox", dir)
		}
	})

	t.Run("default mode respects XDG_CACHE_HOME", func(t *testing.T) {
		clearXDGEnv()
		os.Setenv("XDG_CACHE_HOME", "/custom/cache")
		dir := CacheDir()
		want := "/custom/cache/sageox"
		if dir != want {
			t.Errorf("CacheDir() = %q, want %q", dir, want)
		}
	})

	t.Run("legacy mode uses .sageox", func(t *testing.T) {
		setLegacyMode()
		dir := CacheDir()
		if !strings.Contains(dir, ".sageox") || !strings.HasSuffix(dir, "cache") {
			t.Errorf("CacheDir() = %q in legacy mode, want ~/.sageox/cache", dir)
		}
	})
}

func TestStateDir(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "OX_XDG_DISABLE", "XDG_RUNTIME_DIR")
	defer restoreEnv(saved)

	t.Run("default mode uses XDG_RUNTIME_DIR", func(t *testing.T) {
		clearXDGEnv()
		os.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
		dir := StateDir()
		want := "/run/user/1000/sageox"
		if dir != want {
			t.Errorf("StateDir() = %q, want %q", dir, want)
		}
	})

	t.Run("default mode falls back to temp dir", func(t *testing.T) {
		clearXDGEnv()
		// no XDG_RUNTIME_DIR set, falls back to os.TempDir()
		dir := StateDir()
		// should contain "sageox" suffix
		if !strings.HasSuffix(dir, "sageox") {
			t.Errorf("StateDir() = %q, want suffix 'sageox'", dir)
		}
	})

	t.Run("legacy mode uses .sageox", func(t *testing.T) {
		setLegacyMode()
		dir := StateDir()
		if !strings.Contains(dir, ".sageox") || !strings.HasSuffix(dir, "state") {
			t.Errorf("StateDir() = %q in legacy mode, want ~/.sageox/state", dir)
		}
	})
}

func TestUserConfigFile(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE")
	defer restoreEnv(saved)

	clearXDGEnv()
	path := UserConfigFile()
	if !strings.HasSuffix(path, "config.yaml") {
		t.Errorf("UserConfigFile() = %q, want suffix config.yaml", path)
	}
}

func TestAuthFile(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE")
	defer restoreEnv(saved)

	clearXDGEnv()
	path := AuthFile()
	if !strings.HasSuffix(path, "auth.json") {
		t.Errorf("AuthFile() = %q, want suffix auth.json", path)
	}
}

func TestTeamsDataDir(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "SAGEOX_ENDPOINT")
	defer restoreEnv(saved)

	// all endpoints now include endpoint slug in path for consistent namespacing
	t.Run("production endpoint includes sageox.ai", func(t *testing.T) {
		clearXDGEnv()
		os.Unsetenv("SAGEOX_ENDPOINT")
		dir := TeamsDataDir("https://api.sageox.ai")
		// api.sageox.ai normalizes to sageox.ai
		if !strings.HasSuffix(dir, filepath.Join("sageox.ai", "teams")) {
			t.Errorf("TeamsDataDir(production) = %q, want suffix sageox.ai/teams", dir)
		}
	})

	t.Run("production without api prefix", func(t *testing.T) {
		clearXDGEnv()
		os.Unsetenv("SAGEOX_ENDPOINT")
		dir := TeamsDataDir("https://sageox.ai")
		if !strings.HasSuffix(dir, filepath.Join("sageox.ai", "teams")) {
			t.Errorf("TeamsDataDir(sageox.ai) = %q, want suffix sageox.ai/teams", dir)
		}
	})

	t.Run("staging endpoint", func(t *testing.T) {
		clearXDGEnv()
		os.Unsetenv("SAGEOX_ENDPOINT")
		dir := TeamsDataDir("https://staging.sageox.ai")
		if !strings.HasSuffix(dir, filepath.Join("staging.sageox.ai", "teams")) {
			t.Errorf("TeamsDataDir(staging) = %q, want suffix staging.sageox.ai/teams", dir)
		}
	})

	t.Run("localhost with port strips port", func(t *testing.T) {
		clearXDGEnv()
		os.Unsetenv("SAGEOX_ENDPOINT")
		dir := TeamsDataDir("http://localhost:8080")
		// NormalizeSlug strips port numbers
		if !strings.HasSuffix(dir, filepath.Join("localhost", "teams")) {
			t.Errorf("TeamsDataDir(localhost:8080) = %q, want suffix localhost/teams", dir)
		}
	})

	t.Run("empty endpoint panics", func(t *testing.T) {
		clearXDGEnv()
		defer func() {
			if r := recover(); r == nil {
				t.Error("TeamsDataDir('') should panic when endpoint is empty")
			}
		}()
		TeamsDataDir("")
	})
}

func TestTeamContextDir(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "SAGEOX_ENDPOINT")
	defer restoreEnv(saved)

	clearXDGEnv()
	os.Unsetenv("SAGEOX_ENDPOINT")

	t.Run("production endpoint paths", func(t *testing.T) {
		tests := []struct {
			name   string
			teamID string
			want   string
		}{
			{"simple id", "abc123", "abc123"},
			{"with slashes", "org/team", "org_team"},
			{"empty", "", "unknown"},
			{"with spaces", "my team", "my_team"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				dir := TeamContextDir(tt.teamID, "https://api.sageox.ai")
				if !strings.HasSuffix(dir, tt.want) {
					t.Errorf("TeamContextDir(%q, production) = %q, want suffix %q", tt.teamID, dir, tt.want)
				}
				// should be under data/sageox.ai/teams for production (now includes endpoint)
				if !strings.Contains(dir, filepath.Join("sageox.ai", "teams")) {
					t.Errorf("TeamContextDir(%q, production) = %q, should contain sageox.ai/teams", tt.teamID, dir)
				}
			})
		}
	})

	t.Run("staging endpoint paths", func(t *testing.T) {
		dir := TeamContextDir("team123", "https://staging.sageox.ai")
		if !strings.Contains(dir, filepath.Join("staging.sageox.ai", "teams", "team123")) {
			t.Errorf("TeamContextDir(team123, staging) = %q, want to contain staging.sageox.ai/teams/team123", dir)
		}
	})

	t.Run("localhost endpoint paths", func(t *testing.T) {
		dir := TeamContextDir("team456", "http://localhost:8080")
		// port is stripped by NormalizeSlug
		if !strings.Contains(dir, filepath.Join("localhost", "teams", "team456")) {
			t.Errorf("TeamContextDir(team456, localhost) = %q, want to contain localhost/teams/team456", dir)
		}
	})
}

func TestLedgersDataDir(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "SAGEOX_ENDPOINT")
	defer restoreEnv(saved)

	t.Run("production endpoint includes sageox.ai", func(t *testing.T) {
		clearXDGEnv()
		os.Unsetenv("SAGEOX_ENDPOINT")
		dir := LedgersDataDir("repo123", "https://api.sageox.ai")
		// api.sageox.ai normalizes to sageox.ai
		if !strings.HasSuffix(dir, filepath.Join("sageox.ai", "ledgers", "repo123")) {
			t.Errorf("LedgersDataDir(production) = %q, want suffix sageox.ai/ledgers/repo123", dir)
		}
	})

	t.Run("production without api prefix", func(t *testing.T) {
		clearXDGEnv()
		os.Unsetenv("SAGEOX_ENDPOINT")
		dir := LedgersDataDir("repo456", "https://sageox.ai")
		if !strings.HasSuffix(dir, filepath.Join("sageox.ai", "ledgers", "repo456")) {
			t.Errorf("LedgersDataDir(sageox.ai) = %q, want suffix sageox.ai/ledgers/repo456", dir)
		}
	})

	t.Run("staging endpoint", func(t *testing.T) {
		clearXDGEnv()
		os.Unsetenv("SAGEOX_ENDPOINT")
		dir := LedgersDataDir("repo789", "https://staging.sageox.ai")
		if !strings.HasSuffix(dir, filepath.Join("staging.sageox.ai", "ledgers", "repo789")) {
			t.Errorf("LedgersDataDir(staging) = %q, want suffix staging.sageox.ai/ledgers/repo789", dir)
		}
	})

	t.Run("localhost with port strips port", func(t *testing.T) {
		clearXDGEnv()
		os.Unsetenv("SAGEOX_ENDPOINT")
		dir := LedgersDataDir("repoabc", "http://localhost:8080")
		// NormalizeSlug strips port numbers
		if !strings.HasSuffix(dir, filepath.Join("localhost", "ledgers", "repoabc")) {
			t.Errorf("LedgersDataDir(localhost:8080) = %q, want suffix localhost/ledgers/repoabc", dir)
		}
	})

	t.Run("empty repoID returns base ledgers dir", func(t *testing.T) {
		clearXDGEnv()
		os.Unsetenv("SAGEOX_ENDPOINT")
		dir := LedgersDataDir("", "https://sageox.ai")
		if !strings.HasSuffix(dir, filepath.Join("sageox.ai", "ledgers")) {
			t.Errorf("LedgersDataDir('', sageox.ai) = %q, want suffix sageox.ai/ledgers", dir)
		}
		// should NOT have a trailing path component after ledgers
		if strings.HasSuffix(dir, filepath.Join("ledgers", "unknown")) {
			t.Errorf("LedgersDataDir('', sageox.ai) = %q, should not have unknown suffix", dir)
		}
	})

	t.Run("empty endpoint panics", func(t *testing.T) {
		clearXDGEnv()
		defer func() {
			if r := recover(); r == nil {
				t.Error("LedgersDataDir should panic when endpoint is empty")
			}
		}()
		LedgersDataDir("repo123", "")
	})

	t.Run("repoID sanitization", func(t *testing.T) {
		clearXDGEnv()
		os.Unsetenv("SAGEOX_ENDPOINT")
		tests := []struct {
			name   string
			repoID string
			want   string
		}{
			{"simple id", "abc123", "abc123"},
			{"with slashes", "org/repo", "org_repo"},
			{"with spaces", "my repo", "my_repo"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				dir := LedgersDataDir(tt.repoID, "https://sageox.ai")
				if !strings.HasSuffix(dir, tt.want) {
					t.Errorf("LedgersDataDir(%q) = %q, want suffix %q", tt.repoID, dir, tt.want)
				}
			})
		}
	})
}

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
