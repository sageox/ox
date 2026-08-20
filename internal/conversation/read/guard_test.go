package read

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJoinDiscussion(t *testing.T) {
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
		{name: "overlong", folder: strings.Repeat("a", maxFolderNameLen+1), wantErr: true},
		{name: "dotdot inside name ok", folder: "a..b"},
		{name: "leading dot ok", folder: ".hidden"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := joinDiscussion(root, tt.folder)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("joinDiscussion(%q) = %q, want error", tt.folder, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("joinDiscussion(%q) failed: %v", tt.folder, err)
			}
			assertConfined(t, root, got)
		})
	}
}

// TestJoinDiscussionRejectsSymlinkedEntry verifies the guard refuses a
// discussions/<folder> entry that is itself a symlink. Failure prevented: a
// symlink committed into the customer-writable, git-synced team context
// passes every lexical name check and reads files outside the discussions
// root (read-escape / exfiltration).
func TestJoinDiscussionRejectsSymlinkedEntry(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "discussions")
	if err := os.MkdirAll(filepath.Join(root, "2026-08-18-01-00-real"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "transcript.vtt"), []byte("WEBVTT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "2026-08-18-01-00-evil")); err != nil {
		t.Skipf("cannot create symlinks on this platform: %v", err)
	}

	if _, gerr := joinDiscussion(root, "2026-08-18-01-00-real"); gerr != nil {
		t.Fatalf("real directory rejected: %v", gerr)
	}
	if got, gerr := joinDiscussion(root, "2026-08-18-01-00-evil"); gerr == nil {
		t.Fatalf("symlinked entry accepted: %q", got)
	}
	// A not-yet-existing folder still passes — the caller's read reports the
	// typed absence.
	if _, gerr := joinDiscussion(root, "2026-08-18-01-00-missing"); gerr != nil {
		t.Fatalf("missing folder rejected: %v", gerr)
	}
}

// FuzzJoinDiscussion is the hostile-input fuzz required by the plan: no
// folder name — traversal, absolute, backslash, overlong, anything — may
// yield a path outside the discussions root.
func FuzzJoinDiscussion(f *testing.F) {
	seeds := []string{
		"2026-08-11-22-32-full",
		"", ".", "..", "../escape", "../../..", "a/../../b",
		"/etc/passwd", `\\server\share`, `..\..\windows`, `C:\temp`,
		"a\x00b", "a/b/c", "./x", "...", "..%2f", "%2e%2e/",
		strings.Repeat("a", 300), strings.Repeat("../", 100),
	}
	for _, s := range seeds {
		f.Add(s)
	}
	root := filepath.Join(f.TempDir(), "discussions")
	f.Fuzz(func(t *testing.T, folder string) {
		got, err := joinDiscussion(root, folder)
		if err != nil {
			return
		}
		assertConfined(t, root, got)
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
