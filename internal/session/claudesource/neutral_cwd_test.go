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

// TestValidateSharedNotesFolderVisit: a session that lives in this repo's
// worktree and steps into the Claude desktop app's shared notes folder (issue
// #2041) still belongs to this repo. Every other folder outside the repo still
// makes it foreign, whether or not it looks like a project.
func TestValidateSharedNotesFolderVisit(t *testing.T) {
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
	notesRoot := func(outer string) string {
		return filepath.Join(outer, "Library", "Application Support", "Claude", "side-session-notes")
	}
	for name, tt := range map[string]struct {
		visit    func(t *testing.T, outer, repo string) string
		homeless bool // the session never stood in the repo
		foreign  bool
		gone     bool // the refusal says the folder no longer exists
	}{
		"shared notes folder": {visit: func(t *testing.T, outer, _ string) string {
			return mkdir(t, filepath.Join(notesRoot(outer), "local_1"))
		}},
		"folder inside a shared notes folder": {visit: func(t *testing.T, outer, _ string) string {
			return mkdir(t, filepath.Join(notesRoot(outer), "local_1", "drafts"))
		}},
		// the app removes a session's notes; where they stood is still known
		"shared notes folder since deleted": {visit: func(t *testing.T, outer, _ string) string {
			return filepath.Join(notesRoot(outer), "local_1")
		}},
		"shared notes folder alone is not a home": {homeless: true, foreign: true, visit: func(t *testing.T, outer, _ string) string {
			return mkdir(t, filepath.Join(notesRoot(outer), "local_1"))
		}},
		"the folder that holds every session's notes": {foreign: true, visit: func(t *testing.T, outer, _ string) string {
			return mkdir(t, notesRoot(outer))
		}},
		"repository inside a shared notes folder": {foreign: true, visit: func(t *testing.T, outer, _ string) string {
			clone := mkdir(t, filepath.Join(notesRoot(outer), "local_1", "clone"))
			gitRun(t, clone, "init", "-q")
			return mkdir(t, filepath.Join(clone, "pkg"))
		}},
		"link in a shared notes folder to another repository": {foreign: true, visit: func(t *testing.T, outer, _ string) string {
			other := mkdir(t, filepath.Join(outer, "other"))
			gitRun(t, other, "init", "-q")
			link := filepath.Join(mkdir(t, filepath.Join(notesRoot(outer), "local_1")), "other")
			if err := os.Symlink(other, link); err != nil {
				t.Skip("symlinks unavailable:", err)
			}
			return link
		}},
		// no project markers, but nothing says its owner shares it with this repo
		"plain folder": {foreign: true, visit: func(t *testing.T, outer, _ string) string {
			dir := mkdir(t, filepath.Join(outer, "Documents", "private-client"))
			if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			return dir
		}},
		"folder named like the notes folder elsewhere": {foreign: true, visit: func(t *testing.T, outer, _ string) string {
			return mkdir(t, filepath.Join(outer, "Claude", "side-session-notes", "local_1"))
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
		"deleted folder": {foreign: true, gone: true, visit: func(t *testing.T, outer, _ string) string {
			return filepath.Join(outer, "gone")
		}},
	} {
		t.Run(name, func(t *testing.T) {
			outer, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			useSharedNotesRoot(t, notesRoot(outer))
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
						t.Fatalf("Validate(root=%s): a visit to the shared notes folder must not reject the session: %v", root, err)
					}
					continue
				}
				if !errors.Is(err, ErrUntrustedSource) {
					t.Fatalf("Validate(root=%s): must be refused as foreign, got %v", root, err)
				}
				if !strings.Contains(err.Error(), fmt.Sprintf("%q", visit)) {
					t.Fatalf("Validate(root=%s): the error must name the cwd %q: %v", root, visit, err)
				}
				if gone := strings.Contains(err.Error(), GoneCwdNote); gone != tt.gone {
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

// An archived workspace no longer names its git dir, yet a recording made in it
// that also visited the shared notes folder is still its own (daemon recovery,
// #2041).
func TestValidateSharedNotesFolderVisitFromArchivedWorkspace(t *testing.T) {
	const id = "77b16b24-5b7d-4598-aacf-4c9afeb4b5ca"
	outer, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(outer, "archived")
	useSharedNotesRoot(t, filepath.Join(outer, "side-session-notes"))
	notes := filepath.Join(outer, "side-session-notes", "local_1")
	if err := os.MkdirAll(notes, 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, workspace) +
		fmt.Sprintf("{\"type\":\"assistant\",\"sessionId\":%q,\"cwd\":%q}\n", id, notes)
	path := filepath.Join(t.TempDir(), id+".jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Validate(path, workspace, id); err != nil {
		t.Fatalf("a shared notes folder visit from an archived workspace must not reject it: %v", err)
	}
}

// useSharedNotesRoot points the notes lookup at root for one test, so no test
// depends on, or writes into, the real app's data folder.
func useSharedNotesRoot(t *testing.T, root string) {
	t.Helper()
	prev := sharedNotesRoot
	sharedNotesRoot = func() (string, error) { return root, nil }
	t.Cleanup(func() { sharedNotesRoot = prev })
}

// The real lookup names the notes folder inside the Claude desktop app's data
// folder, which sits in the OS's per-user config directory.
func TestSharedNotesRootIsInsideTheAppDataFolder(t *testing.T) {
	config, err := os.UserConfigDir()
	if err != nil {
		t.Skip("no user config dir:", err)
	}
	got, err := sharedNotesRoot()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(config, "Claude", "side-session-notes"); got != want {
		t.Fatalf("sharedNotesRoot() = %q, want %q", got, want)
	}
}

// The stop guidance reads the refusal as text, so a folder whose name contains
// the "no longer exists" note must not read as a folder that is gone.
func TestForeignCwdIsGoneSkipsTheFolderName(t *testing.T) {
	refusal := func(cwd, note string) string {
		return fmt.Sprintf("adapter error: %v: %s: %s%s", ErrUntrustedSource, ForeignCwdReason, describeCwd(cwd, json.RawMessage(`"2026-11-25T10:00:00Z"`)), note)
	}
	for name, tt := range map[string]struct {
		message string
		want    bool
	}{
		"folder that is gone":             {refusal("/work/gone", ", "+GoneCwdNote), true},
		"folder that exists":              {refusal("/work/other", ""), false},
		"existing folder named like it":   {refusal("/work/"+GoneCwdNote, ""), false},
		"gone folder named like it":       {refusal("/work/"+GoneCwdNote, ", "+GoneCwdNote), true},
		"a refusal for another reason":    {"claude source has invalid or missing repository metadata", false},
		"folder with a quote in its name": {refusal(`/work/a", `+GoneCwdNote, ""), false},
	} {
		if got := ForeignCwdIsGone(tt.message); got != tt.want {
			t.Errorf("%s: ForeignCwdIsGone(%q) = %v, want %v", name, tt.message, got, tt.want)
		}
	}
}
