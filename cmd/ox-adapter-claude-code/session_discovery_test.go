package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFindSessionFile_ResumedSessionInOldBucket(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "work", "monorepo")
	if err := os.MkdirAll(filepath.Join(repo, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "77b16b24-5b7d-4598-aacf-4c9afeb4b5ca"
	otherDir := filepath.Join(home, ".claude", "projects", claudeProjectHash(filepath.Dir(repo)))
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(otherDir, id+".jsonl")
	write := func(cwds ...string) {
		t.Helper()
		var data []byte
		for _, cwd := range cwds {
			data = append(data, []byte(fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, cwd))...)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(repo, filepath.Join(repo, "subdir"))
	got, _, err := findSessionFile(repo, "", "", id)
	if err != nil || got != path {
		t.Fatalf("resumed session: got %q, %v; want %q", got, err, path)
	}
	// A transcript that also worked in the parent cannot be assigned wholesale
	// to the child repo, even when its filename matches the exact session ID.
	write(repo, filepath.Dir(repo))
	if got, _, err := findSessionFile(repo, "", "", id); err == nil {
		t.Fatalf("mixed-repo session returned %q", got)
	}
	write(filepath.Join(home, "work", "monorepo-other"))
	if got, _, err := findSessionFile(repo, "", "", id); err == nil {
		t.Fatalf("sibling repo session returned %q", got)
	}
}

func TestFindSessionFile_OwnBucketRejectsForeignAndMissingMetadata(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "repo-a")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	repo, _ = filepath.EvalSymlinks(repo)
	const id = "77b16b24-5b7d-4598-aacf-4c9afeb4b5ca"
	ownDir := filepath.Join(home, ".claude", "projects", claudeProjectHash(repo))
	if err := os.MkdirAll(ownDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ownDir, id+".jsonl")
	for _, records := range []string{
		fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, filepath.Join(home, "repo-b")),
		fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, repo) + fmt.Sprintf("{\"type\":\"assistant\",\"sessionId\":%q,\"cwd\":%q}\n", id, filepath.Join(home, "repo-b")),
		fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q}\n", id),
		fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", "different-id", repo),
	} {
		if err := os.WriteFile(path, []byte(records), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, _, err := findSessionFile(repo, "", "", id); err == nil {
			t.Fatalf("own-bucket direct lookup accepted invalid metadata: %q", got)
		}
		// The timestamp fallback must enforce the same boundary.
		if got, _, err := findSessionFile(repo, "", "", ""); err == nil {
			t.Fatalf("own-bucket time lookup accepted invalid metadata: %q", got)
		}
	}
}

func TestFindSessionFile_TimestampFallbackSkipsForeignNewest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "repo-a")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	repo, _ = filepath.EvalSymlinks(repo)
	ownDir := filepath.Join(home, ".claude", "projects", claudeProjectHash(repo))
	if err := os.MkdirAll(ownDir, 0o755); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(ownDir, "good.jsonl")
	bad := filepath.Join(ownDir, "bad.jsonl")
	if err := os.WriteFile(good, []byte(fmt.Sprintf("{\"type\":\"user\",\"sessionId\":\"good\",\"cwd\":%q}\n", repo)), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(good, past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte(fmt.Sprintf("{\"type\":\"user\",\"sessionId\":\"bad\",\"cwd\":%q}\n", filepath.Join(home, "repo-b"))), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _, err := findSessionFile(repo, "", "", "")
	if err != nil || got != good {
		t.Fatalf("time lookup chose %q, %v; want %q", got, err, good)
	}
}

func TestReadFromOffset_PartialLineIsRetried(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	complete := `{"type":"user","timestamp":"2026-09-24T12:00:00Z","message":{"role":"user","content":"first"}}` + "\n"
	partial := `{"type":"user","timestamp":"2026-09-24T12:01:00Z","message":{"role":"user","content":"second"}`
	if err := os.WriteFile(path, []byte(complete+partial), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, offset, err := readFromOffset(path, 0)
	if err != nil || len(entries) != 1 || offset != int64(len(complete)) {
		t.Fatalf("partial read got %d entries, offset %d, error %v", len(entries), offset, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("}\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	entries, offset, err = readFromOffset(path, offset)
	if err != nil || len(entries) != 1 || entries[0].Content != "second" || offset != int64(len(complete+partial+"}\n")) {
		t.Fatalf("completed read got %+v, offset %d, error %v", entries, offset, err)
	}
}

func TestFindSessionFile_ExactIDDoesNotFallBackToNewest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := "/tmp/test-repo"
	ownDir := filepath.Join(home, ".claude", "projects", claudeProjectHash(repo))
	if err := os.MkdirAll(ownDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ownDir, "other.jsonl"), []byte(`{"type":"user"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _, err := findSessionFile(repo, "", "", "missing"); err == nil {
		t.Fatalf("different session returned %q", got)
	}
}
