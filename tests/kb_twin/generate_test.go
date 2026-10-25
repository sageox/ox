//go:build kb_twin

package kb_twin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigGitIdentityIgnoresInheritedConfigOverrides(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}

	alternateConfig := filepath.Join(t.TempDir(), "inherited.gitconfig")
	originalConfig := []byte("[user]\n\temail = inherited@test.invalid\n")
	if err := os.WriteFile(alternateConfig, originalConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG", alternateConfig)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "user.name")
	t.Setenv("GIT_CONFIG_VALUE_0", "Inherited Identity")
	t.Setenv("GIT_CONFIG_SYSTEM", "/nonexistent/inherited-system-config")
	t.Setenv("GIT_TERMINAL_PROMPT", "1")

	repoDir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runGit(repoDir, "init"); err != nil {
		t.Fatal(err)
	}
	if err := configGitIdentity(repoDir); err != nil {
		t.Fatal(err)
	}

	localConfig, err := os.ReadFile(filepath.Join(repoDir, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"kb-twin@test.invalid", "KB Twin"} {
		if !strings.Contains(string(localConfig), expected) {
			t.Errorf("local config missing %q:\n%s", expected, localConfig)
		}
	}
	alternateAfter, err := os.ReadFile(alternateConfig)
	if err != nil {
		t.Fatal(err)
	}
	if string(alternateAfter) != string(originalConfig) {
		t.Errorf("inherited GIT_CONFIG was modified:\n%s", alternateAfter)
	}

	values := make(map[string][]string)
	for _, entry := range isolatedGitEnv() {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			values[name] = append(values[name], value)
		}
	}
	for _, name := range []string{"GIT_CONFIG", "GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_CONFIG_SYSTEM"} {
		if got := values[name]; len(got) != 0 {
			t.Errorf("%s leaked into isolated Git environment: %v", name, got)
		}
	}
	if got := values["GIT_CONFIG_GLOBAL"]; len(got) != 1 || got[0] != os.DevNull {
		t.Errorf("GIT_CONFIG_GLOBAL = %v, want [%q]", got, os.DevNull)
	}
	if got := values["GIT_CONFIG_NOSYSTEM"]; len(got) != 1 || got[0] != "1" {
		t.Errorf("GIT_CONFIG_NOSYSTEM = %v, want [1]", got)
	}
	if got := values["GIT_TERMINAL_PROMPT"]; len(got) != 1 || got[0] != "0" {
		t.Errorf("GIT_TERMINAL_PROMPT = %v, want [0]", got)
	}
}
