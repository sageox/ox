package testguard

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// These guards catch the two mechanically-detectable members of the fail-open
// family documented in .claude/rules/testing.md: a test isolation mechanism whose
// semantics differ by platform, which therefore becomes a silent no-op on the
// platform it was not written for while the assertion still passes.
//
// They are deliberately narrow. Only patterns with a reliable textual signature
// and a real recorded consequence are enforced here; the rest is prose in the
// rule, because a noisy guard gets suppressed and then guards nothing.

// repoRootFromTestGuard walks up to the module root from this package.
func repoRootFromTestGuard(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found walking up from %s", dir)
		}
		dir = parent
	}
}

func eachTestFile(t *testing.T, fn func(path string, src string)) {
	t.Helper()
	root := repoRootFromTestGuard(t)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable tree entries are not this guard's business
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "dist", "tmp":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if filepath.Base(path) == "failopen_test.go" {
			return nil // this file quotes the patterns it forbids
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		fn(path, string(b))
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

var pathListJoin = regexp.MustCompile(`Setenv\(\s*"PATH"\s*,[^)]*\+\s*":"`)

// TestNoHardcodedPathListSeparator.
//
// `t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))` injects a fake binary on
// Unix and produces ONE MALFORMED PATH ENTRY on Windows, where the separator is
// ';'. The fake is then unreachable and the REAL binary runs — which is how a
// unit test came to run `git credential reject` against a developer's live
// credential store. Use string(os.PathListSeparator).
func TestNoHardcodedPathListSeparator(t *testing.T) {
	var offenders []string
	eachTestFile(t, func(path, src string) {
		if pathListJoin.MatchString(src) {
			offenders = append(offenders, path)
		}
	})
	if len(offenders) > 0 {
		t.Errorf("PATH built with a hardcoded \":\" separator in:\n  %s\n"+
			"On Windows the separator is ';', so the injected entry is malformed, the fake "+
			"binary is never found, and the REAL one runs. Use string(os.PathListSeparator).",
			strings.Join(offenders, "\n  "))
	}
}

// TestChmodBasedIsolationDeclaresItsPlatform.
//
// Go's os.Chmod on Windows maps only the read-only bit, so Chmod(0o000) does NOT
// make a file unreadable there: the read succeeds and a require.Error assertion
// fails while having exercised nothing. A test that removes ALL permission bits
// to force a failure must say which platforms that holds on.
//
// Only the 0o000 / 0000 form is flagged. Chmod to a specific mode is ordinary
// setup, not an isolation mechanism.
func TestChmodBasedIsolationDeclaresItsPlatform(t *testing.T) {
	var offenders []string
	eachTestFile(t, func(path, src string) {
		// A whole-file build tag covers every test in it.
		if strings.Contains(src, "//go:build unix") ||
			strings.Contains(src, "//go:build !windows") ||
			strings.Contains(src, "//go:build linux || darwin") {
			return
		}
		for _, fn := range chmodZeroFunctions(src) {
			offenders = append(offenders, path+": "+fn)
		}
	})
	if len(offenders) > 0 {
		t.Errorf("Chmod(0o000) used to force a failure, in a test that does not skip on Windows:\n  %s\n"+
			"Windows maps only the read-only bit, so the file stays readable and the test "+
			"passes while asserting nothing. Skip on runtime.GOOS == \"windows\" with a "+
			"comment, or use a portable lever (a directory where a file is expected).",
			strings.Join(offenders, "\n  "))
	}
}

var (
	chmodZero    = regexp.MustCompile(`os\.Chmod\([^)]*,\s*0o?000\s*\)`)
	funcDeclLine = regexp.MustCompile(`^func\s+(\w+)\s*\(`)
	windowsSkip  = regexp.MustCompile(`runtime\.GOOS\s*==\s*"windows"`)
)

// chmodZeroFunctions returns the names of top-level functions that call
// Chmod(0o000) without a Windows guard IN THAT FUNCTION.
//
// Attributing each call to its enclosing function is the point. A file-wide text
// match exempted every chmod in a file as soon as ONE unrelated test mentioned
// windows — so an unguarded test could still pass on Windows without ever
// creating the failure condition it asserts on. The rule in
// .claude/rules/testing.md puts the obligation on the test that uses the
// mechanism, not on its neighbors.
func chmodZeroFunctions(src string) []string {
	var out []string
	var current string
	var body []string

	flush := func() {
		if current == "" {
			return
		}
		joined := strings.Join(body, "\n")
		if chmodZero.MatchString(joined) && !windowsSkip.MatchString(joined) {
			out = append(out, current)
		}
	}

	for _, line := range strings.Split(src, "\n") {
		if m := funcDeclLine.FindStringSubmatch(line); m != nil {
			flush()
			current, body = m[1], nil
			continue
		}
		if current != "" {
			body = append(body, line)
		}
	}
	flush()
	return out
}
