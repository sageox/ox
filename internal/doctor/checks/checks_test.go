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

func TestOxInPathCheck_Found(t *testing.T) {
	check := NewOxInPathCheck(func(file string) (string, error) {
		return "/usr/local/bin/ox", nil
	})

	result := check.Run(context.Background(), false)

	assert.Equal(t, doctor.StatusPass, result.Status)
	assert.Equal(t, "bin", result.Message)
	assert.Equal(t, "ox in PATH", result.Name)
	assert.Equal(t, "Ecosystem", check.Category())
}

func TestOxInPathCheck_NotFound(t *testing.T) {
	check := NewOxInPathCheck(func(file string) (string, error) {
		return "", errors.New("not found")
	})

	result := check.Run(context.Background(), false)

	assert.Equal(t, doctor.StatusWarn, result.Status)
	assert.Equal(t, "not found", result.Message)
	assert.NotEmpty(t, result.Fix)
}

func TestOxInPathCheck_GoPath(t *testing.T) {
	check := NewOxInPathCheck(func(file string) (string, error) {
		return "/home/user/go/bin/ox", nil
	})

	result := check.Run(context.Background(), false)

	assert.Equal(t, doctor.StatusPass, result.Status)
	assert.Equal(t, "bin", result.Message)
}

// --- SageoxDirectoryCheck tests ---

func TestSageoxDirectoryCheck_NotInGitRepo(t *testing.T) {
	git := &mockGitRunner{
		results: map[string]mockGitResult{
			"[rev-parse --show-toplevel]": {err: errors.New("not a git repo")},
		},
	}
	check := NewSageoxDirectoryCheck(git)

	result := check.Run(context.Background(), false)

	assert.Equal(t, doctor.StatusSkip, result.Status)
	assert.Equal(t, "not in git repo", result.Message)
	assert.Equal(t, "Project Structure", check.Category())
}

func TestSageoxDirectoryCheck_DirExists(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(tmpDir+"/.sageox", 0755))

	git := &mockGitRunner{
		results: map[string]mockGitResult{
			"[rev-parse --show-toplevel]": {output: tmpDir},
		},
	}
	check := NewSageoxDirectoryCheck(git)

	result := check.Run(context.Background(), false)

	assert.Equal(t, doctor.StatusPass, result.Status)
}

func TestSageoxDirectoryCheck_DirMissing(t *testing.T) {
	tmpDir := t.TempDir()

	git := &mockGitRunner{
		results: map[string]mockGitResult{
			"[rev-parse --show-toplevel]": {output: tmpDir},
		},
	}
	check := NewSageoxDirectoryCheck(git)

	result := check.Run(context.Background(), false)

	assert.Equal(t, doctor.StatusFail, result.Status)
	assert.Equal(t, "not found", result.Message)
	assert.Contains(t, result.Fix, "ox init")
}

// --- GitignoreCheck tests ---

func TestGitignoreCheck_NotInGitRepo(t *testing.T) {
	git := &mockGitRunner{
		results: map[string]mockGitResult{
			"[rev-parse --show-toplevel]": {err: errors.New("not a git repo")},
		},
	}
	check := NewGitignoreCheck(git, newMockFS())

	result := check.Run(context.Background(), false)

	assert.Equal(t, doctor.StatusSkip, result.Status)
	assert.Equal(t, "not in git repo", result.Message)
	assert.Equal(t, "Git Status", check.Category())
}

func TestGitignoreCheck_NoGitignore(t *testing.T) {
	git := &mockGitRunner{
		results: map[string]mockGitResult{
			"[rev-parse --show-toplevel]": {output: "/repo"},
		},
	}
	check := NewGitignoreCheck(git, newMockFS())

	result := check.Run(context.Background(), false)

	assert.Equal(t, doctor.StatusSkip, result.Status)
	assert.Equal(t, "no .gitignore", result.Message)
}

func TestGitignoreCheck_SageoxNotIgnored(t *testing.T) {
	fs := newMockFS()
	fs.files["/repo/.gitignore"] = []byte("node_modules/\n*.log\n")

	git := &mockGitRunner{
		results: map[string]mockGitResult{
			"[rev-parse --show-toplevel]": {output: "/repo"},
		},
	}
	check := NewGitignoreCheck(git, fs)

	result := check.Run(context.Background(), false)

	assert.Equal(t, doctor.StatusPass, result.Status)
	assert.Equal(t, "not ignored", result.Message)
}

func TestGitignoreCheck_SageoxIgnored(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"bare", "node_modules/\n.sageox\n"},
		{"trailing slash", "node_modules/\n.sageox/\n"},
		{"leading slash", "node_modules/\n/.sageox\n"},
		{"leading and trailing slash", "node_modules/\n/.sageox/\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := newMockFS()
			fs.files["/repo/.gitignore"] = []byte(tt.content)

			git := &mockGitRunner{
				results: map[string]mockGitResult{
					"[rev-parse --show-toplevel]": {output: "/repo"},
				},
			}
			check := NewGitignoreCheck(git, fs)

			result := check.Run(context.Background(), false)

			assert.Equal(t, doctor.StatusFail, result.Status)
			assert.Equal(t, ".sageox/ is ignored", result.Message)
			assert.Contains(t, result.Fix, "Remove from .gitignore")
		})
	}
}

func TestGitignoreCheck_FixRemovesLine(t *testing.T) {
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

	// verify the written content has the .sageox/ line removed
	written := string(fs.written["/repo/.gitignore"])
	assert.NotContains(t, written, ".sageox/")
	assert.Contains(t, written, "node_modules/")
	assert.Contains(t, written, "*.log")
}

func TestGitignoreCheck_FixWriteError(t *testing.T) {
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
}

// --- Interface compliance ---

func TestInterfaceCompliance(t *testing.T) {
	// verify all checks implement doctor.Check
	var _ doctor.Check = (*OxInPathCheck)(nil)
	var _ doctor.Check = (*SageoxDirectoryCheck)(nil)
	var _ doctor.Check = (*GitignoreCheck)(nil)
}
