package index

import (
	"context"
	"testing"
	"time"
)

// --- upsertRepo / loadKnownCommits / loadExistingRefs ---

func TestUpsertRepo(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	s := openTestStore(t)

	tests := []struct {
		name     string
		repoName string
		path     string
	}{
		{"basic insert", "test-repo", "/path/to/repo"},
		{"insert second", "other-repo", "/path/to/other"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := upsertRepo(context.Background(), s, tt.repoName, tt.path)
			if err != nil {
				t.Fatalf("upsertRepo: %v", err)
			}
			if id <= 0 {
				t.Errorf("expected positive id, got %d", id)
			}
		})
	}

	t.Run("upsert updates path", func(t *testing.T) {
		id1, err := upsertRepo(context.Background(), s, "update-repo", "/old/path")
		if err != nil {
			t.Fatalf("first upsert: %v", err)
		}
		id2, err := upsertRepo(context.Background(), s, "update-repo", "/new/path")
		if err != nil {
			t.Fatalf("second upsert: %v", err)
		}
		if id1 != id2 {
			t.Errorf("upsert should return same id, got %d and %d", id1, id2)
		}
		var path string
		if err := s.QueryRow("SELECT path FROM repos WHERE name = ?", "update-repo").Scan(&path); err != nil {
			t.Fatalf("query: %v", err)
		}
		if path != "/new/path" {
			t.Errorf("expected updated path /new/path, got %q", path)
		}
	})
}

func TestLoadKnownCommits(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	s := openTestStore(t)

	repoID, err := upsertRepo(context.Background(), s, "commits-repo", "/repo")
	if err != nil {
		t.Fatalf("upsertRepo: %v", err)
	}

	t.Run("empty repo has no known commits", func(t *testing.T) {
		known, err := loadKnownCommits(context.Background(), s, repoID)
		if err != nil {
			t.Fatalf("loadKnownCommits: %v", err)
		}
		if len(known) != 0 {
			t.Errorf("expected 0 known commits, got %d", len(known))
		}
	})

	t.Run("inserted commits are returned", func(t *testing.T) {
		hashes := []string{"aaa111", "bbb222", "ccc333"}
		for _, h := range hashes {
			_, err := s.Exec(
				"INSERT INTO commits (repo_id, hash, author, message, timestamp) VALUES (?, ?, ?, ?, ?)",
				repoID, h, "test", "msg", time.Now().Unix(),
			)
			if err != nil {
				t.Fatalf("insert commit: %v", err)
			}
		}
		known, err := loadKnownCommits(context.Background(), s, repoID)
		if err != nil {
			t.Fatalf("loadKnownCommits: %v", err)
		}
		if len(known) != 3 {
			t.Errorf("expected 3 known commits, got %d", len(known))
		}
		for _, h := range hashes {
			if !known[h] {
				t.Errorf("expected %q in known commits", h)
			}
		}
	})
}

func TestLoadExistingRefs(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	s := openTestStore(t)

	repoID, err := upsertRepo(context.Background(), s, "refs-repo", "/repo")
	if err != nil {
		t.Fatalf("upsertRepo: %v", err)
	}

	t.Run("no refs initially", func(t *testing.T) {
		refs, err := loadExistingRefs(context.Background(), s, repoID)
		if err != nil {
			t.Fatalf("loadExistingRefs: %v", err)
		}
		if len(refs) != 0 {
			t.Errorf("expected 0 refs, got %d", len(refs))
		}
	})

	t.Run("returns ref name to commit hash mapping", func(t *testing.T) {
		// insert a commit first
		_, err := s.Exec(
			"INSERT INTO commits (repo_id, hash, author, message, timestamp) VALUES (?, ?, ?, ?, ?)",
			repoID, "deadbeef", "test", "msg", time.Now().Unix(),
		)
		if err != nil {
			t.Fatalf("insert commit: %v", err)
		}
		var commitID int64
		if err := s.QueryRow("SELECT id FROM commits WHERE hash = ?", "deadbeef").Scan(&commitID); err != nil {
			t.Fatalf("get commit id: %v", err)
		}

		_, err = s.Exec(
			"INSERT INTO refs (repo_id, name, commit_id) VALUES (?, ?, ?)",
			repoID, "refs/heads/main", commitID,
		)
		if err != nil {
			t.Fatalf("insert ref: %v", err)
		}

		refs, err := loadExistingRefs(context.Background(), s, repoID)
		if err != nil {
			t.Fatalf("loadExistingRefs: %v", err)
		}
		if len(refs) != 1 {
			t.Fatalf("expected 1 ref, got %d", len(refs))
		}
		if refs["refs/heads/main"] != "deadbeef" {
			t.Errorf("expected refs/heads/main -> deadbeef, got %q", refs["refs/heads/main"])
		}
	})
}
