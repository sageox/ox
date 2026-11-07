package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A resumed Claude session lives in a new <id>.jsonl in the repo's own project
// bucket, opening with a copy of the conversation it continues under that
// conversation's ID. Failure prevented: discovery refused the file, so every
// hook of a resumed or compacted session reported session-file-not-found and
// the recording reached the Ledger empty (#1398).
func TestFindSessionFile_ResumedSessionOpensWithItsParentsTurns(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	repo, _ = filepath.EvalSymlinks(repo)
	const (
		own    = "230457d4-c384-4e38-90b5-3289ff44be62"
		parent = "a1969243-7a0d-4f80-bc97-b71beefcb3e6"
	)
	resumedAt := time.Now().UTC().Truncate(time.Second)
	turn := func(id string, at time.Time) string {
		return fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q,\"timestamp\":%q,\"message\":{\"role\":\"user\",\"content\":\"hi\"}}\n",
			id, repo, at.Format(time.RFC3339Nano))
	}
	inherited := turn(parent, resumedAt.Add(-48*time.Hour)) + turn(parent, resumedAt.Add(-time.Hour))
	bucket := filepath.Join(home, ".claude", "projects", claudeProjectHash(repo))
	if err := os.MkdirAll(bucket, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(bucket, own+".jsonl")
	if err := os.WriteFile(path, []byte(inherited+turn(own, resumedAt.Add(time.Second))), 0o600); err != nil {
		t.Fatal(err)
	}

	got, offset, err := findSessionFile(repo, "", resumedAt.Format(time.RFC3339), own)
	if err != nil || got != path {
		t.Fatalf("resumed session: got %q, %v; want %q", got, err, path)
	}
	if offset != int64(len(inherited)) {
		t.Fatalf("the recording must start after the inherited turns: offset %d, want %d", offset, len(inherited))
	}

	// The same file is still refused once another session's turn follows its own.
	if err := os.WriteFile(path, []byte(inherited+turn(own, resumedAt.Add(time.Second))+turn(parent, resumedAt.Add(2*time.Second))), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _, err := findSessionFile(repo, "", resumedAt.Format(time.RFC3339), own); err == nil {
		t.Fatalf("a file mixing sessions after its own turns was returned: %q", got)
	}
}
