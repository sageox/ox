package index

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/utils/merkletrie"
)

// --- fixture repos -----------------------------------------------------------
//
// Fixtures are written with `git fast-import` so one process builds a whole
// history, including modes the working tree cannot express portably (symlinks,
// gitlinks, newline in a path).

type tdfFile struct {
	mode    string
	content string // blob body, or the 40-hex commit id for a gitlink
}

func tdfReg(content string) tdfFile  { return tdfFile{"100644", content} }
func tdfExec(content string) tdfFile { return tdfFile{"100755", content} }
func tdfLink(target string) tdfFile  { return tdfFile{"120000", target} }
func tdfGitlink(sha string) tdfFile  { return tdfFile{"160000", sha} }

type tdfCommit struct {
	name    string
	parents []string // first parent first
	files   map[string]tdfFile
}

type tdfFixture struct {
	gitDir string
	repo   *git.Repository
	commit map[string]plumbing.Hash
	tree   map[string]plumbing.Hash
}

// tdfGit runs git in dir with the developer's global config isolated.
func tdfGit(tb testing.TB, dir string, stdin []byte, args ...string) string {
	tb.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		tb.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return string(out)
}

func tdfQuotePath(path string) string {
	if strings.ContainsAny(path, "\n\"\\") {
		return strconv.Quote(path)
	}
	return path
}

func tdfWriteFile(w *bytes.Buffer, path string, f tdfFile) {
	if f.mode == "160000" {
		fmt.Fprintf(w, "M 160000 %s %s\n", f.content, tdfQuotePath(path))
		return
	}
	fmt.Fprintf(w, "M %s inline %s\ndata %d\n%s\n", f.mode, tdfQuotePath(path), len(f.content), f.content)
}

// tdfBuild imports commits (full snapshots, parents before children) into a new
// repo, one branch per commit name, and resolves every commit and tree id.
func tdfBuild(tb testing.TB, commits []tdfCommit) *tdfFixture {
	tb.Helper()
	dir := tb.TempDir()
	tdfGit(tb, dir, nil, "init", "-q")

	var stream bytes.Buffer
	mark := make(map[string]int, len(commits))
	for i, c := range commits {
		mark[c.name] = i + 1
		msg := "commit " + c.name
		fmt.Fprintf(&stream, "commit refs/heads/%s\nmark :%d\ncommitter Test <test@example.com> %d +0000\ndata %d\n%s\n",
			c.name, i+1, 1_700_000_000+i, len(msg), msg)
		for j, parent := range c.parents {
			pm, ok := mark[parent]
			if !ok {
				tb.Fatalf("commit %s: unknown parent %s", c.name, parent)
			}
			if j == 0 {
				fmt.Fprintf(&stream, "from :%d\n", pm)
			} else {
				fmt.Fprintf(&stream, "merge :%d\n", pm)
			}
		}
		stream.WriteString("deleteall\n")
		paths := make([]string, 0, len(c.files))
		for p := range c.files {
			paths = append(paths, p)
		}
		slices.Sort(paths)
		for _, p := range paths {
			tdfWriteFile(&stream, p, c.files[p])
		}
		stream.WriteString("\n")
	}
	tdfGit(tb, dir, stream.Bytes(), "fast-import", "--quiet")

	fx := &tdfFixture{
		gitDir: filepath.Join(dir, ".git"),
		commit: make(map[string]plumbing.Hash, len(commits)),
		tree:   make(map[string]plumbing.Hash, len(commits)),
	}
	refs := tdfGit(tb, dir, nil, "for-each-ref", "--format=%(refname:short) %(objectname) %(tree)", "refs/heads")
	for line := range strings.SplitSeq(strings.TrimSpace(refs), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			tb.Fatalf("unexpected for-each-ref line %q", line)
		}
		fx.commit[fields[0]] = plumbing.NewHash(fields[1])
		fx.tree[fields[0]] = plumbing.NewHash(fields[2])
	}
	repo, err := plainOpenTolerant(dir)
	if err != nil {
		tb.Fatalf("open fixture repo: %v", err)
	}
	fx.repo = repo
	return fx
}

// --- reference differ and comparison -----------------------------------------

// tdfReferenceDiff is today's go-git path plus indexCommit's file filter. It is
// the oracle the git differ must match.
func tdfReferenceDiff(tb testing.TB, repo *git.Repository, parentTree, childTree plumbing.Hash) []treeChange {
	tb.Helper()
	out, err := tdfReferenceDiffErr(repo, parentTree, childTree)
	if err != nil {
		tb.Fatalf("reference diff: %v", err)
	}
	return out
}

// tdfTreeObject loads a tree, tolerating the empty tree being absent from the
// object store (git never has to write it).
func tdfTreeObject(repo *git.Repository, hash plumbing.Hash) (*object.Tree, error) {
	tree, err := repo.TreeObject(hash)
	if errors.Is(err, plumbing.ErrObjectNotFound) && hash == plumbing.NewHash(gitEmptyTreeSHA1) {
		return &object.Tree{}, nil
	}
	return tree, err
}

func tdfReferenceDiffErr(repo *git.Repository, parentTree, childTree plumbing.Hash) ([]treeChange, error) {
	child, err := tdfTreeObject(repo, childTree)
	if err != nil {
		return nil, fmt.Errorf("child tree %s: %w", childTree, err)
	}
	var parent *object.Tree
	if !parentTree.IsZero() {
		if parent, err = tdfTreeObject(repo, parentTree); err != nil {
			return nil, fmt.Errorf("parent tree %s: %w", parentTree, err)
		}
	}
	changes, err := object.DiffTreeContext(context.Background(), parent, child)
	if err != nil {
		return nil, fmt.Errorf("diff: %w", err)
	}

	var out []treeChange
	for _, change := range changes {
		action, err := change.Action()
		if err != nil {
			continue
		}
		switch action {
		case merkletrie.Insert:
			if change.To.TreeEntry.Mode.IsFile() {
				out = append(out, treeChange{treeChangeInsert, change.To.Name, plumbing.ZeroHash, change.To.TreeEntry.Hash})
			}
		case merkletrie.Modify:
			if change.To.TreeEntry.Mode.IsFile() {
				out = append(out, treeChange{treeChangeModify, change.To.Name, change.From.TreeEntry.Hash, change.To.TreeEntry.Hash})
			}
		case merkletrie.Delete:
			if change.From.TreeEntry.Mode.IsFile() {
				out = append(out, treeChange{treeChangeDelete, change.From.Name, change.From.TreeEntry.Hash, plumbing.ZeroHash})
			}
		}
	}
	return out, nil
}

func tdfKindName(kind treeChangeKind) string {
	switch kind {
	case treeChangeInsert:
		return "insert"
	case treeChangeModify:
		return "modify"
	case treeChangeDelete:
		return "delete"
	}
	return fmt.Sprintf("kind(%d)", int(kind))
}

// tdfFull renders every field so a wrong hash fails the comparison, not just a
// wrong path.
func tdfFull(changes []treeChange) []string {
	out := make([]string, 0, len(changes))
	for _, c := range changes {
		out = append(out, fmt.Sprintf("%s %q old=%s new=%s", tdfKindName(c.Kind), c.Path, c.OldHash, c.NewHash))
	}
	slices.Sort(out)
	return out
}

