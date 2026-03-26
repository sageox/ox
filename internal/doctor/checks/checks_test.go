package checks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/sageox/ox/internal/doctor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockGitRunner implements gitutil.GitRunner for testing.
type mockGitRunner struct {
	results map[string]mockGitResult
}

type mockGitResult struct {
	output string
	err    error
}

func (m *mockGitRunner) RunGit(_ context.Context, _ string, args ...string) (string, error) {
	key := fmt.Sprintf("%v", args)
	if r, ok := m.results[key]; ok {
		return r.output, r.err
	}
	return "", fmt.Errorf("unexpected git call: %v", args)
}

// mockFS implements FileReader for testing.
type mockFS struct {
	files    map[string][]byte
	writeErr error
	written  map[string][]byte
}

func newMockFS() *mockFS {
	return &mockFS{
		files:   make(map[string][]byte),
		written: make(map[string][]byte),
	}
}

func (m *mockFS) ReadFile(path string) ([]byte, error) {
	if data, ok := m.files[path]; ok {
		return data, nil
	}
	return nil, os.ErrNotExist
}

func (m *mockFS) WriteFile(path string, data []byte, _ os.FileMode) error {
	if m.writeErr != nil {
		return m.writeErr
	}
	m.written[path] = data
	return nil
}

// --- OxInPathCheck tests ---

func TestOxInPathCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		lookPath       LookPathFunc
		wantStatus     doctor.Status
		wantMessage    string
		wantFixPresent bool
	}{
		{
			// prevents: user installed ox but gets cryptic errors because it's not in PATH
			name: "found in /usr/local/bin",
			lookPath: func(file string) (string, error) {
				assert.Equal(t, "ox", file)
				return "/usr/local/bin/ox", nil
			},
			wantStatus:  doctor.StatusPass,
			wantMessage: "bin",
		},
		{
			// prevents: user has ox in GOPATH but check fails to recognize it
			name: "found in go/bin",
			lookPath: func(file string) (string, error) {
				return "/home/user/go/bin/ox", nil
			},
			wantStatus:  doctor.StatusPass,
			wantMessage: "bin",
		},
		{
			// prevents: ox not installed at all; user gets no guidance
			name: "not found in PATH",
			lookPath: func(file string) (string, error) {
				return "", errors.New("executable file not found in $PATH")
			},
			wantStatus:     doctor.StatusWarn,
			wantMessage:    "not found",
			wantFixPresent: true,
		},
		{
			// prevents: custom install location not recognized
			name: "found in custom path",
			lookPath: func(file string) (string, error) {
				return "/opt/sageox/bin/ox", nil
			},
			wantStatus:  doctor.StatusPass,
			wantMessage: "bin",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			check := NewOxInPathCheck(tt.lookPath)

			result := check.Run(context.Background(), false)

			assert.Equal(t, tt.wantStatus, result.Status)
			assert.Equal(t, tt.wantMessage, result.Message)
			if tt.wantFixPresent {
				assert.NotEmpty(t, result.Fix)
			} else {
				assert.Empty(t, result.Fix)
			}
		})
	}
}

func TestOxInPathCheck_NameAndCategory(t *testing.T) {
	t.Parallel()
	check := NewOxInPathCheck(func(string) (string, error) { return "", nil })
	assert.Equal(t, "ox in PATH", check.Name())
	assert.Equal(t, "Ecosystem", check.Category())
}

func TestOxInPathCheck_NilLookPathUsesDefault(t *testing.T) {
	t.Parallel()
	// prevents: nil lookPath causes panic at runtime
	check := NewOxInPathCheck(nil)
	assert.NotNil(t, check.lookPath)
	// should not panic when run (uses real exec.LookPath)
	result := check.Run(context.Background(), false)
	assert.Contains(t, []doctor.Status{doctor.StatusPass, doctor.StatusWarn}, result.Status)
}

// --- SageoxDirectoryCheck tests ---

func TestSageoxDirectoryCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		gitErr      error
		gitRoot     string
		setupDir    func(t *testing.T, root string)
		wantStatus  doctor.Status
		wantMessage string
		wantFix     string
	}{
		{
			// prevents: running doctor outside a git repo causes panic instead of skip
			name:        "not in git repo",
			gitErr:      errors.New("fatal: not a git repository"),
			wantStatus:  doctor.StatusSkip,
			wantMessage: "not in git repo",
		},
		{
			// prevents: .sageox exists but check reports missing
			name: "directory exists",
			setupDir: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(root+"/.sageox", 0755))
			},
			wantStatus:  doctor.StatusPass,
			wantMessage: "",
		},
		{
			// prevents: git repo initialized but user forgot ox init; no guidance shown
			name:        "directory missing",
			setupDir:    func(t *testing.T, root string) {},
			wantStatus:  doctor.StatusFail,
			wantMessage: "not found",
			wantFix:     "ox init",
		},
		{
			// prevents: .sageox is a file (not directory) being treated as valid
			name: "sageox is a file not a directory",
			setupDir: func(t *testing.T, root string) {
				require.NoError(t, os.WriteFile(root+"/.sageox", []byte("oops"), 0644))
			},
			wantStatus:  doctor.StatusFail,
			wantMessage: "not found",
			wantFix:     "ox init",
		},
		{
			// prevents: git returns empty root path causing path join issues
			name:        "empty git root",
			gitRoot:     "",
			setupDir:    nil,
			wantStatus:  doctor.StatusFail,
			wantMessage: "not found",
			wantFix:     "ox init",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var gitRoot string
			if tt.gitErr != nil {
				gitRoot = ""
			} else if tt.gitRoot != "" || tt.setupDir == nil {
				gitRoot = tt.gitRoot
			} else {
				gitRoot = t.TempDir()
				tt.setupDir(t, gitRoot)
			}

			results := map[string]mockGitResult{}
			if tt.gitErr != nil {
				results["[rev-parse --show-toplevel]"] = mockGitResult{err: tt.gitErr}
			} else {
				results["[rev-parse --show-toplevel]"] = mockGitResult{output: gitRoot}
			}

			git := &mockGitRunner{results: results}
			check := NewSageoxDirectoryCheck(git)

			result := check.Run(context.Background(), false)

			assert.Equal(t, tt.wantStatus, result.Status)
			assert.Equal(t, tt.wantMessage, result.Message)
			if tt.wantFix != "" {
				assert.Contains(t, result.Fix, tt.wantFix)
			}
		})
	}
}

func TestSageoxDirectoryCheck_NameAndCategory(t *testing.T) {
	t.Parallel()
	check := NewSageoxDirectoryCheck(&mockGitRunner{results: map[string]mockGitResult{}})
	assert.Equal(t, ".sageox/ directory", check.Name())
	assert.Equal(t, "Project Structure", check.Category())
}

// --- GitignoreCheck tests ---

func TestGitignoreCheck_NameAndCategory(t *testing.T) {
	t.Parallel()
	check := NewGitignoreCheck(&mockGitRunner{results: map[string]mockGitResult{}}, newMockFS())
	assert.Equal(t, ".gitignore", check.Name())
	assert.Equal(t, "Git Status", check.Category())
}

func TestGitignoreCheck_NilFSUsesDefault(t *testing.T) {
	t.Parallel()
	// prevents: nil fs causes panic at runtime
	git := &mockGitRunner{
		results: map[string]mockGitResult{
			"[rev-parse --show-toplevel]": {err: errors.New("not a git repo")},
		},
	}
	check := NewGitignoreCheck(git, nil)
	assert.NotNil(t, check.fs)
	// should not panic
	result := check.Run(context.Background(), false)
	assert.Equal(t, doctor.StatusSkip, result.Status)
}

