package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodePuppyPluginTemplate_ContainsExpectedContent(t *testing.T) {
	t.Parallel()

	if len(codePuppyPluginTemplate) == 0 {
		t.Error("codePuppyPluginTemplate should not be empty")
	}
	checks := []string{
		"ox",
		"agent",
		"prime",
		"register_callback",
		"startup",
	}
	for _, check := range checks {
		if !strings.Contains(codePuppyPluginTemplate, check) {
			t.Errorf("codePuppyPluginTemplate should contain %q", check)
		}
	}
}

func TestCodePuppyConstants(t *testing.T) {
	t.Parallel()

	if codePuppyPluginDir != "ox_prime" {
		t.Errorf("unexpected plugin dir: %q", codePuppyPluginDir)
	}
	if codePuppyPluginFileName != "register_callbacks.py" {
		t.Errorf("unexpected plugin filename: %q", codePuppyPluginFileName)
	}
}

func TestHasCodePuppyHooks_NoPlugin(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	got := hasCodePuppyHooks(true)
	if got {
		t.Error("expected false when no plugin installed")
	}
}

func TestInstallCodePuppyHooks_UserLevel(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	err := installCodePuppyHooks(true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedPath := filepath.Join(tmpDir, codePuppyUserPath, codePuppyPluginDir, codePuppyPluginFileName)
	content, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("expected plugin file, got error: %v", err)
	}
	if string(content) != codePuppyPluginTemplate {
		t.Error("plugin content does not match template")
	}
}

func TestInstallCodePuppyHooks_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	if err := installCodePuppyHooks(true); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if err := installCodePuppyHooks(true); err != nil {
		t.Fatalf("second install: %v", err)
	}
}

func TestUninstallCodePuppyHooks_NotInstalled(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	err := uninstallCodePuppyHooks(true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUninstallCodePuppyHooks_RemovesPlugin(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	if err := installCodePuppyHooks(true); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := uninstallCodePuppyHooks(true); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	expectedPath := filepath.Join(tmpDir, codePuppyUserPath, codePuppyPluginDir, codePuppyPluginFileName)
	if _, err := os.Stat(expectedPath); !os.IsNotExist(err) {
		t.Error("expected plugin file to be removed")
	}
}

func TestListCodePuppyHooks(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	status := listCodePuppyHooks()
	if status == nil {
		t.Fatal("expected non-nil status map")
	}
	if _, ok := status["User plugin"]; !ok {
		t.Error("expected 'User plugin' key in status")
	}
}
