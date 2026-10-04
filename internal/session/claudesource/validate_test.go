package claudesource

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateAppendedForeignCwd(t *testing.T) {
	repo := t.TempDir()
	repo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	const id = "77b16b24-5b7d-4598-aacf-4c9afeb4b5ca"
	path := filepath.Join(t.TempDir(), id+".jsonl")
	first := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, repo)
	if err := os.WriteFile(path, []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Validate(path, repo, id); err != nil {
		t.Fatalf("initial session: %v", err)
	}
	foreign := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, filepath.Dir(repo))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(foreign); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Validate(path, repo, id); err == nil {
		t.Fatal("foreign turn appended after discovery was accepted")
	}
}

func TestValidateIgnoresPartialFinalRecordUntilComplete(t *testing.T) {
	repo := t.TempDir()
	repo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	const id = "77b16b24-5b7d-4598-aacf-4c9afeb4b5ca"
	path := filepath.Join(t.TempDir(), id+".jsonl")
	first := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, repo)
	if err := os.WriteFile(path, []byte(first+`{"type":"user","cwd":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Validate(path, repo, id); err != nil {
		t.Fatalf("partial last line should not hide complete records: %v", err)
	}
	if err := os.WriteFile(path, []byte(first+`not-json`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Validate(path, repo, id); err != nil {
		t.Fatalf("malformed complete record should be skipped by both reader and validator: %v", err)
	}
}

// A directory a session visited is often gone by the time it is checked: build
// output, a scratch folder, an archived workspace. Where it stood is still
// decidable, so such a turn must neither stall the recording nor claim a
// directory that was never inside the repo.
func TestValidateDeletedCwdIsJudgedByWhereItStood(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const id = "77b16b24-5b7d-4598-aacf-4c9afeb4b5ca"
	validate := func(t *testing.T, cwd string) error {
		t.Helper()
		path := filepath.Join(t.TempDir(), id+".jsonl")
		turn := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, cwd)
		if err := os.WriteFile(path, []byte(turn), 0o600); err != nil {
			t.Fatal(err)
		}
		return Validate(path, root+string(os.PathSeparator), id)
	}
	nested := filepath.Join(root, "build", "out")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validate(t, nested); err != nil {
		t.Fatalf("existing ordinary subdirectory should be valid: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(root, "build")); err != nil {
		t.Fatal(err)
	}
	if err := validate(t, nested); err != nil {
		t.Fatalf("a deleted subdirectory of the repo still belongs to it: %v", err)
	}
	for name, cwd := range map[string]string{
		"deleted sibling of the repo":   filepath.Join(filepath.Dir(root), "removed-workspace"),
		"traversal out of the repo":     filepath.Join(root, "gone") + "/../../removed-workspace",
		"deleted nested path via dots":  filepath.Join(root, "gone", "..", "..", "removed-workspace"),
		"relative path cannot be owned": "build/out",
	} {
		if err := validate(t, cwd); !errors.Is(err, ErrUntrustedSource) {
			t.Fatalf("%s must be refused as foreign, got %v", name, err)
		}
	}
	path := filepath.Join(t.TempDir(), id+".jsonl")
	if err := os.WriteFile(path, []byte(fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, nested)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := Validate(path, root+string(os.PathSeparator), id); err == nil || errors.Is(err, ErrUntrustedSource) {
		t.Fatalf("a removed project root cannot be validated, but proves nothing about the turn: %v", err)
	}
}

func TestValidateDanglingSymlinkCwdIsUncheckableNotOwned(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const id = "77b16b24-5b7d-4598-aacf-4c9afeb4b5ca"
	// a link inside the repo to a directory that is gone could have led anywhere
	link := filepath.Join(root, "shortcut")
	if err := os.Symlink(filepath.Join(filepath.Dir(root), "removed-elsewhere"), link); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), id+".jsonl")
	turn := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, filepath.Join(link, "pkg"))
	if err := os.WriteFile(path, []byte(turn), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Validate(path, root, id); err == nil || errors.Is(err, ErrUntrustedSource) {
		t.Fatalf("a dangling link cannot be placed, so it defers instead of claiming the repo: %v", err)
	}
}

func TestValidateReadRejectsReplacedOrRewrittenSource(t *testing.T) {
	repo := t.TempDir()
	const id = "77b16b24-5b7d-4598-aacf-4c9afeb4b5ca"
	path := filepath.Join(t.TempDir(), id+".jsonl")
	turn := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, repo)
	if err := os.WriteFile(path, []byte(turn), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := Snapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := path + ".new"
	if err := os.WriteFile(replacement, []byte(turn), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRead(path, repo, id, 0, true, before); err == nil || errors.Is(err, ErrUntrustedSource) {
		t.Fatalf("replacement must retry without quarantine: %v", err)
	}
	before, err = Snapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, before.ModTime().Add(time.Second), before.ModTime().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRead(path, repo, id, 0, true, before); err == nil {
		t.Fatal("same-size rewrite must retry")
	}
	before, err = Snapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(turn); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRead(path, repo, id, 0, true, before); err != nil {
		t.Fatalf("ordinary append should remain capturable: %v", err)
	}
}

func TestValidateRejectsNestedRepository(t *testing.T) {
	root := t.TempDir()
	const id = "77b16b24-5b7d-4598-aacf-4c9afeb4b5ca"
	path := filepath.Join(t.TempDir(), id+".jsonl")
	nested := filepath.Join(root, "tools", "other")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	turn := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, nested)
	if err := os.WriteFile(path, []byte(turn), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Validate(path, root, id); err != nil {
		t.Fatalf("ordinary subdirectory should belong to repo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, ".git"), []byte("gitdir: ../metadata"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Validate(path, root, id); err == nil {
		t.Fatal("nested Git worktree accepted as parent repo")
	}
	if err := os.Remove(filepath.Join(nested, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(nested, ".sageox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, ".sageox", "config.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Validate(path, root, id); err == nil {
		t.Fatal("nested Ox repository accepted as parent repo")
	}
}

func TestValidateFromSkipsUnreadableLinesButRejectsOwnershipChanges(t *testing.T) {
	repo := t.TempDir()
	const id = "77b16b24-5b7d-4598-aacf-4c9afeb4b5ca"
	path := filepath.Join(t.TempDir(), id+".jsonl")
	first := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, repo)
	if err := os.WriteFile(path, []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	offset := int64(len(first))
	oversize := []byte(`{"type":"user","content":"` + strings.Repeat("a", 10*1024*1024) + `"}` + "\n")
	valid := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, repo)
	if err := os.WriteFile(path, append([]byte(first+`{broken`+"\n"), append(oversize, []byte(valid)...)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateFrom(path, repo, id, offset); err != nil {
		t.Fatalf("skipped lines cannot strand later valid turns: %v", err)
	}
	for _, bad := range []string{
		fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, filepath.Dir(repo)),
		fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", "other-id", repo),
		fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":123}\n", id),
		`{"type":"user","message":{"content":"no identity"}}` + "\n",
	} {
		if err := os.WriteFile(path, []byte(first+bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := ValidateFrom(path, repo, id, offset); err == nil {
			t.Fatalf("accepted invalid ownership metadata in %s", bad)
		}
		if err := Validate(path, repo, id); err == nil {
			t.Fatalf("full validation accepted invalid ownership metadata in %s", bad)
		}
	}
}

func TestValidateAcceptsOwnSubmoduleButNotOtherGitlinks(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const id = "77b16b24-5b7d-4598-aacf-4c9afeb4b5ca"
	path := filepath.Join(t.TempDir(), id+".jsonl")
	if err := os.MkdirAll(filepath.Join(root, ".git", "modules", "tools", "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	submodule := filepath.Join(root, "tools", "lib")
	if err := os.MkdirAll(filepath.Join(submodule, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	turn := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, filepath.Join(submodule, "src"))
	if err := os.WriteFile(path, []byte(turn), 0o600); err != nil {
		t.Fatal(err)
	}

	// git writes a relative gitlink for a submodule
	if err := os.WriteFile(filepath.Join(submodule, ".git"), []byte("gitdir: ../../.git/modules/tools/lib\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Validate(path, root, id); err != nil {
		t.Fatalf("a submodule checked out in the repo belongs to it: %v", err)
	}

	// a linked worktree and a clone elsewhere have a .git file too, but their
	// git dir is not under this repo's modules directory
	for name, gitlink := range map[string]string{
		"linked worktree":          "gitdir: " + filepath.Join(root, ".git", "worktrees", "other") + "\n",
		"another repository":       "gitdir: " + filepath.Join(t.TempDir(), ".git", "modules", "lib") + "\n",
		"the modules dir itself":   "gitdir: ../../.git/modules\n",
		"escaping the modules dir": "gitdir: ../../.git/modules/../worktrees/x\n",
		"not a gitlink":            "something else\n",
	} {
		if err := os.WriteFile(filepath.Join(submodule, ".git"), []byte(gitlink), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := Validate(path, root, id); err == nil {
			t.Fatalf("%s accepted as part of the parent repo", name)
		}
	}
}

func TestValidateRecordedIgnoresTurnsBeforeTheRecordingStarted(t *testing.T) {
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const id = "77b16b24-5b7d-4598-aacf-4c9afeb4b5ca"
	path := filepath.Join(t.TempDir(), id+".jsonl")
	earlier := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, filepath.Dir(repo))
	recorded := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, repo)
	if err := os.WriteFile(path, []byte(earlier+recorded), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Snapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRecorded(path, repo, id, 0, snapshot); !errors.Is(err, ErrUntrustedSource) {
		t.Fatalf("a recording that began at the top of the file owns every turn in it: %v", err)
	}
	if err := ValidateRecorded(path, repo, id, int64(len(earlier)), snapshot); err != nil {
		t.Fatalf("a directory visited before the recording began must not condemn it: %v", err)
	}
	// the recorded range itself is still checked
	later := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, filepath.Dir(repo))
	if err := os.WriteFile(path, []byte(earlier+recorded+later), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err = Snapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRecorded(path, repo, id, int64(len(earlier)), snapshot); !errors.Is(err, ErrUntrustedSource) {
		t.Fatalf("a foreign turn inside the recorded range must still be refused: %v", err)
	}
}
