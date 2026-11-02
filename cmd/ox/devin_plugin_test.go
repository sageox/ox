package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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