// tdfBrief renders kind and path only, for hand-written expectations.
func tdfBrief(changes []treeChange) []string {
	out := make([]string, 0, len(changes))
	for _, c := range changes {
		out = append(out, fmt.Sprintf("%s %s", tdfKindName(c.Kind), c.Path))
	}
	slices.Sort(out)
	return out
}

func tdfNewDiffer(tb testing.TB, gitDir string) *gitTreeDiffer {
	tb.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	d, err := newGitTreeDiffer(ctx, gitDir)
	if err != nil {
		cancel()
		tb.Fatalf("newGitTreeDiffer: %v", err)
	}
	tb.Cleanup(func() {
		if err := d.Close(); err != nil {
			tb.Errorf("Close: %v", err)
		}
		cancel()
	})
	return d
}

func tdfSkipShort(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("short: spawns git subprocesses")
	}
}

func tdfSkipNoShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("scripted git needs a POSIX shell")
	}
}

// --- equivalence -------------------------------------------------------------

const (
	tdfShaA = "1111111111111111111111111111111111111111"
	tdfShaB = "2222222222222222222222222222222222222222"
)

func tdfSnap(files map[string]tdfFile, edits ...func(map[string]tdfFile)) map[string]tdfFile {
	out := make(map[string]tdfFile, len(files))
	for p, f := range files {
		out[p] = f
	}
	for _, edit := range edits {
		edit(out)
	}
	return out
}

func tdfSet(p string, f tdfFile) func(map[string]tdfFile) {
	return func(m map[string]tdfFile) { m[p] = f }
}

func tdfDel(p string) func(map[string]tdfFile) {
	return func(m map[string]tdfFile) { delete(m, p) }
}

type tdfEquivalenceCase struct {
	name    string
	commits []tdfCommit
	// want maps "child<-parent" (parent "root" for a root commit) to the
	// expected "kind path" set; pairs not listed are only checked against go-git
	want map[string][]string
}