func TestGitignoreCheck_Detection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		gitErr      error
		fileContent *string // nil = file doesn't exist
		wantStatus  doctor.Status
		wantMessage string
		wantFix     string
	}{
		{
			// prevents: running doctor outside git repo causes panic
			name:        "not in git repo",
			gitErr:      errors.New("not a git repo"),
			wantStatus:  doctor.StatusSkip,
			wantMessage: "not in git repo",
		},
		{
			// prevents: missing .gitignore misreported as a problem
			name:        "no gitignore file",
			fileContent: nil,
			wantStatus:  doctor.StatusSkip,
			wantMessage: "no .gitignore",
		},
		{
			// prevents: normal .gitignore falsely flagged
			name:        "sageox not in gitignore",
			fileContent: strPtr("node_modules/\n*.log\ndist/\n"),
			wantStatus:  doctor.StatusPass,
			wantMessage: "not ignored",
		},
		{
			// prevents: bare .sageox pattern not detected
			name:        "bare .sageox pattern",
			fileContent: strPtr("node_modules/\n.sageox\n*.log\n"),
			wantStatus:  doctor.StatusFail,
			wantMessage: ".sageox/ is ignored",
			wantFix:     "Remove from .gitignore",
		},
		{
			// prevents: .sageox/ with trailing slash not detected
			name:        "trailing slash pattern",
			fileContent: strPtr(".sageox/\n"),
			wantStatus:  doctor.StatusFail,
			wantMessage: ".sageox/ is ignored",
			wantFix:     "Remove from .gitignore",
		},
		{
			// prevents: /.sageox with leading slash not detected
			name:        "leading slash pattern",
			fileContent: strPtr("/.sageox\n"),
			wantStatus:  doctor.StatusFail,
			wantMessage: ".sageox/ is ignored",
			wantFix:     "Remove from .gitignore",
		},
		{
			// prevents: /.sageox/ with both slashes not detected
			name:        "leading and trailing slash pattern",
			fileContent: strPtr("/.sageox/\n"),
			wantStatus:  doctor.StatusFail,
			wantMessage: ".sageox/ is ignored",
			wantFix:     "Remove from .gitignore",
		},
		{
			// prevents: .sageox buried in large gitignore goes undetected
			name:        "sageox among many entries",
			fileContent: strPtr("node_modules/\ndist/\n.env\n.sageox\n*.log\ncoverage/\n.DS_Store\n"),
			wantStatus:  doctor.StatusFail,
			wantMessage: ".sageox/ is ignored",
		},
		{
			// prevents: commented-out .sageox line falsely flagged as active ignore
			name:        "commented out sageox line",
			fileContent: strPtr("node_modules/\n# .sageox\n*.log\n"),
			wantStatus:  doctor.StatusPass,
			wantMessage: "not ignored",
		},
		{
			// prevents: indented comment falsely flagged
			name:        "commented with leading space",
			fileContent: strPtr("  # .sageox\nnode_modules/\n"),
			wantStatus:  doctor.StatusPass,
			wantMessage: "not ignored",
		},
		{
			// prevents: empty gitignore treated as error
			name:        "empty gitignore",
			fileContent: strPtr(""),
			wantStatus:  doctor.StatusPass,
			wantMessage: "not ignored",
		},
		{
			// prevents: .sageox as first line not detected
			name:        "sageox as first line",
			fileContent: strPtr(".sageox\nnode_modules/\n"),
			wantStatus:  doctor.StatusFail,
			wantMessage: ".sageox/ is ignored",
		},
		{
			// prevents: similar-but-different pattern falsely flagged (e.g. .sageox-backup)
			name:        "similar pattern not falsely matched",
			fileContent: strPtr(".sageox-backup\n.sageoxrc\n"),
			wantStatus:  doctor.StatusPass,
			wantMessage: "not ignored",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fs := newMockFS()
			if tt.fileContent != nil {
				fs.files["/repo/.gitignore"] = []byte(*tt.fileContent)
			}

			results := map[string]mockGitResult{}
			if tt.gitErr != nil {
				results["[rev-parse --show-toplevel]"] = mockGitResult{err: tt.gitErr}
			} else {
				results["[rev-parse --show-toplevel]"] = mockGitResult{output: "/repo"}
			}

			git := &mockGitRunner{results: results}
			check := NewGitignoreCheck(git, fs)

			result := check.Run(context.Background(), false)

			assert.Equal(t, tt.wantStatus, result.Status, "status mismatch")
			assert.Equal(t, tt.wantMessage, result.Message, "message mismatch")
			if tt.wantFix != "" {
				assert.Contains(t, result.Fix, tt.wantFix)
			}
		})
	}
}

