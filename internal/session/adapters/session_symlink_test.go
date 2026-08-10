package adapters

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// IsSessionFileAllowed matches lexically, but the tail loop opens the path
// with os.Open, which follows symlinks. A symlink planted inside an allowed
// root passes the lexical check and then gets read and uploaded through the
// session pipeline — the exact exfiltration the allow-list exists to prevent,
// walked straight through its front door.

// piSessionsRoot builds a fake home with pi's session root and returns both.
func piSessionsRoot(t *testing.T) (home, sessions string) {
	t.Helper()
	home = t.TempDir()
	sessions = filepath.Join(home, ".pi", "agent", "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	return home, sessions
}

func TestSafeSessionFilePath_RejectsSymlinkOutOfRoot(t *testing.T) {
	home, sessions := piSessionsRoot(t)

	secret := filepath.Join(home, ".ssh", "id_rsa")
	if err := os.MkdirAll(filepath.Dir(secret), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}

	// looks like an ordinary transcript, sitting in an allowed directory
	planted := filepath.Join(sessions, "notes.jsonl")
	if err := os.Symlink(secret, planted); err != nil {
		t.Skipf("cannot create symlink on this platform: %v", err)
	}

	// the lexical check is happy — that is the whole problem
	if !IsSessionFileAllowed("pi", planted, home) {
		t.Fatal("precondition failed: the lexical check should accept the planted path")
	}

	got, err := SafeSessionFilePath("pi", planted, home)
	if !errors.Is(err, ErrSessionFileEscapes) {
		t.Errorf("a symlink to %s was accepted for watching (resolved=%q, err=%v) — the daemon would read it and upload the contents",
			secret, got, err)
	}
}

func TestSafeSessionFilePath_RejectsSymlinkedDirectoryOutOfRoot(t *testing.T) {
	home, sessions := piSessionsRoot(t)

	outside := filepath.Join(home, "elsewhere")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "x.jsonl"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	// the escape hatch is a directory this time, not the file itself
	if err := os.Symlink(outside, filepath.Join(sessions, "project")); err != nil {
		t.Skipf("cannot create symlink on this platform: %v", err)
	}

	planted := filepath.Join(sessions, "project", "x.jsonl")
	if _, err := SafeSessionFilePath("pi", planted, home); !errors.Is(err, ErrSessionFileEscapes) {
		t.Errorf("a path through a symlinked directory escaped the root, err=%v", err)
	}
}

func TestSafeSessionFilePath_AcceptsARealTranscript(t *testing.T) {
	home, sessions := piSessionsRoot(t)

	real := filepath.Join(sessions, "project", "2026-08-09.jsonl")
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := SafeSessionFilePath("pi", real, home)
	if err != nil {
		t.Fatalf("an ordinary transcript inside the root was rejected: %v", err)
	}
	// EvalSymlinks canonicalizes /var -> /private/var on macOS, so compare
	// against the resolved form rather than the literal input
	want, _ := filepath.EvalSymlinks(real)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestSafeSessionFilePath_AcceptsASymlinkedHome keeps the fix from breaking
// managed machines, where $HOME itself is commonly a symlink. Resolving the
// file but not the root would call every one of those an escape.
func TestSafeSessionFilePath_AcceptsASymlinkedHome(t *testing.T) {
	base := t.TempDir()
	realHome := filepath.Join(base, "real-home")
	sessions := filepath.Join(realHome, ".pi", "agent", "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(sessions, "s.jsonl")
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	linkedHome := filepath.Join(base, "home")
	if err := os.Symlink(realHome, linkedHome); err != nil {
		t.Skipf("cannot create symlink on this platform: %v", err)
	}

	viaLink := filepath.Join(linkedHome, ".pi", "agent", "sessions", "s.jsonl")
	if _, err := SafeSessionFilePath("pi", viaLink, linkedHome); err != nil {
		t.Errorf("a transcript reached through a symlinked home was rejected: %v", err)
	}
}

func TestSafeSessionFilePath_AllowsAMissingFileInARealDirectory(t *testing.T) {
	home, sessions := piSessionsRoot(t)

	// a fresh recording names the transcript before the agent creates it;
	// rejecting that would break every first-run session
	dir := filepath.Join(sessions, "project")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	notYet := filepath.Join(dir, "brand-new.jsonl")

	got, err := SafeSessionFilePath("pi", notYet, home)
	if err != nil {
		t.Fatalf("a not-yet-created transcript was rejected: %v", err)
	}
	resolvedDir, _ := filepath.EvalSymlinks(dir)
	if want := filepath.Join(resolvedDir, "brand-new.jsonl"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestSafeSessionFilePath_RejectsADanglingSymlink is the bypass that hid behind
// "the file does not exist yet".
//
// EvalSymlinks reports IsNotExist for a dangling symlink exactly as it does for
// a missing file. Treating both as pending handed back the unresolved link, so
// a peer could plant a link to a file that does not exist, get it accepted, then
// create the target and have the daemon read through it. No race required.
func TestSafeSessionFilePath_RejectsADanglingSymlink(t *testing.T) {
	home, sessions := piSessionsRoot(t)

	target := filepath.Join(home, ".aws", "credentials") // deliberately not created
	planted := filepath.Join(sessions, "notes.jsonl")
	if err := os.Symlink(target, planted); err != nil {
		t.Skipf("cannot create symlink on this platform: %v", err)
	}

	got, err := SafeSessionFilePath("pi", planted, home)
	if !errors.Is(err, ErrSessionFileEscapes) {
		t.Fatalf("a dangling symlink was accepted (got=%q err=%v) — creating %s afterwards would make the daemon read it",
			got, err, target)
	}

	// and it must still be refused once the target appears
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SafeSessionFilePath("pi", planted, home); !errors.Is(err, ErrSessionFileEscapes) {
		t.Errorf("after the target was created the link was accepted: %v", err)
	}
}

// TestSafeSessionFilePath_RejectsAPendingFileInAnEscapingDirectory covers the
// same escape one level up: the file does not exist, but its parent is a
// symlink pointing out of the root.
func TestSafeSessionFilePath_RejectsAPendingFileInAnEscapingDirectory(t *testing.T) {
	home, sessions := piSessionsRoot(t)

	outside := filepath.Join(home, "elsewhere")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(sessions, "project")); err != nil {
		t.Skipf("cannot create symlink on this platform: %v", err)
	}

	pending := filepath.Join(sessions, "project", "not-yet.jsonl")
	if _, err := SafeSessionFilePath("pi", pending, home); !errors.Is(err, ErrSessionFileEscapes) {
		t.Errorf("a pending file inside a symlinked-out directory was accepted: %v", err)
	}
}

func TestSafeSessionFilePath_PassesOpaqueHandlesThrough(t *testing.T) {
	home := t.TempDir()
	for _, handle := range []struct{ adapter, file string }{
		{"opencode", "opencode:ses_096fdea05ffeagb7OGYpJCte6a"},
		{"goose", "goose:20260602_3"},
	} {
		got, err := SafeSessionFilePath(handle.adapter, handle.file, home)
		if err != nil {
			t.Errorf("%s: opaque handle rejected: %v", handle.adapter, err)
		}
		if got != handle.file {
			t.Errorf("%s: handle was rewritten to %q", handle.adapter, got)
		}
	}
}

func TestSafeSessionFilePath_StillRejectsPlainlyDeniedPaths(t *testing.T) {
	home, _ := piSessionsRoot(t)
	for _, tc := range []struct{ name, adapter, file string }{
		{"outside every root", "pi", filepath.Join(home, ".ssh", "id_rsa")},
		{"relative", "pi", ".pi/agent/sessions/x.jsonl"},
		{"unknown adapter", "nope", filepath.Join(home, ".pi", "agent", "sessions", "x.jsonl")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := SafeSessionFilePath(tc.adapter, tc.file, home); !errors.Is(err, ErrSessionFileNotAllowed) {
				t.Errorf("expected a not-allowed rejection, got %v", err)
			}
		})
	}
}
