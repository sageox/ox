//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

func TestPreservePluginOwnerExplainsForeignOwner(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can assign any owner")
	}
	foreign, err := os.Stat("/")
	if err != nil {
		t.Fatal(err)
	}
	tmp, err := os.Create(filepath.Join(t.TempDir(), "plugin.ts"))
	if err != nil {
		t.Fatal(err)
	}
	defer tmp.Close()
	err = preservePluginOwner(tmp, foreign)
	if err == nil || !strings.Contains(err.Error(), "rerun as that user") {
		t.Fatalf("expected an ownership remediation error, got %v", err)
	}
}

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
}

func TestPreservePluginOwnerReportsStatFailure(t *testing.T) {
	tmp, err := os.Create(filepath.Join(t.TempDir(), "plugin.ts"))
	if err != nil {
		t.Fatal(err)
	}
	existing, err := tmp.Stat()
	if err != nil {
		t.Fatal(err)
	}
	_ = tmp.Close()
	if err := preservePluginOwner(tmp, existing); err == nil {
		t.Fatal("expected an error for a closed temporary file")
	}
}

func TestRepairKeepsPluginGroup(t *testing.T) {
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	gid := -1
	for _, g := range groups {
		if g != os.Getegid() {
			gid = g
			break
		}
	}
	if gid < 0 {
		t.Skip("needs a supplementary group")
	}
	params := adapterprotocol.HookParams{RepoRoot: t.TempDir(), Scope: "project"}
	path := resolvePluginPath(params.RepoRoot, params.Scope)
	writePluginAt(t, path, legacyPluginFixture, 0644)
	if err := os.Chown(path, -1, gid); err != nil {
		t.Skipf("cannot assign supplementary group: %v", err)
	}
	if _, err := handleInstallHooks(params); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := int(info.Sys().(*syscall.Stat_t).Gid); got != gid {
		t.Fatalf("repaired plugin gid = %d, want %d", got, gid)
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != openCodePluginTemplate {
		t.Fatalf("plugin not repaired: %v", err)
	}
}

func TestUnreadablePluginIsReported(t *testing.T) {
	skipIfRoot(t)
	params := adapterprotocol.HookParams{RepoRoot: t.TempDir(), Scope: "project"}
	writePluginAt(t, resolvePluginPath(params.RepoRoot, params.Scope), legacyPluginFixture, 0)
	if _, err := handleCheckHooks(params); err == nil {
		t.Error("check hid an unreadable plugin")
	}
	if _, err := handleInstallHooks(params); err == nil {
		t.Error("install ignored an unreadable plugin")
	}
}

func TestInstallHooksReportsUnwritableLocations(t *testing.T) {
	skipIfRoot(t)
	tests := []struct {
		name   string
		setup  func(t *testing.T, root, path string) string
		legacy bool
	}{
		{"read-only project root", func(t *testing.T, root, _ string) string { return root }, false},
		{"read-only plugin directory", func(t *testing.T, _, path string) string {
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			return filepath.Dir(path)
		}, false},
		{"read-only directory around legacy plugin", func(t *testing.T, _, path string) string {
			writePluginAt(t, path, legacyPluginFixture, 0644)
			return filepath.Dir(path)
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := adapterprotocol.HookParams{RepoRoot: t.TempDir(), Scope: "project"}
			path := resolvePluginPath(params.RepoRoot, params.Scope)
			locked := tt.setup(t, params.RepoRoot, path)
			if err := os.Chmod(locked, 0555); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(locked, 0755) })
			if _, err := handleInstallHooks(params); err == nil {
				t.Fatal("install reported success without writing the plugin")
			}
			if tt.legacy {
				if content, err := os.ReadFile(path); err != nil || string(content) != legacyPluginFixture {
					t.Fatalf("legacy plugin changed after failed repair: %v", err)
				}
			}
		})
	}
}
