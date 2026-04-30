package automerge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestBuildPrompt_IncludesBothSidesVerbatim(t *testing.T) {
	t.Parallel()

	conflicted := []byte("line1\n<<<<<<< HEAD\nours-only\n=======\ntheirs-only\n>>>>>>> branch\nline2\n")
	prompt := buildPrompt("config.json", conflicted)

	for _, want := range []string{
		"ours-only",
		"theirs-only",
		"<<<<<<<",
		"=======",
		">>>>>>>",
		"config.json",
		"BEGIN_FILE",
		"END_FILE",
		"Preserve user intent",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q\n--- prompt ---\n%s", want, prompt)
		}
	}
}

func TestBuildPrompt_AppendsNewlineWhenMissing(t *testing.T) {
	t.Parallel()
	prompt := buildPrompt("a.txt", []byte("no-trailing-newline"))
	if !strings.Contains(prompt, "no-trailing-newline\nEND_FILE") {
		t.Errorf("expected newline before END_FILE, got: %s", prompt)
	}
}

func TestStripFences(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		// non-fenced input is returned untouched — preserves leading
		// indentation and trailing blank lines for whitespace-sensitive
		// file types (Python, Makefiles, YAML, etc.).
		{"no fences", "hello world", "hello world"},
		{"already clean trailing newline", "hello\n", "hello\n"},
		{"preserves leading indent", "    indented\n", "    indented\n"},
		// fences only stripped when paired (leading + trailing). a stray
		// trailing-only fence is ambiguous (could be content) and is
		// preserved verbatim.
		{"leading fence with lang", "```json\n{\"a\":1}\n```\n", "{\"a\":1}\n"},
		{"leading fence no lang", "```\nhello\n```", "hello\n"},
		{"trailing only is preserved", "hello\n```", "hello\n```"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripFences(tc.in)
			if got != tc.want {
				t.Errorf("stripFences(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMergeOneWithLLM_RejectsOversizedFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo := initTestRepo(t, dir)
	path := "huge.txt"
	writeFile(t, repo, path, strings.Repeat("x", 200))

	r := New(Options{LLMBinary: "fake", MaxLLMFileBytes: 100})
	r.runLLM = func(ctx context.Context, binary, prompt string) (string, error) {
		t.Fatal("runLLM should not be called for oversized files")
		return "", nil
	}
	err := r.mergeOneWithLLM(context.Background(), repo, path)
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected 'too large' error, got: %v", err)
	}
}

func TestMergeOneWithLLM_RejectsOutputWithMarkers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo := initTestRepo(t, dir)
	path := "a.txt"
	writeFile(t, repo, path, "<<<<<<< HEAD\nours\n=======\ntheirs\n>>>>>>> br\n")

	r := New(Options{LLMBinary: "fake"})
	r.runLLM = func(ctx context.Context, binary, prompt string) (string, error) {
		// model fails to remove markers
		return "<<<<<<< HEAD\nstill broken\n", nil
	}
	err := r.mergeOneWithLLM(context.Background(), repo, path)
	if err == nil || !strings.Contains(err.Error(), "conflict markers") {
		t.Fatalf("expected conflict-marker rejection, got: %v", err)
	}
}

func TestMergeOneWithLLM_WritesAndStages(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo := initTestRepo(t, dir)
	path := "config.txt"
	writeFile(t, repo, path, "<<<<<<< HEAD\nours\n=======\ntheirs\n>>>>>>> br\n")

	var sawPrompt string
	r := New(Options{LLMBinary: "fake"})
	r.runLLM = func(ctx context.Context, binary, prompt string) (string, error) {
		sawPrompt = prompt
		return "ours\ntheirs\n", nil
	}
	if err := r.mergeOneWithLLM(context.Background(), repo, path); err != nil {
		t.Fatalf("mergeOneWithLLM: %v", err)
	}
	if !strings.Contains(sawPrompt, "ours") || !strings.Contains(sawPrompt, "theirs") {
		t.Errorf("prompt missing one side: %s", sawPrompt)
	}
	got := readFile(t, repo, path)
	if got != "ours\ntheirs\n" {
		t.Errorf("file content = %q, want %q", got, "ours\ntheirs\n")
	}
	// verify staged: `git diff --cached --name-only` should list path
	out := mustGit(t, repo, "diff", "--cached", "--name-only")
	if !strings.Contains(out, path) {
		t.Errorf("expected %q to be staged, git output: %s", path, out)
	}
}

func TestTryLLMTier_ReturnsSentinelWhenBinaryMissing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo := initTestRepo(t, dir)

	r := New(Options{LLMBinary: "definitely-not-on-path-12345"})
	err := r.tryLLMTier(context.Background(), repo, []string{"x"})
	if err == nil || !errors.Is(err, ErrLLMUnavailable) {
		t.Fatalf("expected ErrLLMUnavailable, got: %v", err)
	}
}

func TestMergeOneWithLLM_HonorsTimeout(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo := initTestRepo(t, dir)
	path := "slow.txt"
	writeFile(t, repo, path, "<<<<<<< HEAD\nA\n=======\nB\n>>>>>>> br\n")

	r := New(Options{LLMBinary: "fake", LLMTimeout: 20 * time.Millisecond})
	r.runLLM = func(ctx context.Context, binary, prompt string) (string, error) {
		select {
		case <-time.After(2 * time.Second):
			return "should not get here", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	start := time.Now()
	err := r.mergeOneWithLLM(context.Background(), repo, path)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if time.Since(start) > time.Second {
		t.Errorf("timeout not respected: took %s", time.Since(start))
	}
}
