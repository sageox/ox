package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFindPiSession_CurrentLayoutAndHeaderOwnership(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "repo-a")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	projectDir := piProjectDirs(filepath.Join(home, ".pi", "agent", "sessions"), repo)[0]
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "01a0d3eb-fd67-721d-8288-0641a6cc84a9"
	path := filepath.Join(projectDir, "2026-09-24T14-57-33-543Z_"+id+".jsonl")
	write := func(name, id, cwd string) {
		t.Helper()
		header := fmt.Sprintf("{\"type\":\"session\",\"version\":3,\"id\":%q,\"cwd\":%q}\n", id, cwd)
		if err := os.WriteFile(filepath.Join(projectDir, name), []byte(header), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Base(path), id, repo)
	got, err := findPiSession(repo, "", "", id)
	if err != nil || got != path {
		t.Fatalf("current layout: got %q, %v; want %q", got, err, path)
	}

	// Directory names can collide (slashes vs dashes); the header owns identity.
	write("2026-09-24T15-00-00-000Z_"+id+".jsonl", id, filepath.Join(home, "repo-b"))
	got, err = findPiSession(repo, "", "", id)
	if err != nil || got != path {
		t.Fatalf("foreign newer header: got %q, %v; want %q", got, err, path)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got, err := findPiSession(repo, "", "", id); err == nil {
		t.Fatalf("foreign-only candidate returned %q", got)
	}
}

func TestFindPiSession_DoesNotGuessAnotherAgentInSameRepo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "repo-a")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	projectDir := piProjectDirs(filepath.Join(home, ".pi", "agent", "sessions"), repo)[0]
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "01a0d3eb-fd67-721d-8288-0641a6cc84a9"
	path := filepath.Join(projectDir, "2026-09-24T14-57-33-543Z_"+id+".jsonl")
	data := fmt.Sprintf("{\"type\":\"session\",\"id\":%q,\"cwd\":%q}\n", id, repo)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := findPiSession(repo, "OxOther", "", ""); err == nil {
		t.Fatalf("guessed another session %q", got)
	}
	if got, err := findPiSession(repo, "OxOther", "", id); err != nil || got != path {
		t.Fatalf("exact native ID got %q, %v; want %q", got, err, path)
	}
}

func TestFindPiSession_MtimeFilterOnlyWithoutExactID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "repo-a")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	projectDir := piProjectDirs(filepath.Join(home, ".pi", "agent", "sessions"), repo)[0]
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "01a0d3eb-fd67-721d-8288-0641a6cc84a9"
	path := filepath.Join(projectDir, "2026-09-24T14-57-33-543Z_"+id+".jsonl")
	header := fmt.Sprintf("{\"type\":\"session\",\"id\":%q,\"cwd\":%q}\n", id, repo)
	if err := os.WriteFile(path, []byte(header), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	since := time.Now().Add(-time.Minute).Format(time.RFC3339)
	if got, err := findPiSession(repo, "", since, ""); err == nil {
		t.Fatalf("time-based lookup returned stale session %q", got)
	}
	if got, err := findPiSession(repo, "", since, id); err != nil || got != path {
		t.Fatalf("exact ID lookup got %q, %v; want %q", got, err, path)
	}
}

func TestFindPiSession_RefusesMissingOrWrongHeader(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := "/repo-a"
	projectDir := piProjectDirs(filepath.Join(home, ".pi", "agent", "sessions"), repo)[0]
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "22222222-2222-2222-2222-222222222222"
	for _, header := range []string{
		`{"type":"session","id":"` + id + `","cwd":"/repo-b"}`,
		`{"type":"session","id":"different","cwd":"/repo-a"}`,
		`{"type":"message","message":{"role":"user"}}`,
	} {
		if err := os.WriteFile(filepath.Join(projectDir, id+".jsonl"), []byte(header+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := findPiSession(repo, "", "", id); err == nil {
			t.Fatalf("header %s returned %q", header, got)
		}
	}
}
