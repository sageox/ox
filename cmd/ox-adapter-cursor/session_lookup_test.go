package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/session/cursorpaths"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

const lookupConversationA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
const lookupConversationB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"

func cursorLookupFixture(t *testing.T) (home, repo string) {
	t.Helper()
	base := t.TempDir()
	home = filepath.Join(base, "home")
	repo = filepath.Join(base, "repo")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	return home, repo
}

func createLookupTranscript(t *testing.T, home, repo, id string) string {
	t.Helper()
	path, err := cursorpaths.SessionPath(home, repo, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHandleFindSessionUsesExactConversationInSameProject(t *testing.T) {
	home, repo := cursorLookupFixture(t)
	pathA := createLookupTranscript(t, home, repo, lookupConversationA)
	createLookupTranscript(t, home, repo, lookupConversationB)

	got, err := handleFindSession(adapterprotocol.FindSessionParams{RepoRoot: repo, AgentSessionID: lookupConversationA})
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionFile != pathA || got.Offset != 0 {
		t.Fatalf("FindSession = %#v, want exact chat %q at offset zero", got, pathA)
	}
}

func TestHandleFindSessionDoesNotCrossWorktreesOrFallBack(t *testing.T) {
	home, repo := cursorLookupFixture(t)
	worktree := filepath.Join(filepath.Dir(repo), "worktree")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	createLookupTranscript(t, home, repo, lookupConversationA)
	worktreePath := createLookupTranscript(t, home, worktree, lookupConversationB)

	got, err := handleFindSession(adapterprotocol.FindSessionParams{RepoRoot: worktree, AgentSessionID: lookupConversationB})
	if err != nil || got.SessionFile != worktreePath {
		t.Fatalf("worktree lookup = %#v, %v; want %q", got, err, worktreePath)
	}
	_, err = handleFindSession(adapterprotocol.FindSessionParams{RepoRoot: worktree, AgentSessionID: lookupConversationA})
	if err == nil || !strings.Contains(err.Error(), "session not found") {
		t.Fatalf("wrong-worktree conversation fell back instead of failing: %v", err)
	}
}

func TestHandleFindSessionRequiresExactIdentityAndExistingSource(t *testing.T) {
	_, repo := cursorLookupFixture(t)
	for _, p := range []adapterprotocol.FindSessionParams{
		{RepoRoot: ""},
		{RepoRoot: repo},
		{RepoRoot: repo, AgentSessionID: "conversation-1"},
		{RepoRoot: repo, AgentSessionID: lookupConversationA},
	} {
		if _, err := handleFindSession(p); err == nil {
			t.Fatalf("FindSession(%#v) unexpectedly succeeded", p)
		}
	}
}
