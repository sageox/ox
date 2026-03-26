package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallProjectOpenCodeHooks_NewInstall(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	err := InstallProjectOpenCodeHooks(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pluginPath := filepath.Join(tmpDir, openCodeProjectPath, openCodePluginFileName)
	content, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatalf("expected plugin file at %s, got error: %v", pluginPath, err)
	}
	if string(content) != openCodePluginTemplate {
		t.Error("plugin content does not match template")
	}
}

func TestInstallProjectOpenCodeHooks_Idempotent(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// install twice
	if err := InstallProjectOpenCodeHooks(tmpDir); err != nil {
		t.Fatalf("first install failed: %v", err)
	}
	if err := InstallProjectOpenCodeHooks(tmpDir); err != nil {
		t.Fatalf("second install failed: %v", err)
	}

	pluginPath := filepath.Join(tmpDir, openCodeProjectPath, openCodePluginFileName)
	content, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != openCodePluginTemplate {
		t.Error("content should match template after double install")
	}
}

func TestHasProjectOpenCodeHooks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		setup    func(dir string)
		expected bool
	}{
		{
			name:     "no plugin",
			setup:    func(dir string) {},
			expected: false,
		},
		{
			name: "plugin installed",
			setup: func(dir string) {
				path := filepath.Join(dir, openCodeProjectPath, openCodePluginFileName)
				os.MkdirAll(filepath.Dir(path), 0755)
				os.WriteFile(path, []byte(openCodePluginTemplate), 0644)
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tmpDir := t.TempDir()
			tt.setup(tmpDir)
			got := HasProjectOpenCodeHooks(tmpDir)
			if got != tt.expected {
				t.Errorf("HasProjectOpenCodeHooks() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestOpenCodePluginTemplate_ContainsExpectedContent(t *testing.T) {
	t.Parallel()

	if len(openCodePluginTemplate) == 0 {
		t.Error("openCodePluginTemplate should not be empty")
	}
	checks := []string{
		"ox agent prime",
		"session.created",
		"Plugin",
	}
	for _, check := range checks {
		if !strings.Contains(openCodePluginTemplate, check) {
			t.Errorf("openCodePluginTemplate should contain %q", check)
		}
	}
}
