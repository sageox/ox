package paths

import (
	"strings"
	"testing"
)

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

func TestConfigFiles(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "OX_XDG_DISABLE")
	defer restoreEnv(saved)
	clearXDGEnv()

	tests := []struct {
		name     string
		fn       func() string
		wantFile string
	}{
		{"GitCredentialsFile", GitCredentialsFile, "git-credentials.json"},
		{"VerificationCacheFile", VerificationCacheFile, "verification-cache.json"},
		{"MachineIDFile", MachineIDFile, "machine-id"},
		{"UserConfigFile", UserConfigFile, "config.yaml"},
		{"AuthFile", AuthFile, "auth.json"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.fn()
			if !strings.HasSuffix(path, tt.wantFile) {
				t.Errorf("%s() = %q, want suffix %q", tt.name, path, tt.wantFile)
			}
			// all config files must live under ConfigDir
			if !strings.HasPrefix(path, ConfigDir()) {
				t.Errorf("%s() = %q, want prefix %q", tt.name, path, ConfigDir())
			}
		})
	}
}

func TestDaemonCacheDir(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "OX_XDG_DISABLE")
	defer restoreEnv(saved)
	clearXDGEnv()

	dir := DaemonCacheDir()
	cacheDir := CacheDir()
	if dir != cacheDir {
		t.Errorf("DaemonCacheDir() = %q, want %q (same as CacheDir)", dir, cacheDir)
	}
}
