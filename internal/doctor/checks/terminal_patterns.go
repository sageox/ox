package checks

import (
	"context"

	"github.com/sageox/ox/internal/doctor"
	"github.com/sageox/ox/pkg/adapterruntime"
)

// terminalFrameworkProbe is the YAML body used to verify the YAML catalog
// loader is wired correctly. An empty patterns list compiles cleanly when
// every step (YAML parsing, version gate, registry resolution) works.
const terminalFrameworkProbe = "version: 1\npatterns: []\n"

// TerminalPatternsCheck is an advisory doctor check that confirms the
// adapter terminal-pattern framework is callable from the ox process.
// It does NOT enumerate per-adapter pattern catalogs — those live inside
// adapter binaries and are loaded at adapter startup, not from ox itself.
//
// The check is intentionally cheap and unconditional: it always returns
// pass in a healthy build. A failure here means the YAML loader or the
// adapterruntime package itself has regressed, which is a build-level
// bug rather than a user environment issue.
type TerminalPatternsCheck struct{}

// NewTerminalPatternsCheck creates a TerminalPatternsCheck.
func NewTerminalPatternsCheck() *TerminalPatternsCheck {
	return &TerminalPatternsCheck{}
}

// Name returns the check identifier.
func (c *TerminalPatternsCheck) Name() string {
	return "terminal-patterns framework"
}

// Category groups the check under the ecosystem family.
func (c *TerminalPatternsCheck) Category() string {
	return "Ecosystem"
}

// Run probes the YAML pattern loader and reports whether the framework
// is reachable. Always informational — never blocks `ox doctor --fix`.
func (c *TerminalPatternsCheck) Run(_ context.Context, _ bool) doctor.CheckResult {
	patterns, err := adapterruntime.LoadYAMLPatterns(
		[]byte(terminalFrameworkProbe),
		map[string]adapterruntime.StructuredCheck{},
		nil,
	)
	if err != nil {
		return doctor.CheckResult{
			Name:    c.Name(),
			Status:  doctor.StatusWarn,
			Message: "loader probe failed: " + err.Error(),
		}
	}
	if len(patterns) != 0 {
		return doctor.CheckResult{
			Name:    c.Name(),
			Status:  doctor.StatusWarn,
			Message: "loader probe returned unexpected patterns",
		}
	}
	return doctor.CheckResult{
		Name:    c.Name(),
		Status:  doctor.StatusPass,
		Message: "ok",
	}
}

// compile-time interface check
var _ doctor.Check = (*TerminalPatternsCheck)(nil)
