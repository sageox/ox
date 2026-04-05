package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// --- A. List behavior ---

// TestAdapterList_NoAdapters verifies list returns cleanly when no adapters exist.
// Failure prevented: panic or error when adapter directories are empty.
func TestAdapterList_NoAdapters(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OX_ADAPTER_PATH", dir)

	cmd := adapterListCmd
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("list with no adapters should not error: %v", err)
	}
}

// TestAdapterList_WithDiscoveredAdapters verifies list finds adapters from OX_ADAPTER_PATH.
// Failure prevented: discovery integration broken in CLI layer.
func TestAdapterList_WithDiscoveredAdapters(t *testing.T) {
	dir := t.TempDir()
	createFakeAdapter(t, dir, "test-list", "0.2.0", "session")

	t.Setenv("OX_ADAPTER_PATH", dir)

	cmd := adapterListCmd
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("list should succeed: %v", err)
	}
}

// --- B. Info behavior ---

// TestAdapterInfo_KnownAdapter verifies info shows details for a discovered adapter.
// Failure prevented: info command fails to find adapter that list shows.
func TestAdapterInfo_KnownAdapter(t *testing.T) {
	dir := t.TempDir()
	createFakeAdapter(t, dir, "test-info", "1.0.0", "session")

	t.Setenv("OX_ADAPTER_PATH", dir)

	cmd := adapterInfoCmd
	if err := cmd.RunE(cmd, []string{"test-info"}); err != nil {
		t.Fatalf("info for known adapter should succeed: %v", err)
	}
}

// TestAdapterInfo_UnknownAdapter verifies info returns an error for a missing adapter.
// Failure prevented: silent success when querying nonexistent adapter.
func TestAdapterInfo_UnknownAdapter(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OX_ADAPTER_PATH", dir)

	cmd := adapterInfoCmd
	err := cmd.RunE(cmd, []string{"nonexistent"})
	if err == nil {
		t.Fatal("info for unknown adapter should return an error")
	}
}

// --- C. Remove guards ---

// TestAdapterRemove_RefusesBundled verifies remove rejects bundled adapters.
// Failure prevented: user accidentally deletes adapter shipped with ox.
func TestAdapterRemove_RefusesBundled(t *testing.T) {
	// create an adapter in the same directory as the test binary (simulates bundled)
	exe, err := os.Executable()
	if err != nil {
		t.Skip("cannot determine test executable path")
	}
	exeDir := filepath.Dir(exe)

	script := fakeAdapterScript("bundled-test", "1.0.0", "session")
	binaryPath := filepath.Join(exeDir, "ox-adapter-bundled-test")
	if err := os.WriteFile(binaryPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(binaryPath) })

	t.Setenv("OX_ADAPTER_PATH", exeDir)

	cmd := adapterRemoveCmd
	err = cmd.RunE(cmd, []string{"bundled-test"})
	if err == nil {
		t.Fatal("remove should refuse bundled adapters")
	}
	if !strings.Contains(err.Error(), "bundled") {
		t.Errorf("error should mention 'bundled', got: %s", err.Error())
	}
}

// TestAdapterRemove_UnknownAdapter verifies remove returns error for nonexistent adapter.
// Failure prevented: silent success when removing adapter that doesn't exist.
func TestAdapterRemove_UnknownAdapter(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OX_ADAPTER_PATH", dir)

	cmd := adapterRemoveCmd
	err := cmd.RunE(cmd, []string{"nonexistent"})
	if err == nil {
		t.Fatal("remove should fail for unknown adapter")
	}
}

// --- D. Link validation ---

// TestAdapterLink_ValidatesBinaryBeforeLinking verifies link checks the binary runs info.
// Failure prevented: symlink created to invalid/non-adapter binary.
func TestAdapterLink_ValidatesBinaryBeforeLinking(t *testing.T) {
	dir := t.TempDir()

	badBinary := filepath.Join(dir, "ox-adapter-bad")
	if err := os.WriteFile(badBinary, []byte("#!/bin/sh\necho 'not json'"), 0755); err != nil {
		t.Fatal(err)
	}

	cmd := adapterLinkCmd
	err := cmd.RunE(cmd, []string{badBinary})
	if err == nil {
		t.Fatal("link should fail for invalid binary")
	}
}

