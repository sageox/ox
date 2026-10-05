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

// TestMergeOneWithLLM_RejectsOutputWithOrphanedTail covers a model that strips
// the opening marker but leaves the "=======" and ">>>>>>>" lines.
// Failure prevented: the half-merged output is written and staged as resolved.
func TestMergeOneWithLLM_RejectsOutputWithOrphanedTail(t *testing.T) {
	t.Parallel()
	repo := initTestRepo(t, t.TempDir())
	path := "a.txt"
	writeFile(t, repo, path, "<<<<<<< HEAD\nours\n=======\ntheirs\n>>>>>>> br\n")

	r := New(Options{LLMBinary: "fake"})
	r.runLLM = func(ctx context.Context, binary, prompt string) (string, error) {
		return "ours\n=======\ntheirs\n>>>>>>> br\n", nil
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

// TestMergeOneWithLLM_ContentPreservation covers the post-condition beyond
// "no markers left": every line from both conflict sides must survive.
// Failure prevented: a model keeps one side (or paraphrases both) and the
// lossy result is staged as a resolved merge of the team's memory.
func TestMergeOneWithLLM_ContentPreservation(t *testing.T) {
	t.Parallel()
	const conflicted = "# Memory\n<<<<<<< HEAD\n- Devon chose Postgres\n=======\n- Avery chose SQLite\n>>>>>>> br\ntail\n"
	cases := []struct {
		name    string
		in      string
		out     string
		wantErr bool
	}{
		{"union keeps both", conflicted, "# Memory\n- Devon chose Postgres\n- Avery chose SQLite\ntail\n", false},
		{"reorder keeps both", conflicted, "# Memory\n- Avery chose SQLite\n- Devon chose Postgres\ntail\n", false},
		{"drops theirs", conflicted, "# Memory\n- Devon chose Postgres\ntail\n", true},
		{"drops ours", conflicted, "# Memory\n- Avery chose SQLite\ntail\n", true},
		{"paraphrases both", conflicted, "# Memory\n- Team weighed Postgres vs SQLite\ntail\n", true},
		{
			"json union may add a comma and reindent",
			"{\n<<<<<<< HEAD\n  \"a\": 1\n=======\n  \"b\": 2\n>>>>>>> br\n}\n",
			"{\n    \"a\": 1,\n    \"b\": 2\n}\n",
			false,
		},
		{
			"diff3 base may be discarded",
			"<<<<<<< HEAD\nours\n||||||| base\nancestor\n=======\ntheirs\n>>>>>>> br\n",
			"ours\ntheirs\n",
			false,
		},
		{
			"same line rewritten on both sides is refused",
			"<<<<<<< HEAD\nversion: 2\n=======\nversion: 3\n>>>>>>> br\n",
			"version: 3\n",
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := initTestRepo(t, t.TempDir())
			path := "MEMORY.md"
			writeFile(t, repo, path, tc.in)

			r := New(Options{LLMBinary: "fake"})
			r.runLLM = func(ctx context.Context, binary, prompt string) (string, error) {
				return tc.out, nil
			}
			err := r.mergeOneWithLLM(context.Background(), repo, path)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "dropped") {
					t.Fatalf("expected dropped-content rejection, got: %v", err)
				}
				if got := readFile(t, repo, path); got != tc.in {
					t.Errorf("rejected merge must leave the file untouched, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("mergeOneWithLLM: %v", err)
			}
			if got := readFile(t, repo, path); got != tc.out {
				t.Errorf("file content = %q, want %q", got, tc.out)
			}
		})
	}
}
