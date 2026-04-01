package index

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// --- ParseSymbols / ParseComments ---

func TestParseSymbols_NoSupportedLanguages(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	s := openTestStore(t)

	stats, err := ParseSymbols(context.Background(), s, nil)
	if err != nil {
		t.Fatalf("ParseSymbols: %v", err)
	}
	// symbols stub returns nil for SupportedLanguages, so nothing to parse
	if stats.BlobsParsed != 0 || stats.SymbolsExtracted != 0 {
		t.Errorf("expected zero stats with no supported languages, got %+v", stats)
	}
}

func TestParseComments_NoUnparsedBlobs(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	s := openTestStore(t)

	var messages []string
	progress := func(msg string) { messages = append(messages, msg) }

	stats, err := ParseComments(context.Background(), s, progress)
	if err != nil {
		t.Fatalf("ParseComments: %v", err)
	}
	if stats.BlobsParsed != 0 {
		t.Errorf("expected 0 blobs parsed, got %d", stats.BlobsParsed)
	}
}

func TestParseComments_WithIndexedRepo(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	dir, _ := initGitRepo(t, 3)
	s := openTestStore(t)

	// index the repo first so blobs exist in the store
	if err := IndexLocalRepo(context.Background(), s, dir, IndexOptions{}); err != nil {
		t.Fatalf("IndexLocalRepo: %v", err)
	}

	// verify blobs exist before parsing
	var blobCount int
	if err := s.QueryRow("SELECT COUNT(*) FROM blobs").Scan(&blobCount); err != nil {
		t.Fatalf("query blobs: %v", err)
	}
	if blobCount == 0 {
		t.Skip("no blobs indexed, nothing to parse")
	}

	stats, err := ParseComments(context.Background(), s, nil)
	if err != nil {
		t.Fatalf("ParseComments: %v", err)
	}
	// initGitRepo creates .go files which have comment support
	// the result depends on whether comments exist in the Go files
	// but the function should at least not error out
	t.Logf("ParseComments: %d blobs parsed, %d comments extracted", stats.BlobsParsed, stats.CommentsExtracted)
}

func TestParseComments_Idempotent(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	dir, _ := initGitRepo(t, 2)
	s := openTestStore(t)

	if err := IndexLocalRepo(context.Background(), s, dir, IndexOptions{}); err != nil {
		t.Fatalf("IndexLocalRepo: %v", err)
	}

	// parse once
	stats1, err := ParseComments(context.Background(), s, nil)
	if err != nil {
		t.Fatalf("first ParseComments: %v", err)
	}

	// parse again — should find no unparsed blobs
	stats2, err := ParseComments(context.Background(), s, nil)
	if err != nil {
		t.Fatalf("second ParseComments: %v", err)
	}
	if stats2.BlobsParsed != 0 {
		t.Errorf("second parse should find 0 unparsed blobs, got %d (first parsed %d)",
			stats2.BlobsParsed, stats1.BlobsParsed)
	}
}

// --- resolveDefaultBranch ---

func TestResolveDefaultBranch_WithHead(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	dir, tipHash := initGitRepo(t, 2)

	repo, err := plainOpenTolerant(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	ref, err := resolveDefaultBranch(repo)
	if err != nil {
		t.Fatalf("resolveDefaultBranch: %v", err)
	}
	if ref.name != "refs/heads/main" {
		t.Errorf("expected refs/heads/main, got %q", ref.name)
	}
	if ref.tipOID.String() != tipHash {
		t.Errorf("expected tip %s, got %s", tipHash, ref.tipOID.String())
	}
}

func TestResolveDefaultBranch_FallbackToBranchNames(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}

	// create a bare repo (HEAD may not resolve in the same way)
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), // safe: git subprocess needs parent env for PATH
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@sageox.ai",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@sageox.ai",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-m", "init")

	// clone as bare repo
	bareDir := filepath.Join(t.TempDir(), "bare.git")
	run2 := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	run2("clone", "--bare", dir, bareDir)

	repo, err := plainOpenTolerant(bareDir)
	if err != nil {
		t.Fatalf("open bare: %v", err)
	}

	ref, err := resolveDefaultBranch(repo)
	if err != nil {
		t.Fatalf("resolveDefaultBranch on bare repo: %v", err)
	}
	// should resolve HEAD or fallback to refs/heads/main
	if ref.name != "refs/heads/main" {
		t.Errorf("expected refs/heads/main, got %q", ref.name)
	}
}
