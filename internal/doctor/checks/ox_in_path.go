package checks

import (
	"context"
	"os/exec"
	"path/filepath"

	"github.com/sageox/ox/internal/doctor"
)

// LookPathFunc abstracts exec.LookPath for testability.
type LookPathFunc func(file string) (string, error)

// OxInPathCheck verifies that the ox binary is accessible via PATH.
type OxInPathCheck struct {
	lookPath LookPathFunc
}

// NewOxInPathCheck creates a new OxInPathCheck.
// If lookPath is nil, exec.LookPath is used.
func NewOxInPathCheck(lookPath LookPathFunc) *OxInPathCheck {
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	return &OxInPathCheck{lookPath: lookPath}
}

func (c *OxInPathCheck) Name() string {
	return "ox in PATH"
}

func (c *OxInPathCheck) Category() string {
	return "Ecosystem"
}

func (c *OxInPathCheck) Run(_ context.Context, _ bool) doctor.CheckResult {
	path, err := c.lookPath("ox")
	if err != nil {
		return doctor.CheckResult{
			Name:    c.Name(),
			Status:  doctor.StatusWarn,
			Message: "not found",
			Fix:     "Add ox to PATH for global access",
		}
	}
	dir := filepath.Base(filepath.Dir(path))
	return doctor.CheckResult{
		Name:    c.Name(),
		Status:  doctor.StatusPass,
		Message: dir,
	}
}

// compile-time interface check
var _ doctor.Check = (*OxInPathCheck)(nil)
