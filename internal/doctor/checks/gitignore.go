package checks

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/doctor"
	"github.com/sageox/ox/internal/gitutil"
)

// FileReader abstracts filesystem reads for testability.
type FileReader interface {
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte, perm os.FileMode) error
}

// OSFileReader is the production implementation using os package.
type OSFileReader struct{}

func (r *OSFileReader) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (r *OSFileReader) WriteFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}

// sageoxIgnorePatterns are patterns that indicate .sageox/ is being ignored.
var sageoxIgnorePatterns = []string{".sageox", ".sageox/", "/.sageox", "/.sageox/"}

// GitignoreCheck ensures .sageox/ is not listed in the root .gitignore.
type GitignoreCheck struct {
	git gitutil.GitRunner
	fs  FileReader
}

// NewGitignoreCheck creates a new GitignoreCheck.
// If fs is nil, the real OS filesystem is used.
func NewGitignoreCheck(git gitutil.GitRunner, fs FileReader) *GitignoreCheck {
	if fs == nil {
		fs = &OSFileReader{}
	}
	return &GitignoreCheck{git: git, fs: fs}
}

func (c *GitignoreCheck) Name() string {
	return ".gitignore"
}

func (c *GitignoreCheck) Category() string {
	return "Git Status"
}

func (c *GitignoreCheck) Run(ctx context.Context, fix bool) doctor.CheckResult {
	gitRoot, err := c.git.RunGit(ctx, ".", "rev-parse", "--show-toplevel")
	if err != nil {
		return doctor.CheckResult{
			Name:    c.Name(),
			Status:  doctor.StatusSkip,
			Message: "not in git repo",
		}
	}

	gitignorePath := filepath.Join(gitRoot, ".gitignore")
	content, err := c.fs.ReadFile(gitignorePath)
	if err != nil {
		return doctor.CheckResult{
			Name:    c.Name(),
			Status:  doctor.StatusSkip,
			Message: "no .gitignore",
		}
	}

	lines := strings.Split(string(content), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isSageoxIgnoreLine(trimmed) {
			if fix {
				newLines := append(lines[:i], lines[i+1:]...)
				newContent := strings.Join(newLines, "\n")
				if err := c.fs.WriteFile(gitignorePath, []byte(newContent), 0644); err != nil {
					return doctor.CheckResult{
						Name:    c.Name(),
						Status:  doctor.StatusFail,
						Message: "fix failed",
						Fix:     err.Error(),
					}
				}
				return doctor.CheckResult{
					Name:    c.Name(),
					Status:  doctor.StatusPass,
					Message: "fixed",
				}
			}
			return doctor.CheckResult{
				Name:    c.Name(),
				Status:  doctor.StatusFail,
				Message: ".sageox/ is ignored",
				Fix:     "Remove from .gitignore to track conventions",
			}
		}
	}

	return doctor.CheckResult{
		Name:    c.Name(),
		Status:  doctor.StatusPass,
		Message: "not ignored",
	}
}

func isSageoxIgnoreLine(line string) bool {
	for _, pattern := range sageoxIgnorePatterns {
		if line == pattern {
			return true
		}
	}
	return false
}

// compile-time interface check
var _ doctor.Check = (*GitignoreCheck)(nil)