func tdfEquivalenceCases() []tdfEquivalenceCase {
	basic1 := map[string]tdfFile{
		"a.txt":         tdfReg("one"),
		"dir/b.txt":     tdfReg("bee"),
		"dir/sub/c.txt": tdfReg("sea"),
	}
	basic2 := tdfSnap(basic1, tdfSet("a.txt", tdfReg("one changed")), tdfDel("dir/b.txt"), tdfSet("new.go", tdfReg("package x")))

	exec1 := map[string]tdfFile{"x.sh": tdfReg("#!/bin/sh"), "other": tdfReg("o")}

	link1 := map[string]tdfFile{"link": tdfReg("plain"), "keep": tdfReg("k")}
	link2 := tdfSnap(link1, tdfSet("link", tdfLink("target")))
	link3 := tdfSnap(link2, tdfSet("link2", tdfLink("other-target")))
	link4 := tdfSnap(link3, tdfDel("link"))
	link5 := tdfSnap(link4, tdfSet("link", tdfReg("plain again")))

	dir1 := map[string]tdfFile{"x": tdfReg("file x"), "keep.txt": tdfReg("k")}
	dir2 := map[string]tdfFile{"x/inner.txt": tdfReg("i"), "x/deep/more.txt": tdfReg("m"), "keep.txt": tdfReg("k")}
	dir4 := tdfSnap(dir1, tdfSet("a/b/c", tdfReg("leaf")))
	dir5 := tdfSnap(dir1, tdfSet("a/b", tdfReg("now a file")))

	sub1 := map[string]tdfFile{"README": tdfReg("r"), "sub": tdfGitlink(tdfShaA)}
	sub2 := tdfSnap(sub1, tdfSet("sub", tdfGitlink(tdfShaB)))
	sub3 := tdfSnap(sub2, tdfDel("sub"))
	sub4 := tdfSnap(sub3, tdfSet("p", tdfReg("plain file")))
	sub5 := tdfSnap(sub4, tdfSet("p", tdfGitlink(tdfShaA)))
	sub6 := tdfSnap(sub5, tdfSet("p", tdfReg("plain file again")))
	sub7 := tdfSnap(sub6, tdfSet("g", tdfGitlink(tdfShaA)))
	sub8 := tdfSnap(sub6, tdfSet("g/file.txt", tdfReg("now a dir")))
	sub9 := tdfSnap(sub6, tdfSet("g", tdfGitlink(tdfShaB)))

	mergeBase := map[string]tdfFile{"a.txt": tdfReg("a")}
	mergeMain := tdfSnap(mergeBase, tdfSet("m.txt", tdfReg("m")))
	mergeSide := tdfSnap(mergeBase, tdfSet("s.txt", tdfReg("s")), tdfSet("a.txt", tdfReg("a edited on side")))
	mergeBoth := tdfSnap(mergeMain, tdfSet("s.txt", tdfReg("s")), tdfSet("a.txt", tdfReg("a edited on side")))

	odd := map[string]tdfFile{
		"with space/a b.txt":    tdfReg("1"),
		"café/ünï.txt":          tdfReg("2"),
		"日本語/ファイル.txt":          tdfReg("3"),
		"line\nbreak.txt":       tdfReg("4"),
		"-dash.txt":             tdfReg("5"),
		"quo\"te.txt":           tdfReg("6"),
		"back\\slash.txt":       tdfReg("7"),
		"tab\there.txt":         tdfReg("8"),
		"trailing space /f":     tdfReg("9"),
		"emoji-\U0001F600.txt":  tdfReg("10"),
		"nul-lookalike\\0.txt":  tdfReg("11"),
		"colon:first/:lead.txt": tdfReg("12"),
	}
	oddEdited := tdfSnap(odd)
	for p, f := range oddEdited {
		oddEdited[p] = tdfReg(f.content + " edited")
	}

	deepPath := strings.Repeat("d/", 40) + "leaf.txt"
	deep1 := map[string]tdfFile{deepPath: tdfReg("leaf"), "top.txt": tdfReg("t")}
	deep2 := tdfSnap(deep1, tdfSet(deepPath, tdfReg("leaf v2")), tdfSet(strings.Repeat("d/", 20)+"mid.txt", tdfReg("mid")))
	deep3 := tdfSnap(deep2, tdfDel(deepPath))

	// git sorts a directory as "name/", so "a" the dir lands after "a.txt" and
	// "a-b" but before "a0"; a diff that mishandles that order misses entries
	order1 := map[string]tdfFile{
		"a/x": tdfReg("x"), "a.txt": tdfReg("t"), "a-b": tdfReg("b"), "a0": tdfReg("z"), "a+": tdfReg("p"),
	}
	order2 := tdfSnap(order1,
		tdfSet("a/x", tdfReg("x2")), tdfSet("a.txt", tdfReg("t2")), tdfSet("a-b", tdfReg("b2")),
		tdfSet("a0", tdfReg("z2")), tdfSet("a+", tdfReg("p2")))
	order3 := tdfSnap(order2, tdfDel("a/x"), tdfSet("a", tdfReg("a is a file now")))

	big := make(map[string]tdfFile, 6000)
	bigWant := make([]string, 0, 6000)
	for i := range 6000 {
		p := fmt.Sprintf("pkg%02d/file%04d.go", i%40, i)
		big[p] = tdfReg(fmt.Sprintf("package p\n// %d\n", i))
		bigWant = append(bigWant, "insert "+p)
	}
	slices.Sort(bigWant)

	return []tdfEquivalenceCase{
		{
			name: "add modify delete and empty-diff commit",
			commits: []tdfCommit{
				{"c1", nil, basic1},
				{"c2", []string{"c1"}, basic2},
				{"c3", []string{"c2"}, basic2},
			},
			want: map[string][]string{
				"c1<-root": {"insert a.txt", "insert dir/b.txt", "insert dir/sub/c.txt"},
				"c2<-c1":   {"delete dir/b.txt", "insert new.go", "modify a.txt"},
				"c3<-c2":   {},
			},
		},
		{
			name: "exec bit only change",
			commits: []tdfCommit{
				{"c1", nil, exec1},
				{"c2", []string{"c1"}, tdfSnap(exec1, tdfSet("x.sh", tdfExec("#!/bin/sh")))},
				{"c3", []string{"c2"}, exec1},
			},
			want: map[string][]string{
				"c2<-c1": {"modify x.sh"},
				"c3<-c2": {"modify x.sh"},
			},
		},
		{
			name: "symlink add and file symlink type change",
			commits: []tdfCommit{
				{"c1", nil, link1},
				{"c2", []string{"c1"}, link2},
				{"c3", []string{"c2"}, link3},
				{"c4", []string{"c3"}, link4},
				{"c5", []string{"c4"}, link5},
			},
			want: map[string][]string{
				"c2<-c1": {"modify link"},
				"c3<-c2": {"insert link2"},
				"c4<-c3": {"delete link"},
				"c5<-c4": {"insert link"},
			},
		},
		{
			name: "file to directory and back",
			commits: []tdfCommit{
				{"c1", nil, dir1},
				{"c2", []string{"c1"}, dir2},
				{"c3", []string{"c2"}, dir1},
				{"c4", []string{"c3"}, dir4},
				{"c5", []string{"c4"}, dir5},
			},
			want: map[string][]string{
				"c2<-c1": {"delete x", "insert x/deep/more.txt", "insert x/inner.txt"},
				"c3<-c2": {"delete x/deep/more.txt", "delete x/inner.txt", "insert x"},
				"c5<-c4": {"delete a/b/c", "insert a/b"},
			},
		},
		{
			name: "submodule gitlinks never reported",
			commits: []tdfCommit{
				{"c1", nil, sub1},
				{"c2", []string{"c1"}, sub2},
				{"c3", []string{"c2"}, sub3},
				{"c4", []string{"c3"}, sub4},
				{"c5", []string{"c4"}, sub5},
				{"c6", []string{"c5"}, sub6},
				{"c7", []string{"c6"}, sub7},
				{"c8", []string{"c6"}, sub8},
				{"c9", []string{"c8"}, sub9},
			},
			want: map[string][]string{
				"c1<-root": {"insert README"},
				"c2<-c1":   {},                    // gitlink update
				"c3<-c2":   {},                    // gitlink removed
				"c5<-c4":   {},                    // file -> gitlink
				"c6<-c5":   {"modify p"},          // gitlink -> file
				"c7<-c6":   {},                    // gitlink added
				"c8<-c6":   {"insert g/file.txt"}, // nothing -> dir
				"c9<-c8":   {"delete g/file.txt"}, // dir -> gitlink
			},
		},
		{
			name: "merge commit against both parents",
			commits: []tdfCommit{
				{"base", nil, mergeBase},
				{"main1", []string{"base"}, mergeMain},
				{"side1", []string{"base"}, mergeSide},
				{"merged", []string{"main1", "side1"}, mergeBoth},
			},
			want: map[string][]string{
				"merged<-main1": {"insert s.txt", "modify a.txt"},
				"merged<-side1": {"insert m.txt"},
			},
		},
		{
			name: "rename is delete plus insert",
			commits: []tdfCommit{
				{"r1", nil, map[string]tdfFile{"old.txt": tdfReg("same body\nsame body\nsame body\n")}},
				{"r2", []string{"r1"}, map[string]tdfFile{"new.txt": tdfReg("same body\nsame body\nsame body\n")}},
			},
			want: map[string][]string{
				"r2<-r1": {"delete old.txt", "insert new.txt"},
			},
		},
		{
			name: "awkward path bytes",
			commits: []tdfCommit{
				{"c1", nil, odd},
				{"c2", []string{"c1"}, oddEdited},
			},
			want: map[string][]string{
				"c1<-root": tdfBrief(func() []treeChange {
					var cs []treeChange
					for p := range odd {
						cs = append(cs, treeChange{Kind: treeChangeInsert, Path: p})
					}
					return cs
				}()),
				"c2<-c1": tdfBrief(func() []treeChange {
					var cs []treeChange
					for p := range odd {
						cs = append(cs, treeChange{Kind: treeChangeModify, Path: p})
					}
					return cs
				}()),
			},
		},
		{
			name: "deep nesting",
			commits: []tdfCommit{
				{"c1", nil, deep1},
				{"c2", []string{"c1"}, deep2},
				{"c3", []string{"c2"}, deep3},
			},
			want: map[string][]string{
				"c2<-c1": {"insert " + strings.Repeat("d/", 20) + "mid.txt", "modify " + deepPath},
				"c3<-c2": {"delete " + deepPath},
			},
		},
		{
			name: "directory sort order against dotted and dashed siblings",
			commits: []tdfCommit{
				{"c1", nil, order1},
				{"c2", []string{"c1"}, order2},
				{"c3", []string{"c2"}, order3},
			},
			want: map[string][]string{
				"c2<-c1": {"modify a+", "modify a-b", "modify a.txt", "modify a/x", "modify a0"},
				"c3<-c2": {"delete a/x", "insert a"},
			},
		},
		{
			name: "empty tree root and back to empty",
			commits: []tdfCommit{
				{"e1", nil, map[string]tdfFile{}},
				{"e2", []string{"e1"}, map[string]tdfFile{"f": tdfReg("f")}},
				{"e3", []string{"e2"}, map[string]tdfFile{}},
			},
			want: map[string][]string{
				// empty tree against empty tree: the header is all there is
				"e1<-root": {},
				"e2<-e1":   {"insert f"},
				"e3<-e2":   {"delete f"},
			},
		},
		{
			name: "response larger than the pipe buffer",
			commits: []tdfCommit{
				{"b1", nil, big},
				{"b2", []string{"b1"}, tdfSnap(big, tdfSet("pkg00/file0000.go", tdfReg("changed")))},
			},
			want: map[string][]string{
				"b1<-root": bigWant,
				"b2<-b1":   {"modify pkg00/file0000.go"},
			},
		},
	}
}

