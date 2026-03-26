package checks

import (
	"context"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/doctor"
	"github.com/sageox/ox/internal/gitutil"
)

// SageoxDirectoryCheck verifies that the .sageox/ directory exists in the git root.
type SageoxDirectoryCheck struct {
	git gitutil.GitRunner
}

// NewSageoxDirectoryCheck creates a new SageoxDirectoryCheck.
func NewSageoxDirectoryCheck(git gitutil.GitRunner) *SageoxDirectoryCheck {
	return &SageoxDirectoryCheck{git: git}
}

func (c *SageoxDirectoryCheck) Name() string {
	return ".sageox/ directory"
}

func (c *SageoxDirectoryCheck) Category() string {
	return "Project Structure"
}

func (c *SageoxDirectoryCheck) Run(ctx context.Context, _ bool) doctor.CheckResult {
	gitRoot, err := c.git.RunGit(ctx, ".", "rev-parse", "--show-toplevel")
	if err != nil {
		return doctor.CheckResult{
			Name:    c.Name(),
			Status:  doctor.StatusSkip,
			Message: "not in git repo",
		}
	}

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	info, err := os.Stat(sageoxDir)
	if err == nil && info.IsDir() {
		return doctor.CheckResult{
			Name:    c.Name(),
			Status:  doctor.StatusPass,
			Message: "",
		}
	}

	return doctor.CheckResult{
		Name:    c.Name(),
		Status:  doctor.StatusFail,
		Message: "not found",
		Fix:     "Run `ox init` to create it",
	}
}

// compile-time interface check
var _ doctor.Check = (*SageoxDirectoryCheck)(nil)