// TestAdapterLink_RequiresPrefix verifies link rejects binaries without ox-adapter- prefix.
// Failure prevented: non-adapter binary gets linked and confuses discovery.
func TestAdapterLink_RequiresPrefix(t *testing.T) {
	dir := t.TempDir()
	createFakeAdapter(t, dir, "myagent", "1.0.0", "session")

	// rename to remove prefix
	oldPath := filepath.Join(dir, "ox-adapter-myagent")
	newPath := filepath.Join(dir, "myagent")
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatal(err)
	}

	cmd := adapterLinkCmd
	err := cmd.RunE(cmd, []string{newPath})
	if err == nil {
		t.Fatal("link should fail for binary without ox-adapter- prefix")
	}
}

// --- E. Unlink behavior ---

// TestAdapterUnlink_NonexistentAdapter verifies unlink returns error for missing adapter.
// Failure prevented: silent success when unlinking adapter that doesn't exist.
func TestAdapterUnlink_NonexistentAdapter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior differs on Windows")
	}

	cmd := adapterUnlinkCmd
	err := cmd.RunE(cmd, []string{"nonexistent"})
	if err == nil {
		t.Fatal("unlink should fail for nonexistent adapter")
	}
}

// --- F. GitHub URL parsing ---

// TestParseGitHubRepo verifies owner/repo extraction from various URL formats.
// Failure prevented: install fails to parse valid GitHub URLs.
func TestParseGitHubRepo(t *testing.T) {
	tests := []struct {
		input     string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{input: "github.com/sageox/ox-adapters", wantOwner: "sageox", wantRepo: "ox-adapters"},
		{input: "https://github.com/sageox/ox-adapters", wantOwner: "sageox", wantRepo: "ox-adapters"},
		{input: "https://github.com/sageox/ox-adapters/", wantOwner: "sageox", wantRepo: "ox-adapters"},
		{input: "http://github.com/sageox/ox-adapters", wantOwner: "sageox", wantRepo: "ox-adapters"},
		{input: "gitlab.com/foo/bar", wantErr: true},
		{input: "github.com/", wantErr: true},
		{input: "github.com/owner-only", wantErr: true},
		{input: "not-a-url", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			owner, repo, err := parseGitHubRepo(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error for %q", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if owner != tt.wantOwner {
				t.Errorf("owner = %q, want %q", owner, tt.wantOwner)
			}
			if repo != tt.wantRepo {
				t.Errorf("repo = %q, want %q", repo, tt.wantRepo)
			}
		})
	}
}

// --- G. Binary name derivation ---

