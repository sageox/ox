package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGeminiSettings_JSONRoundTrip(t *testing.T) {
	t.Parallel()

	settings := GeminiSettings{
		Hooks: map[string]GeminiHook{
			"SessionStart": {
				Command: "ox agent prime",
				Timeout: 30,
			},
		},
	}

	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded GeminiSettings
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	hook, ok := decoded.Hooks["SessionStart"]
	if !ok {
		t.Fatal("expected SessionStart hook")
	}
	if hook.Command != "ox agent prime" {
		t.Errorf("command = %q, want %q", hook.Command, "ox agent prime")
	}
	if hook.Timeout != 30 {
		t.Errorf("timeout = %d, want 30", hook.Timeout)
	}
}

func TestGeminiSettings_TimeoutOmittedWhenZero(t *testing.T) {
	t.Parallel()

	settings := GeminiSettings{
		Hooks: map[string]GeminiHook{
			"SessionStart": {Command: "ox agent prime"},
		},
	}

	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}

	var raw map[string]interface{}
	json.Unmarshal(data, &raw)
	hooks := raw["hooks"].(map[string]interface{})
	hook := hooks["SessionStart"].(map[string]interface{})
	if _, exists := hook["timeout"]; exists {
		t.Error("timeout should be omitted when zero")
	}
}

func TestReadGeminiSettings_NonexistentFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	settings, err := readGeminiSettings(true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if settings == nil {
		t.Fatal("expected non-nil settings")
	}
	if settings.Hooks == nil {
		t.Error("expected initialized hooks map")
	}
	if len(settings.Hooks) != 0 {
		t.Errorf("expected empty hooks map, got %d entries", len(settings.Hooks))
	}
}

func TestReadGeminiSettings_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	settingsPath := filepath.Join(tmpDir, geminiUserPath, geminiSettingsFileName)
	os.MkdirAll(filepath.Dir(settingsPath), 0755)
	os.WriteFile(settingsPath, []byte(""), 0644)

	settings, err := readGeminiSettings(true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if settings.Hooks == nil {
		t.Error("expected initialized hooks map for empty file")
	}
}

func TestReadGeminiSettings_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	settingsPath := filepath.Join(tmpDir, geminiUserPath, geminiSettingsFileName)
	os.MkdirAll(filepath.Dir(settingsPath), 0755)
	os.WriteFile(settingsPath, []byte("not json"), 0644)

	_, err := readGeminiSettings(true)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestHasGeminiHooks_NoHooks(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	if hasGeminiHooks(true) {
		t.Error("expected false when no hooks installed")
	}
}

func TestInstallGeminiHooks_UserLevel(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	err := installGeminiHooks(true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !hasGeminiHooks(true) {
		t.Error("expected hooks to be installed")
	}
}

func TestInstallGeminiHooks_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	if err := installGeminiHooks(true); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if err := installGeminiHooks(true); err != nil {
		t.Fatalf("second install: %v", err)
	}
}

func TestUninstallGeminiHooks_NotInstalled(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	err := uninstallGeminiHooks(true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUninstallGeminiHooks_RemovesHook(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	if err := installGeminiHooks(true); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := uninstallGeminiHooks(true); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if hasGeminiHooks(true) {
		t.Error("expected hooks to be removed after uninstall")
	}
}

func TestListGeminiHooks(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	status := listGeminiHooks()
	if status == nil {
		t.Fatal("expected non-nil status map")
	}
	if _, ok := status["Project"]; !ok {
		t.Error("expected 'Project' key in status")
	}
	if _, ok := status["User"]; !ok {
		t.Error("expected 'User' key in status")
	}
}
