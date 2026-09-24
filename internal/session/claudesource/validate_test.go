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

func TestValidateDeletedCwdCannotClaimParentRepo(t *testing.T) {
	root := t.TempDir()
	const id = "77b16b24-5b7d-4598-aacf-4c9afeb4b5ca"
	path := filepath.Join(t.TempDir(), id+".jsonl")
	nested := filepath.Join(root, "removed-child")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	turn := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, nested)
	if err := os.WriteFile(path, []byte(turn), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Validate(path, root+string(os.PathSeparator), id); err != nil {
		t.Fatalf("existing ordinary subdirectory should be valid: %v", err)
	}
	if err := os.Remove(nested); err != nil {
		t.Fatal(err)
	}
	if err := Validate(path, root+string(os.PathSeparator), id); err == nil || errors.Is(err, ErrUntrustedSource) {
		t.Fatalf("deleted cwd must defer without permanent quarantine: %v", err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := Validate(path, root+string(os.PathSeparator), id); err == nil {
		t.Fatal("removed project root must not hang or validate")
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