// Failure prevented: the git differ silently disagreeing with go-git on which
// files a commit touched, which would corrupt the commit/diff index.
func TestGitTreeDifferEquivalence(t *testing.T) {
	t.Parallel()
	tdfSkipShort(t)

	for _, tt := range tdfEquivalenceCases() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fx := tdfBuild(t, tt.commits)
			d := tdfNewDiffer(t, fx.gitDir)

			checked := make(map[string]bool)
			for _, c := range tt.commits {
				parents := c.parents
				if len(parents) == 0 {
					parents = []string{"root"}
				}
				for _, parent := range parents {
					parentTree := plumbing.ZeroHash
					if parent != "root" {
						parentTree = fx.tree[parent]
					}
					pair := c.name + "<-" + parent

					got, err := d.DiffTrees(context.Background(), parentTree, fx.tree[c.name])
					if err != nil {
						t.Fatalf("%s: DiffTrees: %v", pair, err)
					}
					want := tdfReferenceDiff(t, fx.repo, parentTree, fx.tree[c.name])
					if g, w := tdfFull(got), tdfFull(want); !slices.Equal(g, w) {
						t.Errorf("%s: differs from go-git\n got: %q\nwant: %q", pair, g, w)
					}
					if explicit, ok := tt.want[pair]; ok {
						checked[pair] = true
						if g := tdfBrief(got); !slices.Equal(g, explicit) {
							t.Errorf("%s: unexpected changes\n got: %q\nwant: %q", pair, g, explicit)
						}
					}
				}
			}
			for pair := range tt.want {
				if !checked[pair] {
					t.Errorf("expectation %q matched no commit pair", pair)
				}
			}
		})
	}
}

// Failure prevented: a type change or gitlink transition reporting the wrong
// hash side, which would make the indexer read a submodule commit id as a blob.
func TestGitTreeDifferHashes(t *testing.T) {
	t.Parallel()
	tdfSkipShort(t)

	fx := tdfBuild(t, []tdfCommit{
		{"c1", nil, map[string]tdfFile{"p": tdfGitlink(tdfShaA), "x.sh": tdfReg("#!/bin/sh"), "gone": tdfReg("bye")}},
		{"c2", []string{"c1"}, map[string]tdfFile{"p": tdfReg("now a file"), "x.sh": tdfExec("#!/bin/sh")}},
	})
	d := tdfNewDiffer(t, fx.gitDir)
	got, err := d.DiffTrees(context.Background(), fx.tree["c1"], fx.tree["c2"])
	if err != nil {
		t.Fatalf("DiffTrees: %v", err)
	}
	byPath := make(map[string]treeChange)
	for _, c := range got {
		byPath[c.Path] = c
	}

	if c := byPath["p"]; c.Kind != treeChangeModify || c.OldHash != plumbing.NewHash(tdfShaA) || c.NewHash.IsZero() {
		t.Errorf("gitlink -> file: got %+v, want modify with old=gitlink id and a new blob hash", c)
	}
	if c := byPath["x.sh"]; c.Kind != treeChangeModify || c.OldHash != c.NewHash || c.OldHash.IsZero() {
		t.Errorf("exec bit flip: got %+v, want modify with identical old and new hash", c)
	}
	if c := byPath["gone"]; c.Kind != treeChangeDelete || c.OldHash.IsZero() || !c.NewHash.IsZero() {
		t.Errorf("delete: got %+v, want old hash set and new hash zero", c)
	}
}

