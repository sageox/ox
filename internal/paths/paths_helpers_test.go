package paths

import (
	"os"
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
