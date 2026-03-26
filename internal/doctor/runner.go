package doctor

import "context"

// Runner executes a collection of checks and aggregates results.
type Runner struct {
	checks []Check
}

// NewRunner creates a new check runner.
func NewRunner() *Runner {
	return &Runner{
		checks: make([]Check, 0),
	}
}

// Register adds a check to the runner.
func (r *Runner) Register(c Check) {
	r.checks = append(r.checks, c)
}

// RunAll executes all registered checks and returns their results.
func (r *Runner) RunAll(ctx context.Context, fix bool) []CheckResult {
	results := make([]CheckResult, 0, len(r.checks))
	for _, c := range r.checks {
		results = append(results, c.Run(ctx, fix))
	}
	return results
}
