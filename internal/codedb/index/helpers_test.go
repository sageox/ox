package index

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTimeToNullInt64(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git operations")
	}

	tests := []struct {
		name      string
		input     time.Time
		wantValid bool
		wantUnix  int64
	}{
		{"zero time returns invalid", time.Time{}, false, 0},
		{"epoch returns 0", time.Unix(0, 0), true, 0},
		{"specific time", time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC), true, 1736942400},
		{"negative unix time", time.Date(1969, 1, 1, 0, 0, 0, 0, time.UTC), true, -31536000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := timeToNullInt64(tt.input)
			if got.Valid != tt.wantValid {
				t.Errorf("valid = %v, want %v", got.Valid, tt.wantValid)
				return
			}
			if got.Valid && got.Int64 != tt.wantUnix {
				t.Errorf("timeToNullInt64 = %d, want %d", got.Int64, tt.wantUnix)
			}
		})
	}
}

func TestTimePtrToNullInt64(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git operations")
	}

	t.Run("nil pointer returns invalid", func(t *testing.T) {
		got := timePtrToNullInt64(nil)
		if got.Valid {
			t.Errorf("expected invalid, got %d", got.Int64)
		}
	})

	t.Run("zero time pointer returns invalid", func(t *testing.T) {
		zero := time.Time{}
		got := timePtrToNullInt64(&zero)
		if got.Valid {
			t.Errorf("expected invalid for zero time, got %d", got.Int64)
		}
	})

	t.Run("valid time pointer", func(t *testing.T) {
		ts := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
		got := timePtrToNullInt64(&ts)
		if !got.Valid {
			t.Fatal("expected valid")
		}
		if got.Int64 != ts.Unix() {
			t.Errorf("got %d, want %d", got.Int64, ts.Unix())
		}
	})
}

func TestToNullString(t *testing.T) {
	t.Parallel()

	got := toNullString("")
	if got.Valid {
		t.Error("empty string should be invalid")
	}

	got = toNullString("hello")
	if !got.Valid || got.String != "hello" {
		t.Errorf("expected valid 'hello', got valid=%v string=%q", got.Valid, got.String)
	}
}

func TestPtrIntToNullInt64(t *testing.T) {
	t.Parallel()

	got := ptrIntToNullInt64(nil)
	if got.Valid {
		t.Error("nil should be invalid")
	}

	n := 42
	got = ptrIntToNullInt64(&n)
	if !got.Valid || got.Int64 != 42 {
		t.Errorf("expected valid 42, got valid=%v int64=%d", got.Valid, got.Int64)
	}
}


func TestDirtyIndexPath_Structure(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git operations")
	}

	codedbDir := "/data/codedb"
	worktreePath := "/home/user/project"

	p := DirtyIndexPath(codedbDir, worktreePath)

	// should be under codedbDir/bleve/dirty/
	dir := filepath.Dir(p)
	expectedDir := filepath.Join(codedbDir, "bleve", "dirty")
	if dir != expectedDir {
		t.Errorf("directory = %q, want %q", dir, expectedDir)
	}

	// hash should be SHA256 of worktree path, first 8 bytes hex-encoded
	h := sha256.Sum256([]byte(worktreePath))
	expectedName := hex.EncodeToString(h[:8])
	gotName := filepath.Base(p)
	if gotName != expectedName {
		t.Errorf("filename = %q, want %q", gotName, expectedName)
	}
}

func TestDirtyIndexPath_DifferentInputs(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git operations")
	}

	// same codedb dir, different worktrees should produce different paths
	p1 := DirtyIndexPath("/data", "/project-a")
	p2 := DirtyIndexPath("/data", "/project-b")
	if p1 == p2 {
		t.Error("different worktrees should produce different paths")
	}

	// different codedb dirs, same worktree should produce different paths
	p3 := DirtyIndexPath("/data1", "/project")
	p4 := DirtyIndexPath("/data2", "/project")
	if p3 == p4 {
		t.Error("different codedb dirs should produce different parent paths")
	}
}

func TestFileUnchanged(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git operations")
	}

	tmp := t.TempDir()
	testFile := filepath.Join(tmp, "test.json")
	if err := os.WriteFile(testFile, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	info, err := os.Stat(testFile)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	mtime := info.ModTime().UTC().UnixNano()

	tests := []struct {
		name        string
		path        string
		knownMtimes map[string]int64
		want        bool
	}{
		{
			"matching mtime returns true",
			testFile,
			map[string]int64{testFile: mtime},
			true,
		},
		{
			"different mtime returns false",
			testFile,
			map[string]int64{testFile: mtime - 1000},
			false,
		},
		{
			"missing from known returns false",
			testFile,
			map[string]int64{},
			false,
		},
		{
			"nonexistent file returns false",
			filepath.Join(tmp, "no-such-file.json"),
			map[string]int64{filepath.Join(tmp, "no-such-file.json"): 12345},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fileUnchanged(tt.path, tt.knownMtimes)
			if got != tt.want {
				t.Errorf("fileUnchanged = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResolveGitDir_InvalidGitFile(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git operations")
	}

	// .git is a file but doesn't contain "gitdir: ..." prefix
	tmp := t.TempDir()
	dotGit := filepath.Join(tmp, ".git")
	if err := os.WriteFile(dotGit, []byte("not a gitdir reference"), 0o644); err != nil {
		t.Fatalf("write .git: %v", err)
	}

	path, isWorktree := resolveGitDir(tmp)
	if isWorktree {
		t.Error("should not detect as worktree with invalid .git file content")
	}
	if path != tmp {
		t.Errorf("path = %q, want %q", path, tmp)
	}
}

func TestResolveGitDir_GitFilePointsToMissing(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git operations")
	}

	tmp := t.TempDir()
	dotGit := filepath.Join(tmp, ".git")
	// valid gitdir prefix but points to nonexistent path
	if err := os.WriteFile(dotGit, []byte("gitdir: /nonexistent/path/.git/worktrees/wt"), 0o644); err != nil {
		t.Fatalf("write .git: %v", err)
	}

	path, isWorktree := resolveGitDir(tmp)
	// should gracefully fall back since commondir file doesn't exist
	if isWorktree {
		t.Error("should not detect as worktree when commondir is missing")
	}
	if path != tmp {
		t.Errorf("path = %q, want %q", path, tmp)
	}
}

func TestDefaultSkipDirs(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git operations")
	}

	expected := []string{".git", "node_modules", ".sageox", "vendor"}
	for _, dir := range expected {
		if !defaultSkipDirs[dir] {
			t.Errorf("expected %q in defaultSkipDirs", dir)
		}
	}

	// should not skip regular directories
	notSkipped := []string{"src", "internal", "cmd", "pkg", "lib"}
	for _, dir := range notSkipped {
		if defaultSkipDirs[dir] {
			t.Errorf("%q should not be in defaultSkipDirs", dir)
		}
	}
}

func TestDefaultBranchFallbacks(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git operations")
	}

	if len(defaultBranchFallbacks) == 0 {
		t.Fatal("defaultBranchFallbacks should not be empty")
	}

	// should include common default branch refs
	expected := map[string]bool{
		"refs/heads/main":            false,
		"refs/heads/master":          false,
		"refs/remotes/origin/main":   false,
		"refs/remotes/origin/master": false,
	}

	for _, fb := range defaultBranchFallbacks {
		expected[fb] = true
	}

	for name, found := range expected {
		if !found {
			t.Errorf("expected %q in defaultBranchFallbacks", name)
		}
	}
}
