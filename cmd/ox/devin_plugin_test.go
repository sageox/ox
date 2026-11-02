package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestInstallDevinPlugin(t *testing.T) {
	tests := []struct {
		name     string
		existing string // "" = no file
		wantErr  bool
		wantLen  int
		wantKeys []string
	}{
		{name: "no config", wantLen: 1},
		{name: "empty file", existing: "  \n", wantLen: 1},
		{name: "keeps other keys and plugins", existing: `{"requiredPlugins":["acme/tools"],"other":true}`, wantLen: 2, wantKeys: []string{"other"}},
		{name: "already present is a no-op", existing: `{"requiredPlugins":[{"source":"git-subdir","url":"https://github.com/sageox/ox.git","path":"devin-plugin"}]}`, wantLen: 1},
		{name: "invalid json left alone", existing: `{not json`, wantErr: true},
		{name: "requiredPlugins not a list", existing: `{"requiredPlugins":"acme/tools"}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			path := devinConfigPath(root)
			if tt.existing != "" {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(tt.existing), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			err := InstallDevinPlugin(root)
			if tt.wantErr {
				if err == nil {
					t.Fatal("want error")
				}
				if got, _ := os.ReadFile(path); string(got) != tt.existing {
					t.Fatalf("file modified on error: %q", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !HasDevinPlugin(root) {
				t.Fatal("HasDevinPlugin false after install")
			}
			if err := InstallDevinPlugin(root); err != nil {
				t.Fatalf("second install: %v", err)
			}

			var cfg map[string]any
			data, _ := os.ReadFile(path)
			if err := json.Unmarshal(data, &cfg); err != nil {
				t.Fatal(err)
			}
			if got := len(cfg["requiredPlugins"].([]any)); got != tt.wantLen {
				t.Fatalf("requiredPlugins len = %d, want %d", got, tt.wantLen)
			}
			for _, k := range tt.wantKeys {
				if _, ok := cfg[k]; !ok {
					t.Fatalf("key %q dropped", k)
				}
			}
		})
	}
}

func TestDevinDetected(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DEVIN_DIR", "")
	prevAgents := initAgentsFlag
	initAgentsFlag = ""
	t.Cleanup(func() { initAgentsFlag = prevAgents })
	if devinDetected(root) {
		t.Fatal("detected without .devin or DEVIN_DIR")
	}
	t.Setenv("DEVIN_DIR", "/opt/.devin")
	if !devinDetected(root) {
		t.Fatal("not detected with DEVIN_DIR")
	}
	t.Setenv("DEVIN_DIR", "")
	if err := os.Mkdir(filepath.Join(root, ".devin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !devinDetected(root) {
		t.Fatal("not detected with .devin/")
	}
}

func TestHasDevinPlugin(t *testing.T) {
	root := t.TempDir()
	if HasDevinPlugin(root) {
		t.Fatal("no config must not report the plugin")
	}
	path := devinConfigPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{`{not json`, `{"requiredPlugins":["acme/tools"]}`} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if HasDevinPlugin(root) {
			t.Fatalf("%s must not report the plugin", content)
		}
	}

	// an unreadable config (here a directory) is an error, not an empty config
	dirRoot := t.TempDir()
	if err := os.MkdirAll(devinConfigPath(dirRoot), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := readDevinConfig(dirRoot); err == nil {
		t.Fatal("expected error reading a directory as config")
	}
}

func TestInstallAgentHooksDevin(t *testing.T) {
	root := t.TempDir()
	want := devinConfigPath(root)
	devin := map[string]bool{"devin": true}

	installed := installAgentHooks(root, false, devin)
	if !slices.Contains(installed, want) {
		t.Fatalf("first install must report %s, got %v", want, installed)
	}
	if !HasDevinPlugin(root) {
		t.Fatal("plugin not written")
	}

	if again := installAgentHooks(root, false, devin); slices.Contains(again, want) {
		t.Fatalf("re-install must be a no-op, got %v", again)
	}

	broken := t.TempDir()
	if err := os.MkdirAll(filepath.Join(broken, ".devin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(devinConfigPath(broken), []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := installAgentHooks(broken, false, devin); slices.Contains(got, devinConfigPath(broken)) {
		t.Fatalf("invalid config must not be reported as installed, got %v", got)
	}
}

func TestSelectAgentsForInitDetectsDevin(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DEVIN_DIR", "")
	prevAgents := initAgentsFlag
	initAgentsFlag = ""
	t.Cleanup(func() { initAgentsFlag = prevAgents })
	if err := os.MkdirAll(filepath.Join(root, ".devin"), 0o755); err != nil {
		t.Fatal(err)
	}
	selected, err := selectAgentsForInit(root)
	if err != nil {
		t.Fatal(err)
	}
	if !selected["devin"] {
		t.Fatalf("devin not selected: %v", selected)
	}
}