// TestDeriveAdapterBinaryName verifies platform suffix stripping from asset names.
// Failure prevented: downloaded binary gets wrong filename in adapters dir.
func TestDeriveAdapterBinaryName(t *testing.T) {
	tests := []struct {
		asset    string
		platform string
		want     string
	}{
		{asset: "ox-adapter-claude-code_darwin_arm64", platform: "darwin_arm64", want: "ox-adapter-claude-code"},
		{asset: "ox-adapter-claude-code_linux_amd64", platform: "linux_amd64", want: "ox-adapter-claude-code"},
		{asset: "ox-adapter-foo_darwin_amd64", platform: "darwin_amd64", want: "ox-adapter-foo"},
	}

	for _, tt := range tests {
		t.Run(tt.asset, func(t *testing.T) {
			got := deriveAdapterBinaryName(tt.asset, tt.platform)
			// on non-Windows, no .exe suffix
			if runtime.GOOS != "windows" && got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// --- H. Verify adapter binary ---

// TestVerifyAdapterBinary_InvalidBinary verifies verification rejects bad binaries.
// Failure prevented: broken adapter gets installed without validation catching it.
func TestVerifyAdapterBinary_InvalidBinary(t *testing.T) {
	dir := t.TempDir()
	badBinary := filepath.Join(dir, "ox-adapter-bad")
	if err := os.WriteFile(badBinary, []byte("#!/bin/sh\necho 'not json'"), 0755); err != nil {
		t.Fatal(err)
	}

	err := verifyAdapterBinary(badBinary)
	if err == nil {
		t.Fatal("should reject binary with invalid info response")
	}
}

// TestVerifyAdapterBinary_ValidBinary verifies verification accepts good binaries.
// Failure prevented: valid adapter rejected during install.
func TestVerifyAdapterBinary_ValidBinary(t *testing.T) {
	dir := t.TempDir()
	createFakeAdapter(t, dir, "valid", "1.0.0", "session")

	err := verifyAdapterBinary(filepath.Join(dir, "ox-adapter-valid"))
	if err != nil {
		t.Fatalf("should accept valid adapter: %v", err)
	}
}

// TestVerifyAdapterBinary_OldProtocol verifies verification rejects old protocol versions.
// Failure prevented: adapter with incompatible protocol gets installed.
func TestVerifyAdapterBinary_OldProtocol(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
echo '{"protocol_version":0,"name":"old","display_name":"Old","version":"1.0.0","type":"session","capabilities":["session_reader"]}'`
	binaryPath := filepath.Join(dir, "ox-adapter-old")
	if err := os.WriteFile(binaryPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	err := verifyAdapterBinary(binaryPath)
	if err == nil {
		t.Fatal("should reject adapter with old protocol version")
	}
	if !strings.Contains(err.Error(), "protocol version") {
		t.Errorf("error should mention protocol version, got: %s", err.Error())
	}
}

// --- Helpers ---

// fakeAdapterScript returns a shell script that mimics a valid adapter info response.
func fakeAdapterScript(name, version, adapterType string) string {
	return fmt.Sprintf(`#!/bin/sh
echo '{"protocol_version":1,"name":"%s","display_name":"%s","version":"%s","type":"%s","capabilities":["session_reader"]}'`,
		name, name, version, adapterType)
}

// createFakeAdapter writes a fake adapter binary script to the given directory.
// Skips on Windows since these are shell scripts.
func createFakeAdapter(t *testing.T, dir, name, version, adapterType string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script adapters require unix")
	}
	script := fakeAdapterScript(name, version, adapterType)
	binaryPath := filepath.Join(dir, "ox-adapter-"+name)
	if err := os.WriteFile(binaryPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return binaryPath
}

// fakeAdapterWithHooksScript returns a shell script that handles info, install-hooks,
// check-hooks, and uninstall-hooks subcommands. configDir is the dot-directory name
// (e.g., ".codex") where the fake install-hooks writes a hooks.json file.
func fakeAdapterWithHooksScript(name, version, adapterType, configDir string) string {
	return fmt.Sprintf(`#!/bin/sh
case "$1" in
  info)
    echo '{"protocol_version":1,"name":"%s","display_name":"%s","version":"%s","type":"%s","capabilities":["session_reader","hook_installer"]}'
    ;;
  install-hooks)
    repo_root=""
    shift
    while [ $# -gt 0 ]; do
      case "$1" in
        --repo-root) repo_root="$2"; shift 2 ;;
        --scope) shift 2 ;;
        *) shift ;;
      esac
    done
    if [ -n "$repo_root" ]; then
      mkdir -p "$repo_root/%s"
      echo '{"hooks":{}}' > "$repo_root/%s/hooks.json"
    fi
    echo '{"installed":true,"files_written":["hooks.json"],"hooks":["PostToolUse","Stop"]}'
    ;;
  check-hooks)
    repo_root=""
    shift
    while [ $# -gt 0 ]; do
      case "$1" in
        --repo-root) repo_root="$2"; shift 2 ;;
        --scope) shift 2 ;;
        *) shift ;;
      esac
    done
    if [ -n "$repo_root" ] && [ -f "$repo_root/%s/hooks.json" ]; then
      echo '{"installed":true,"scope":"project","hook_files":["hooks.json"]}'
    else
      echo '{"installed":false,"scope":"project","hook_files":[]}'
    fi
    ;;
  uninstall-hooks)
    echo '{"uninstalled":true,"files_modified":[]}'
    ;;
  *)
    echo '{}'
    ;;
esac`, name, name, version, adapterType, configDir, configDir, configDir)
}

// createFakeAdapterWithHooks writes a fake adapter script that supports hook operations.
// Skips on Windows since these are shell scripts.
func createFakeAdapterWithHooks(t *testing.T, dir, name, version, adapterType, configDir string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script adapters require unix")
	}
	script := fakeAdapterWithHooksScript(name, version, adapterType, configDir)
	binaryPath := filepath.Join(dir, "ox-adapter-"+name)
	if err := os.WriteFile(binaryPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return binaryPath
}
