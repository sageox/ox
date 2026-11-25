package claudesource

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateNeutralFolderVisit: a session that lives in this repo's worktree
// and steps into a folder that is no project (the Claude desktop app's shared
// notes folder, issue #2041) still belongs to this repo. A folder that holds or
// may hold another project's work still makes it foreign.
func TestValidateNeutralFolderVisit(t *testing.T) {
	const id = "77b16b24-5b7d-4598-aacf-4c9afeb4b5ca"
	gitRun := func(t *testing.T, dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	mkdir := func(t *testing.T, dir string) string {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	for name, tt := range map[string]struct {
		visit    func(t *testing.T, outer, repo string) string
		homeless bool // the session never stood in the repo
		foreign  bool
	}{
		"plain notes folder": {visit: func(t *testing.T, outer, _ string) string {
			return mkdir(t, filepath.Join(outer, "Library", "Application Support", "Claude", "side-session-notes", "local_1"))
		}},
		"plain folder holding files": {visit: func(t *testing.T, outer, _ string) string {
			dir := mkdir(t, filepath.Join(outer, "notes"))
			mkdir(t, filepath.Join(dir, "drafts"))
			if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			return dir
		}},
		"plain folder alone is not a home": {homeless: true, foreign: true, visit: func(t *testing.T, outer, _ string) string {
			return mkdir(t, filepath.Join(outer, "notes"))
		}},
		"another repository": {foreign: true, visit: func(t *testing.T, outer, _ string) string {
			other := mkdir(t, filepath.Join(outer, "other"))
			gitRun(t, other, "init", "-q")
			return mkdir(t, filepath.Join(other, "pkg"))
		}},
		"another Ox project": {foreign: true, visit: func(t *testing.T, outer, _ string) string {
			dir := mkdir(t, filepath.Join(outer, "proj", ".sageox"))
			if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
			return filepath.Dir(dir)
		}},
		"folder above the repo": {foreign: true, visit: func(t *testing.T, outer, _ string) string {
			return outer
		}},
		"folder of other projects": {foreign: true, visit: func(t *testing.T, outer, _ string) string {
			src := mkdir(t, filepath.Join(outer, "src"))
			gitRun(t, mkdir(t, filepath.Join(src, "other")), "init", "-q")
			return src
		}},
		"folder linking to another project": {foreign: true, visit: func(t *testing.T, outer, _ string) string {
			other := mkdir(t, filepath.Join(outer, "other"))
			gitRun(t, other, "init", "-q")
			links := mkdir(t, filepath.Join(outer, "links"))
			if err := os.Symlink(other, filepath.Join(links, "other")); err != nil {
				t.Skip("symlinks unavailable:", err)
			}
			return links
		}},
		"deleted folder": {foreign: true, visit: func(t *testing.T, outer, _ string) string {
			return filepath.Join(outer, "gone")
		}},
		"folder linking into another project": {foreign: true, visit: func(t *testing.T, outer, _ string) string {
			other := mkdir(t, filepath.Join(outer, "other"))
			gitRun(t, other, "init", "-q")
			links := mkdir(t, filepath.Join(outer, "links"))
			if err := os.Symlink(mkdir(t, filepath.Join(other, "src")), filepath.Join(links, "src")); err != nil {
				t.Skip("symlinks unavailable:", err)
			}
			return links
		}},
		"folder holding a linked worktree": {foreign: true, visit: func(t *testing.T, outer, repo string) string {
			trees := mkdir(t, filepath.Join(outer, "trees"))
			gitRun(t, repo, "worktree", "add", "-q", filepath.Join(trees, "nested", "wt2"))
			return trees
		}},
	} {
		t.Run(name, func(t *testing.T) {
			outer, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			repo := mkdir(t, filepath.Join(outer, "repo"))
			gitRun(t, repo, "init", "-q")
			gitRun(t, repo, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init")
			wt := filepath.Join(repo, ".claude", "worktrees", "wt")
			gitRun(t, repo, "worktree", "add", "-q", wt)
			visit := tt.visit(t, outer, repo)

			var body strings.Builder
			if !tt.homeless {
				fmt.Fprintf(&body, "{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, wt)
			}
			fmt.Fprintf(&body, "{\"type\":\"assistant\",\"sessionId\":%q,\"cwd\":%q,\"timestamp\":\"2026-11-25T10:00:00Z\"}\n", id, visit)
			path := filepath.Join(t.TempDir(), id+".jsonl")
			if err := os.WriteFile(path, []byte(body.String()), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, root := range []string{repo, wt} {
				err := Validate(path, root, id)
				if !tt.foreign {
					if err != nil {
						t.Fatalf("Validate(root=%s): a visit to a folder that is no project must not reject the session: %v", root, err)
					}
					continue
				}
				if !errors.Is(err, ErrUntrustedSource) {
					t.Fatalf("Validate(root=%s): must be refused as foreign, got %v", root, err)
				}
				if !strings.Contains(err.Error(), fmt.Sprintf("%q", visit)) {
					t.Fatalf("Validate(root=%s): the error must name the cwd %q: %v", root, visit, err)
				}
				if gone := strings.Contains(err.Error(), GoneCwdNote); gone != (name == "deleted folder") {
					t.Fatalf("Validate(root=%s): only a deleted folder is named as gone: %v", root, err)
				}
			}
		})
	}
}

// A record's timestamp reaches a terminal in the error, so only a well-formed
// one is shown, and one that is not a string does not hide the record's cwd.
func TestDescribeCwdShowsOnlyAValidTimestamp(t *testing.T) {
	for raw, want := range map[string]string{
		`"2026-11-25T10:00:00Z"`:          `"/w" (first at 2026-11-25T10:00:00Z)`,
		`"2026-11-25T10:00:00Z\u001b[2J"`: `"/w"`,
		`1795634424`:                      `"/w"`,
		``:                                `"/w"`,
	} {
		if got := describeCwd("/w", json.RawMessage(raw)); got != want {
			t.Errorf("describeCwd(%s) = %s, want %s", raw, got, want)
		}
	}

	const id = "77b16b24-5b7d-4598-aacf-4c9afeb4b5ca"
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	other := t.TempDir()
	if err := os.Mkdir(filepath.Join(other, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, repo) +
		fmt.Sprintf("{\"type\":\"assistant\",\"sessionId\":%q,\"cwd\":%q,\"timestamp\":1795634424}\n", id, other)
	path := filepath.Join(t.TempDir(), id+".jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Validate(path, repo, id); !errors.Is(err, ErrUntrustedSource) {
		t.Fatalf("a numeric timestamp must not hide a foreign cwd: %v", err)
	}
}