func TestGitignoreCheck_Fix(t *testing.T) {
	t.Parallel()

	t.Run("removes sageox line and preserves others", func(t *testing.T) {
		t.Parallel()
		// prevents: fix removes wrong lines or corrupts gitignore
		fs := newMockFS()
		fs.files["/repo/.gitignore"] = []byte("node_modules/\n.sageox/\n*.log\n")

		git := &mockGitRunner{
			results: map[string]mockGitResult{
				"[rev-parse --show-toplevel]": {output: "/repo"},
			},
		}
		check := NewGitignoreCheck(git, fs)

		result := check.Run(context.Background(), true)

		assert.Equal(t, doctor.StatusPass, result.Status)
		assert.Equal(t, "fixed", result.Message)

		written := string(fs.written["/repo/.gitignore"])
		assert.NotContains(t, written, ".sageox")
		assert.Contains(t, written, "node_modules/")
		assert.Contains(t, written, "*.log")
	})

	t.Run("write error returns fail with error detail", func(t *testing.T) {
		t.Parallel()
		// prevents: fix silently fails, user thinks issue is resolved
		fs := newMockFS()
		fs.files["/repo/.gitignore"] = []byte(".sageox/\n")
		fs.writeErr = errors.New("permission denied")

		git := &mockGitRunner{
			results: map[string]mockGitResult{
				"[rev-parse --show-toplevel]": {output: "/repo"},
			},
		}
		check := NewGitignoreCheck(git, fs)

		result := check.Run(context.Background(), true)

		assert.Equal(t, doctor.StatusFail, result.Status)
		assert.Equal(t, "fix failed", result.Message)
		assert.Contains(t, result.Fix, "permission denied")
	})

	t.Run("only sageox in file results in near-empty file", func(t *testing.T) {
		t.Parallel()
		// prevents: removing the only line leaves a malformed file
		fs := newMockFS()
		fs.files["/repo/.gitignore"] = []byte(".sageox\n")

		git := &mockGitRunner{
			results: map[string]mockGitResult{
				"[rev-parse --show-toplevel]": {output: "/repo"},
			},
		}
		check := NewGitignoreCheck(git, fs)

		result := check.Run(context.Background(), true)

		assert.Equal(t, doctor.StatusPass, result.Status)
		assert.Equal(t, "fixed", result.Message)

		written := string(fs.written["/repo/.gitignore"])
		assert.NotContains(t, written, ".sageox")
	})

	t.Run("no fix when sageox not present", func(t *testing.T) {
		t.Parallel()
		// prevents: fix=true on clean gitignore causes unnecessary write
		fs := newMockFS()
		fs.files["/repo/.gitignore"] = []byte("node_modules/\n")

		git := &mockGitRunner{
			results: map[string]mockGitResult{
				"[rev-parse --show-toplevel]": {output: "/repo"},
			},
		}
		check := NewGitignoreCheck(git, fs)

		result := check.Run(context.Background(), true)

		assert.Equal(t, doctor.StatusPass, result.Status)
		assert.Equal(t, "not ignored", result.Message)
		assert.Empty(t, fs.written, "should not write when nothing to fix")
	})
}

// strPtr returns a pointer to a string literal (test helper).
func strPtr(s string) *string { return &s }

// --- Interface compliance ---

func TestInterfaceCompliance(t *testing.T) {
	// verify all checks implement doctor.Check
	var _ doctor.Check = (*OxInPathCheck)(nil)
	var _ doctor.Check = (*SageoxDirectoryCheck)(nil)
	var _ doctor.Check = (*GitignoreCheck)(nil)
}