// Failure prevented: raw-status mapping drifting from indexCommit's filter.
func TestGitTreeDifferMapRawChange(t *testing.T) {
	t.Parallel()

	oldHash := plumbing.NewHash(tdfShaA)
	newHash := plumbing.NewHash(tdfShaB)
	reg, exe, link, gitlink := filemode.Regular, filemode.Executable, filemode.Symlink, filemode.Submodule

	tests := []struct {
		name     string
		status   byte
		oldMode  filemode.FileMode
		newMode  filemode.FileMode
		wantKeep bool
		want     treeChange
		wantErr  bool
	}{
		{name: "insert regular", status: 'A', newMode: reg, wantKeep: true,
			want: treeChange{treeChangeInsert, "p", plumbing.ZeroHash, newHash}},
		{name: "insert executable", status: 'A', newMode: exe, wantKeep: true,
			want: treeChange{treeChangeInsert, "p", plumbing.ZeroHash, newHash}},
		{name: "insert symlink", status: 'A', newMode: link, wantKeep: true,
			want: treeChange{treeChangeInsert, "p", plumbing.ZeroHash, newHash}},
		{name: "insert gitlink skipped", status: 'A', newMode: gitlink},
		{name: "modify regular", status: 'M', oldMode: reg, newMode: reg, wantKeep: true,
			want: treeChange{treeChangeModify, "p", oldHash, newHash}},
		{name: "modify gitlink skipped", status: 'M', oldMode: gitlink, newMode: gitlink},
		{name: "type change file to symlink is modify", status: 'T', oldMode: reg, newMode: link, wantKeep: true,
			want: treeChange{treeChangeModify, "p", oldHash, newHash}},
		{name: "type change file to gitlink skipped", status: 'T', oldMode: reg, newMode: gitlink},
		{name: "type change gitlink to file is modify", status: 'T', oldMode: gitlink, newMode: reg, wantKeep: true,
			want: treeChange{treeChangeModify, "p", oldHash, newHash}},
		{name: "delete regular", status: 'D', oldMode: reg, wantKeep: true,
			want: treeChange{treeChangeDelete, "p", oldHash, plumbing.ZeroHash}},
		{name: "delete symlink", status: 'D', oldMode: link, wantKeep: true,
			want: treeChange{treeChangeDelete, "p", oldHash, plumbing.ZeroHash}},
		{name: "delete gitlink skipped", status: 'D', oldMode: gitlink},
		{name: "rename status rejected", status: 'R', oldMode: reg, newMode: reg, wantErr: true},
		{name: "unmerged status rejected", status: 'U', oldMode: reg, newMode: reg, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, keep, err := mapRawChange(tt.status, tt.oldMode, tt.newMode, oldHash, newHash, "p")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				if !errors.Is(err, errGitTreeDifferProtocol) {
					t.Errorf("err = %v, want errGitTreeDifferProtocol", err)
				}
				return
			}
			if keep != tt.wantKeep {
				t.Fatalf("keep = %v, want %v", keep, tt.wantKeep)
			}
			if keep && got != tt.want {
				t.Errorf("change = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// tdfChunkReader hands out one preset chunk per Read, so a test controls exactly
// where the bufio buffer has to refill.
type tdfChunkReader struct{ chunks []string }

func (c *tdfChunkReader) Read(p []byte) (int, error) {
	if len(c.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.chunks[0])
	c.chunks[0] = c.chunks[0][n:]
	if c.chunks[0] == "" {
		c.chunks = c.chunks[1:]
	}
	return n, nil
}

// Failure prevented: record fields aliasing the read buffer. The meta line is a
// view into the buffer; reading a long path right after it refills the buffer
// over those bytes. Pipe chunking decides when that happens, so a live git run
// hits it only occasionally; this pins it deterministically.
func TestGitTreeDifferRecordSurvivesBufferRefill(t *testing.T) {
	t.Parallel()

	const blob = "2222222222222222222222222222222222222222"
	meta := ":000000 100644 0000000000000000000000000000000000000000 " + blob + " A\x00"
	longPath := strings.Repeat("deep-directory-name/", 6) + "file.go"

	// first chunk ends exactly after the meta line, so the path read must refill
	reader := &tdfChunkReader{chunks: []string{meta, longPath + "\x00"}}
	d := &gitTreeDiffer{stdout: bufio.NewReaderSize(reader, 256)}
	if b, err := d.stdout.ReadByte(); err != nil || b != ':' {
		t.Fatalf("setup: first byte %q, err %v", b, err)
	}

	change, keep, err := d.readRecord()
	if err != nil {
		t.Fatalf("readRecord: %v", err)
	}
	want := treeChange{Kind: treeChangeInsert, Path: longPath, OldHash: plumbing.ZeroHash, NewHash: plumbing.NewHash(blob)}
	if !keep || change != want {
		t.Errorf("got keep=%v %+v, want keep=true %+v", keep, change, want)
	}
}

// Failure prevented: a differ left over from one commit leaking state into the
// next, e.g. an unread end marker desynchronizing every later response.
func TestGitTreeDifferManyRequestsStayInSync(t *testing.T) {
	t.Parallel()
	tdfSkipShort(t)

	fx := tdfBuild(t, []tdfCommit{
		{"c1", nil, map[string]tdfFile{"a": tdfReg("1"), "b": tdfReg("1")}},
		{"c2", []string{"c1"}, map[string]tdfFile{"a": tdfReg("2"), "b": tdfReg("1")}},
		{"c3", []string{"c2"}, map[string]tdfFile{"a": tdfReg("2")}},
	})
	d := tdfNewDiffer(t, fx.gitDir)

	pairs := [][2]string{{"c1", "c2"}, {"c1", "c1"}, {"c2", "c3"}, {"c3", "c1"}, {"c1", "c3"}, {"c2", "c1"}}
	for round := range 20 {
		for _, p := range pairs {
			got, err := d.DiffTrees(context.Background(), fx.tree[p[0]], fx.tree[p[1]])
			if err != nil {
				t.Fatalf("round %d %v: %v", round, p, err)
			}
			want := tdfReferenceDiff(t, fx.repo, fx.tree[p[0]], fx.tree[p[1]])
			if g, w := tdfFull(got), tdfFull(want); !slices.Equal(g, w) {
				t.Fatalf("round %d %v: got %q want %q", round, p, g, w)
			}
		}
	}
}

// --- failure modes -----------------------------------------------------------

// tdfDiffWithin fails the test if DiffTrees does not return within d.
func tdfDiffWithin(ctx context.Context, t *testing.T, d treeDiffer, parent, child plumbing.Hash, within time.Duration) ([]treeChange, error) {
	t.Helper()
	type result struct {
		changes []treeChange
		err     error
	}
	done := make(chan result, 1)
	go func() {
		changes, err := d.DiffTrees(ctx, parent, child)
		done <- result{changes, err}
	}()
	select {
	case r := <-done:
		return r.changes, r.err
	case <-time.After(within):
		t.Fatalf("DiffTrees did not return within %s (hang)", within)
		return nil, nil
	}
}

// Failure prevented: a dead git process hanging the indexer forever instead of
// surfacing an error the fallback can act on.
func TestGitTreeDifferProcessDeath(t *testing.T) {
	t.Parallel()
	tdfSkipShort(t)

	fx := tdfBuild(t, []tdfCommit{
		{"c1", nil, map[string]tdfFile{"a": tdfReg("1")}},
		{"c2", []string{"c1"}, map[string]tdfFile{"a": tdfReg("2")}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, err := newGitTreeDiffer(ctx, fx.gitDir)
	if err != nil {
		t.Fatalf("newGitTreeDiffer: %v", err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Errorf("Close after failure: %v", err)
		}
	}()

	if _, err := tdfDiffWithin(ctx, t, d, fx.tree["c1"], fx.tree["c2"], 10*time.Second); err != nil {
		t.Fatalf("diff before kill: %v", err)
	}

	if err := d.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill git: %v", err)
	}

	_, err = tdfDiffWithin(ctx, t, d, fx.tree["c1"], fx.tree["c2"], 10*time.Second)
	if err == nil {
		t.Fatal("diff after the process was killed returned no error")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("process death reported as context error %v; the fallback would not engage", err)
	}

	// every later call fails fast instead of writing to a dead pipe
	_, err = tdfDiffWithin(ctx, t, d, fx.tree["c1"], fx.tree["c2"], 10*time.Second)
	if err == nil {
		t.Fatal("second diff after failure returned no error")
	}
}

// Failure prevented: git ignoring a request it cannot resolve (it prints
// nothing, not even an error) being mistaken for an empty diff, which would
// record a commit as having no changes.
func TestGitTreeDifferUnknownTreeIsAnError(t *testing.T) {
	t.Parallel()
	tdfSkipShort(t)

	fx := tdfBuild(t, []tdfCommit{{"c1", nil, map[string]tdfFile{"a": tdfReg("1")}}})
	d := tdfNewDiffer(t, fx.gitDir)

	missing := plumbing.NewHash("deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	got, err := tdfDiffWithin(context.Background(), t, d, plumbing.ZeroHash, missing, 10*time.Second)
	if err == nil {
		t.Fatalf("unknown tree returned changes %v and no error", got)
	}
	if !errors.Is(err, errGitTreeDifferProtocol) {
		t.Errorf("err = %v, want errGitTreeDifferProtocol", err)
	}
}

// Failure prevented: a constructor that "succeeds" against a git dir git cannot
// use, pushing the failure into the middle of an indexing run.
func TestGitTreeDifferConstructorFailures(t *testing.T) {
	t.Parallel()
	tdfSkipShort(t)

	t.Run("not a git dir", func(t *testing.T) {
		t.Parallel()
		d, err := tdfConstructWithin(t, filepath.Join(t.TempDir(), "nope"))
		if err == nil {
			_ = d.Close()
			t.Fatal("expected an error for a missing git dir")
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		t.Parallel()
		fx := tdfBuild(t, []tdfCommit{{"c1", nil, map[string]tdfFile{"a": tdfReg("1")}}})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		d, err := newGitTreeDiffer(ctx, fx.gitDir)
		if err == nil {
			_ = d.Close()
			t.Fatal("expected an error for a canceled context")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})
}

func tdfConstructWithin(t *testing.T, gitDir string) (*gitTreeDiffer, error) {
	t.Helper()
	type result struct {
		d   *gitTreeDiffer
		err error
	}
	done := make(chan result, 1)
	go func() {
		d, err := newGitTreeDiffer(context.Background(), gitDir)
		done <- result{d, err}
	}()
	select {
	case r := <-done:
		return r.d, r.err
	case <-time.After(10 * time.Second):
		t.Fatal("newGitTreeDiffer did not return within 10s (hang)")
		return nil, nil
	}
}

// tdfScripted starts script under sh as a stand-in for git, with no handshake.
// The script receives the request on its stdin, one line per request.
func tdfScripted(t *testing.T, script string) *gitTreeDiffer {
	t.Helper()
	tdfSkipNoShell(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d, err := startGitTreeDiffer(ctx, exec.CommandContext(ctx, "sh", "-c", script), false)
	if err != nil {
		t.Fatalf("start scripted differ: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// Failure prevented: malformed or truncated git output being parsed as a
// (partial) diff instead of failing the request.
func TestGitTreeDifferProtocolViolations(t *testing.T) {
	t.Parallel()

	const sha = "1111111111111111111111111111111111111111"
	tests := []struct {
		name      string
		script    string
		wantInErr string
		wantErrIs error
	}{
		{
			name:      "git crashes before replying",
			script:    `echo 'fatal: not a git repository' >&2; exit 128`,
			wantInErr: "not a git repository",
		},
		{
			name:      "garbage header",
			script:    `read -r a; read -r b; printf 'garbage\n'; exit 0`,
			wantErrIs: errGitTreeDifferProtocol,
		},
		{
			name:      "stream ends mid record",
			script:    `read -r a; read -r b; printf '%s\n:100644 100644 abc' "$a"; exit 0`,
			wantErrIs: io.ErrUnexpectedEOF,
		},
		{
			name:      "stream ends before the end marker",
			script:    `read -r a; read -r b; printf '%s\n' "$a"; exit 0`,
			wantErrIs: io.ErrUnexpectedEOF,
		},
		{
			name: "rename status",
			script: `read -r a; read -r b; printf '%s\n' "$a"; ` +
				`printf ':100644 100644 ` + sha + ` ` + sha + ` R100\000old\000new\000'; printf '%s\n' "$b"; exec cat >/dev/null`,
			wantErrIs: errGitTreeDifferProtocol,
		},
		{
			name: "bad object id",
			script: `read -r a; read -r b; printf '%s\n' "$a"; ` +
				`printf ':100644 100644 nothex nothex M\000p\000'; printf '%s\n' "$b"; exec cat >/dev/null`,
			wantErrIs: errGitTreeDifferProtocol,
		},
		{
			// what git does with ids of the wrong hash size: the request line is
			// echoed back, which reads like a header followed by an empty diff
			name:      "request echoed as pass-through",
			script:    `read -r a; read -r b; printf '%s\n%s\n' "$a" "$b"; exec cat >/dev/null`,
			wantErrIs: errGitTreeDifferProtocol,
		},
		{
			name: "output after the end marker",
			script: `read -r a; read -r b; printf '%s\n' "$a"; ` +
				`printf ':000000 100644 0000000000000000000000000000000000000000 ` + sha + ` A\000p\000'; ` +
				`printf '%s\nstray\n' "$b"; exec cat >/dev/null`,
			wantErrIs: errGitTreeDifferProtocol,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := tdfScripted(t, tt.script)
			child := plumbing.NewHash("3333333333333333333333333333333333333333")
			_, err := tdfDiffWithin(context.Background(), t, d, plumbing.ZeroHash, child, 10*time.Second)
			if err == nil {
				t.Fatal("expected an error")
			}
			if tt.wantErrIs != nil && !errors.Is(err, tt.wantErrIs) {
				t.Errorf("err = %v, want it to wrap %v", err, tt.wantErrIs)
			}
			if tt.wantInErr != "" && !strings.Contains(err.Error(), tt.wantInErr) {
				t.Errorf("err = %v, want it to mention %q", err, tt.wantInErr)
			}
		})
	}
}

// Failure prevented: a canceled index run leaving a git process running or
// blocking forever on a read that ignores the context.
func TestGitTreeDifferContextCancellationKillsProcess(t *testing.T) {
	t.Parallel()

	// a git that never answers but keeps its pipes open
	d := tdfScripted(t, `exec sleep 60`)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := tdfDiffWithin(ctx, t, d, plumbing.ZeroHash, plumbing.NewHash("3333333333333333333333333333333333333333"), 10*time.Second)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("cancellation took %s", elapsed)
	}
	if !d.killed.Load() {
		t.Error("process was not killed on cancellation")
	}
	if d.cmd.ProcessState == nil {
		t.Error("process was not reaped on cancellation")
	}
}

// Failure prevented: an already-canceled context starting work, or breaking a
// healthy differ that merely saw a canceled caller.
func TestGitTreeDifferCanceledBeforeStartLeavesDifferUsable(t *testing.T) {
	t.Parallel()
	tdfSkipShort(t)

	fx := tdfBuild(t, []tdfCommit{
		{"c1", nil, map[string]tdfFile{"a": tdfReg("1")}},
		{"c2", []string{"c1"}, map[string]tdfFile{"a": tdfReg("2")}},
	})
	d := tdfNewDiffer(t, fx.gitDir)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.DiffTrees(canceled, fx.tree["c1"], fx.tree["c2"]); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := d.DiffTrees(context.Background(), fx.tree["c1"], fx.tree["c2"]); err != nil {
		t.Errorf("differ unusable after a call that never started: %v", err)
	}
}

// Failure prevented: Close hanging on a git that does not exit when stdin closes.
func TestGitTreeDifferCloseKillsStubbornProcess(t *testing.T) {
	t.Parallel()
	tdfSkipNoShell(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// ignores stdin EOF and sleeps
	d, err := startGitTreeDiffer(ctx, exec.CommandContext(ctx, "sh", "-c", `exec sleep 30`), false)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	d.closeTimeout = 100 * time.Millisecond

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- d.Close() }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "killed") {
			t.Errorf("Close err = %v, want one saying the process was killed", err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("Close took %s", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close hung on a process that ignores stdin EOF")
	}
	if d.cmd.ProcessState == nil {
		t.Error("process not reaped")
	}
	if err := d.Close(); err != nil {
		t.Errorf("second Close = %v, want nil", err)
	}
	if _, err := d.DiffTrees(context.Background(), plumbing.ZeroHash, plumbing.NewHash(tdfShaA)); !errors.Is(err, errGitTreeDifferClosed) {
		t.Errorf("DiffTrees after Close err = %v, want errGitTreeDifferClosed", err)
	}
}

// Failure prevented: Close failing or leaking when git exits cleanly on EOF.
func TestGitTreeDifferCloseIsCleanAndIdempotent(t *testing.T) {
	t.Parallel()
	tdfSkipShort(t)

	fx := tdfBuild(t, []tdfCommit{{"c1", nil, map[string]tdfFile{"a": tdfReg("1")}}})
	d, err := newGitTreeDiffer(context.Background(), fx.gitDir)
	if err != nil {
		t.Fatalf("newGitTreeDiffer: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close = %v, want nil for a clean exit", err)
	}
	if d.cmd.ProcessState == nil || !d.cmd.ProcessState.Success() {
		t.Errorf("git did not exit cleanly on stdin EOF: %v", d.cmd.ProcessState)
	}
	if err := d.Close(); err != nil {
		t.Errorf("second Close = %v, want nil", err)
	}
}

// --- fallback ----------------------------------------------------------------

type tdfFakeDiffer struct {
	changes  []treeChange
	err      error
	calls    int
	closed   int
	closeErr error
}

func (f *tdfFakeDiffer) DiffTrees(context.Context, plumbing.Hash, plumbing.Hash) ([]treeChange, error) {
	f.calls++
	return f.changes, f.err
}

func (f *tdfFakeDiffer) Close() error {
	f.closed++
	return f.closeErr
}

// Failure prevented: one broken git process failing every commit of a run, or
// the fallback engaging more than once and flooding logs.
func TestFallbackTreeDiffer(t *testing.T) {
	t.Parallel()

	primaryErr := errors.New("git exploded")
	fromPrimary := []treeChange{{Kind: treeChangeInsert, Path: "primary"}}
	fromFallback := []treeChange{{Kind: treeChangeInsert, Path: "fallback"}}
	parent, child := plumbing.NewHash(tdfShaA), plumbing.NewHash(tdfShaB)

	t.Run("healthy primary never touches the fallback", func(t *testing.T) {
		t.Parallel()
		primary, fallback := &tdfFakeDiffer{changes: fromPrimary}, &tdfFakeDiffer{changes: fromFallback}
		var notified int
		f := &fallbackTreeDiffer{primary: primary, fallback: fallback, onFallback: func(error) { notified++ }}
		for range 3 {
			got, err := f.DiffTrees(context.Background(), parent, child)
			if err != nil || len(got) != 1 || got[0].Path != "primary" {
				t.Fatalf("got %v, %v", got, err)
			}
		}
		if fallback.calls != 0 || notified != 0 {
			t.Errorf("fallback calls=%d notified=%d, want 0 and 0", fallback.calls, notified)
		}
	})

	t.Run("primary error switches permanently and retries the failed diff", func(t *testing.T) {
		t.Parallel()
		primary, fallback := &tdfFakeDiffer{err: primaryErr}, &tdfFakeDiffer{changes: fromFallback}
		var notified []error
		f := &fallbackTreeDiffer{primary: primary, fallback: fallback, onFallback: func(err error) { notified = append(notified, err) }}

		for i := range 3 {
			got, err := f.DiffTrees(context.Background(), parent, child)
			if err != nil || len(got) != 1 || got[0].Path != "fallback" {
				t.Fatalf("call %d: got %v, %v; want the fallback's result", i, got, err)
			}
		}
		if len(notified) != 1 || !errors.Is(notified[0], primaryErr) {
			t.Errorf("onFallback calls = %v, want exactly one carrying the primary's error", notified)
		}
		if primary.calls != 1 {
			t.Errorf("primary called %d times, want 1 (permanent switch)", primary.calls)
		}
		if fallback.calls != 3 {
			t.Errorf("fallback called %d times, want 3", fallback.calls)
		}
		if primary.closed != 1 {
			t.Errorf("primary closed %d times at the switch, want 1", primary.closed)
		}
	})

	t.Run("cancellation is not a primary failure", func(t *testing.T) {
		t.Parallel()
		for name, errFor := range map[string]func() (context.Context, context.CancelFunc, error){
			"canceled": func() (context.Context, context.CancelFunc, error) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel, context.Canceled
			},
			"deadline": func() (context.Context, context.CancelFunc, error) {
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				return ctx, cancel, context.DeadlineExceeded
			},
		} {
			ctx, cancel, ctxErr := errFor()
			primary, fallback := &tdfFakeDiffer{err: fmt.Errorf("wrapped: %w", ctxErr)}, &tdfFakeDiffer{changes: fromFallback}
			var notified int
			f := &fallbackTreeDiffer{primary: primary, fallback: fallback, onFallback: func(error) { notified++ }}

			_, err := f.DiffTrees(ctx, parent, child)
			cancel()
			if !errors.Is(err, ctxErr) {
				t.Errorf("%s: err = %v, want it to wrap %v", name, err, ctxErr)
			}
			if notified != 0 || fallback.calls != 0 || primary.closed != 0 {
				t.Errorf("%s: notified=%d fallbackCalls=%d primaryClosed=%d, want all 0", name, notified, fallback.calls, primary.closed)
			}

			// not switched: a later healthy call still goes to the primary
			primary.err, primary.changes = nil, fromPrimary
			got, err := f.DiffTrees(context.Background(), parent, child)
			if err != nil || len(got) != 1 || got[0].Path != "primary" {
				t.Errorf("%s: after cancellation got %v, %v; want the primary's result", name, got, err)
			}
		}
	})

	t.Run("fallback error is returned wrapped", func(t *testing.T) {
		t.Parallel()
		fallbackErr := errors.New("go-git exploded too")
		f := &fallbackTreeDiffer{primary: &tdfFakeDiffer{err: primaryErr}, fallback: &tdfFakeDiffer{err: fallbackErr}}
		if _, err := f.DiffTrees(context.Background(), parent, child); !errors.Is(err, fallbackErr) {
			t.Errorf("err = %v, want it to wrap the fallback's error", err)
		}
	})

	t.Run("nil onFallback is allowed", func(t *testing.T) {
		t.Parallel()
		f := &fallbackTreeDiffer{primary: &tdfFakeDiffer{err: primaryErr}, fallback: &tdfFakeDiffer{changes: fromFallback}}
		if _, err := f.DiffTrees(context.Background(), parent, child); err != nil {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("Close closes both exactly once and joins errors", func(t *testing.T) {
		t.Parallel()
		errPrimary, errFallback := errors.New("primary close"), errors.New("fallback close")

		unswitched := &fallbackTreeDiffer{
			primary:  &tdfFakeDiffer{closeErr: errPrimary},
			fallback: &tdfFakeDiffer{closeErr: errFallback},
		}
		err := unswitched.Close()
		if !errors.Is(err, errPrimary) || !errors.Is(err, errFallback) {
			t.Errorf("Close err = %v, want both close errors", err)
		}

		primary, fallback := &tdfFakeDiffer{err: primaryErr}, &tdfFakeDiffer{changes: fromFallback}
		switched := &fallbackTreeDiffer{primary: primary, fallback: fallback}
		if _, err := switched.DiffTrees(context.Background(), parent, child); err != nil {
			t.Fatalf("DiffTrees: %v", err)
		}
		if err := switched.Close(); err != nil {
			t.Errorf("Close = %v", err)
		}
		if primary.closed != 1 || fallback.closed != 1 {
			t.Errorf("closed primary=%d fallback=%d, want 1 and 1", primary.closed, fallback.closed)
		}
	})
}

// tdfReferenceDiffer adapts the test oracle to treeDiffer, standing in for the
// production go-git differ owned by another file.
type tdfReferenceDiffer struct {
	repo *git.Repository
}

func (r *tdfReferenceDiffer) DiffTrees(_ context.Context, parent, child plumbing.Hash) ([]treeChange, error) {
	return tdfReferenceDiffErr(r.repo, parent, child)
}

func (r *tdfReferenceDiffer) Close() error { return nil }

// Failure prevented: the real wiring (git primary, go-git fallback) not
// surviving the git process dying mid-run.
func TestFallbackTreeDifferSurvivesGitProcessDeath(t *testing.T) {
	t.Parallel()
	tdfSkipShort(t)

	fx := tdfBuild(t, []tdfCommit{
		{"c1", nil, map[string]tdfFile{"a": tdfReg("1")}},
		{"c2", []string{"c1"}, map[string]tdfFile{"a": tdfReg("2"), "b": tdfReg("b")}},
		{"c3", []string{"c2"}, map[string]tdfFile{"b": tdfReg("b")}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	primary, err := newGitTreeDiffer(ctx, fx.gitDir)
	if err != nil {
		t.Fatalf("newGitTreeDiffer: %v", err)
	}
	var notified []error
	f := &fallbackTreeDiffer{
		primary:    primary,
		fallback:   &tdfReferenceDiffer{repo: fx.repo},
		onFallback: func(err error) { notified = append(notified, err) },
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	if _, err := tdfDiffWithin(ctx, t, f, fx.tree["c1"], fx.tree["c2"], 10*time.Second); err != nil {
		t.Fatalf("diff before kill: %v", err)
	}
	if len(notified) != 0 {
		t.Fatalf("fallback engaged on a healthy primary: %v", notified)
	}

	if err := primary.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill git: %v", err)
	}

	for i, pair := range [][2]string{{"c2", "c3"}, {"c1", "c3"}} {
		got, err := tdfDiffWithin(ctx, t, f, fx.tree[pair[0]], fx.tree[pair[1]], 10*time.Second)
		if err != nil {
			t.Fatalf("call %d after kill: %v", i, err)
		}
		want := tdfReferenceDiff(t, fx.repo, fx.tree[pair[0]], fx.tree[pair[1]])
		if g, w := tdfFull(got), tdfFull(want); !slices.Equal(g, w) {
			t.Errorf("call %d after kill: got %q want %q", i, g, w)
		}
	}
	if len(notified) != 1 {
		t.Errorf("onFallback fired %d times, want exactly 1: %v", len(notified), notified)
	}
}

// --- benchmark ---------------------------------------------------------------

// tdfSyntheticRepo builds a main branch of commits that each touch a few files
// in a tree of fileCount files, using one incremental fast-import so setup
// stays fast. It returns the (parent, child) tree pairs, root first.
func tdfSyntheticRepo(tb testing.TB, fileCount, commitCount int) (*tdfFixture, [][2]plumbing.Hash) {
	tb.Helper()
	dir := tb.TempDir()
	tdfGit(tb, dir, nil, "init", "-q")

	path := func(i int) string { return fmt.Sprintf("mod%02d/pkg%02d/file%03d.go", i%30, (i/30)%10, i/300) }
	rng := rand.New(rand.NewPCG(7, 11))

	var stream bytes.Buffer
	writeCommit := func(n int, build func()) {
		msg := fmt.Sprintf("commit %d", n)
		fmt.Fprintf(&stream, "commit refs/heads/main\ncommitter Test <test@example.com> %d +0000\ndata %d\n%s\n", 1_700_000_000+n, len(msg), msg)
		build()
		stream.WriteString("\n")
	}
	revision := make([]int, fileCount)
	live := make([]bool, fileCount)
	body := func(i int) tdfFile {
		return tdfReg(fmt.Sprintf("package p\n// file %d revision %d\n%s", i, revision[i], strings.Repeat("// padding line to give the blob some weight\n", 20)))
	}
	writeCommit(0, func() {
		for i := range fileCount {
			live[i] = true
			tdfWriteFile(&stream, path(i), body(i))
		}
	})
	for n := 1; n < commitCount; n++ {
		writeCommit(n, func() {
			for range 3 {
				i := rng.IntN(fileCount)
				if !live[i] {
					continue
				}
				revision[i]++
				tdfWriteFile(&stream, path(i), body(i))
			}
			if n%10 == 0 { // occasional delete and re-add keep inserts and deletes in the mix
				i := rng.IntN(fileCount)
				if live[i] {
					live[i] = false
					fmt.Fprintf(&stream, "D %s\n", path(i))
				}
			}
			if n%10 == 5 {
				for i := range fileCount {
					if !live[i] {
						live[i] = true
						revision[i]++
						tdfWriteFile(&stream, path(i), body(i))
						break
					}
				}
			}
		})
	}
	tdfGit(tb, dir, stream.Bytes(), "fast-import", "--quiet")

	trees := tdfGit(tb, dir, nil, "log", "--reverse", "--format=%T", "refs/heads/main")
	var pairs [][2]plumbing.Hash
	prev := plumbing.ZeroHash
	for line := range strings.SplitSeq(strings.TrimSpace(trees), "\n") {
		tree := plumbing.NewHash(line)
		pairs = append(pairs, [2]plumbing.Hash{prev, tree})
		prev = tree
	}
	if len(pairs) != commitCount {
		tb.Fatalf("synthetic repo has %d commits, want %d", len(pairs), commitCount)
	}
	repo, err := plainOpenTolerant(dir)
	if err != nil {
		tb.Fatalf("open synthetic repo: %v", err)
	}
	return &tdfFixture{gitDir: filepath.Join(dir, ".git"), repo: repo}, pairs
}

// BenchmarkTreeDiffers compares the go-git oracle with the streaming git differ
// over a full pass of a synthetic history (320 commits, 3000 files, ~3 files
// touched per commit). One op is the whole pass; ns/commit is the per-commit
// cost. The git differ's op includes starting its process. The root commit
// (3000 inserts, a one-off per repo) is verified but not timed, so the numbers
// describe steady-state incremental commits.
func BenchmarkTreeDiffers(b *testing.B) {
	const files, commits = 3000, 320
	fx, allPairs := tdfSyntheticRepo(b, files, commits)

	// a benchmark of two differs that disagree measures nothing
	check := tdfNewDiffer(b, fx.gitDir)
	for i, p := range allPairs {
		got, err := check.DiffTrees(context.Background(), p[0], p[1])
		if err != nil {
			b.Fatalf("commit %d: DiffTrees: %v", i, err)
		}
		if g, w := tdfFull(got), tdfFull(tdfReferenceDiff(b, fx.repo, p[0], p[1])); !slices.Equal(g, w) {
			b.Fatalf("commit %d: git differ and go-git disagree\n got: %q\nwant: %q", i, g, w)
		}
	}

	pairs := allPairs[1:]

	b.Run("gogit", func(b *testing.B) {
		b.ReportAllocs()
		var total int
		for b.Loop() {
			for _, p := range pairs {
				total += len(tdfReferenceDiff(b, fx.repo, p[0], p[1]))
			}
		}
		b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(pairs)), "ns/commit")
		if total == 0 {
			b.Fatal("no changes diffed")
		}
	})

	b.Run("git", func(b *testing.B) {
		b.ReportAllocs()
		var total int
		for b.Loop() {
			d, err := newGitTreeDiffer(context.Background(), fx.gitDir)
			if err != nil {
				b.Fatalf("newGitTreeDiffer: %v", err)
			}
			for _, p := range pairs {
				changes, err := d.DiffTrees(context.Background(), p[0], p[1])
				if err != nil {
					b.Fatalf("DiffTrees: %v", err)
				}
				total += len(changes)
			}
			if err := d.Close(); err != nil {
				b.Fatalf("Close: %v", err)
			}
		}
		b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(pairs)), "ns/commit")
		if total == 0 {
			b.Fatal("no changes diffed")
		}
	})
}
