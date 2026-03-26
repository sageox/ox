package main

import (
	"testing"
)

func TestParseQueryArgs_KEqualsAlias(t *testing.T) {
	t.Parallel()
	// test --k= equals form (existing tests only cover --k separate and --k= invalid)
	qa, err := parseQueryArgs([]string{"--k=8", "search text"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if qa.limit != 8 {
		t.Errorf("limit = %d, want 8", qa.limit)
	}
}

func TestParseQueryArgs_TeamEquals(t *testing.T) {
	t.Parallel()
	qa, err := parseQueryArgs([]string{"--team=team-abc", "query"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if qa.teamID != "team-abc" {
		t.Errorf("teamID = %q, want %q", qa.teamID, "team-abc")
	}
}

func TestParseQueryArgs_RepoEquals(t *testing.T) {
	t.Parallel()
	qa, err := parseQueryArgs([]string{"--repo=repo-xyz", "query"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if qa.repoID != "repo-xyz" {
		t.Errorf("repoID = %q, want %q", qa.repoID, "repo-xyz")
	}
}

func TestParseQueryArgs_EmptyArgs(t *testing.T) {
	t.Parallel()
	_, err := parseQueryArgs([]string{})
	if err == nil {
		t.Error("expected error for empty args")
	}
}
