package read

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateFolderName(t *testing.T) {
	root := filepath.Join(t.TempDir(), "discussions")
	tests := []struct {
		name    string
		folder  string
		wantErr bool
	}{
		{name: "plain folder", folder: "2026-08-11-22-32-full"},
		{name: "empty", folder: "", wantErr: true},
		{name: "dot", folder: ".", wantErr: true},
		{name: "dotdot", folder: "..", wantErr: true},
		{name: "traversal", folder: "../escape", wantErr: true},
		{name: "nested traversal", folder: "a/../../escape", wantErr: true},
		{name: "subpath", folder: "a/b", wantErr: true},
		{name: "absolute", folder: "/etc/passwd", wantErr: true},
		{name: "backslash", folder: `..\escape`, wantErr: true},
		{name: "backslash subpath", folder: `a\b`, wantErr: true},
		{name: "windows drive", folder: `C:\temp`, wantErr: true},
		{name: "colon volume", folder: "C:evil", wantErr: true},
		{name: "NUL byte", folder: "a\x00b", wantErr: true},
		{name: "trailing separator", folder: "folder/", wantErr: true},
		{name: "overlong", folder: strings.Repeat("a", maxFolderNameLen+1), wantErr: true},
		{name: "dotdot inside name ok", folder: "a..b"},
		{name: "leading dot ok", folder: ".hidden"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFolderName(tt.folder)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("validateFolderName(%q) accepted a hostile name", tt.folder)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateFolderName(%q) failed: %v", tt.folder, err)
			}
			assertConfined(t, root, filepath.Join(root, tt.folder))
		})
	}
}

// TestOpenDiscussionSkipsSymlinkedEntry verifies the open guard refuses a
// discussions/<folder> entry that is itself a symlink. Failure prevented: a
// symlink committed into the customer-writable, git-synced team context
// passes every lexical name check and reads files outside the discussions
// root (read-escape / exfiltration).
func TestOpenDiscussionSkipsSymlinkedEntry(t *testing.T) {
	base := t.TempDir()
	rootDir := filepath.Join(base, "discussions")
	if err := os.MkdirAll(filepath.Join(rootDir, "2026-08-18-01-00-real"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "transcript.vtt"), []byte("WEBVTT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(rootDir, "2026-08-18-01-00-evil")); err != nil {
		t.Skipf("cannot create symlinks on this platform: %v", err)
	}

	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	droot, gerr := openDiscussion(root, "2026-08-18-01-00-real")
	if gerr != nil || droot == nil {
		t.Fatalf("real directory rejected: %v", gerr)
	}
	droot.Close()

	if droot, gerr := openDiscussion(root, "2026-08-18-01-00-evil"); gerr != nil || droot != nil {
		if droot != nil {
			droot.Close()
		}
		t.Fatalf("symlinked entry not skipped: droot=%v err=%v", droot, gerr)
	}
	// A not-yet-existing folder is skipped too — the caller reports the typed
	// absence.
	if droot, gerr := openDiscussion(root, "2026-08-18-01-00-missing"); gerr != nil || droot != nil {
		t.Fatalf("missing folder: droot=%v err=%v, want nil, nil", droot, gerr)
	}
}

// TestOpenDiscussionRefusesFolderSwappedAfterValidation is the controlled
// TOCTOU replacement for the indexed lookup path: the folder is validated as
// a real directory against the held discussions root (exactly what loadRows
// does), then swapped for a symlink escaping the root, then opened.
// Failure prevented: re-opening the folder by absolute path after validation
// (os.OpenRoot follows symlinks while resolving its initial path argument)
// would follow the swapped-in link and serve arbitrary files outside the
// discussions root as discussion content (read-escape / exfiltration).
func TestOpenDiscussionRefusesFolderSwappedAfterValidation(t *testing.T) {
	base := t.TempDir()
	rootDir := filepath.Join(base, "discussions")
	const folder = "2026-08-18-02-00-swap"
	folderAbs := filepath.Join(rootDir, folder)
	if err := os.MkdirAll(folderAbs, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "transcript.vtt"), []byte("secret outside content"), 0o644); err != nil {
		t.Fatal(err)
	}

	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	// Validation instant: the entry is a live, plain directory — the same
	// no-follow check loadRows performs before serving the row.
	if info, statErr := root.Lstat(folder); statErr != nil || !info.IsDir() {
		t.Fatalf("pre-swap validation failed: %v", statErr)
	}

	// The controlled replacement: between validation and open, the folder
	// becomes a symlink pointing outside the discussions root.
	if err := os.Remove(folderAbs); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, folderAbs); err != nil {
		t.Skipf("cannot create symlinks on this platform: %v", err)
	}

	droot, gerr := openDiscussion(root, folder)
	if droot != nil {
		data, readErr := droot.ReadFile("transcript.vtt")
		droot.Close()
		t.Fatalf("swapped folder was opened (err=%v); read = %q, %v — outside content must never be reachable", gerr, data, readErr)
	}
	if gerr != nil && gerr.Code != ErrCodeReadError {
		t.Fatalf("swap error code = %s, want %s", gerr.Code, ErrCodeReadError)
	}
}

// FuzzFolderName is the hostile-input fuzz required by the plan: no folder
// name — traversal, absolute, backslash, overlong, anything — accepted by the
// lexical guard may resolve outside the discussions root or span more than a
// single path element.
func FuzzFolderName(f *testing.F) {
	seeds := []string{
		"2026-08-11-22-32-full",
		"", ".", "..", "../escape", "../../..", "a/../../b",
		"/etc/passwd", `\\server\share`, `..\..\windows`, `C:\temp`,
		"a\x00b", "a/b/c", "./x", "...", "..%2f", "%2e%2e/",
		"folder/", "link/.",
		strings.Repeat("a", 300), strings.Repeat("../", 100),
	}
	for _, s := range seeds {
		f.Add(s)
	}
	root := filepath.Join(f.TempDir(), "discussions")
	f.Fuzz(func(t *testing.T, folder string) {
		if err := validateFolderName(folder); err != nil {
			return
		}
		assertConfined(t, root, filepath.Join(root, folder))
	})
}

func assertConfined(t *testing.T, root, got string) {
	t.Helper()
	rel, relErr := filepath.Rel(root, got)
	if relErr != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("path %q escapes root %q (rel %q)", got, root, rel)
	}
	if strings.Contains(rel, string(filepath.Separator)) {
		t.Fatalf("path %q is not a single element under root (rel %q)", got, rel)
	}
}
