package checks

import (
	"github.com/sageox/ox/internal/doctor"
	"github.com/sageox/ox/internal/gitutil"
)

// Deps holds shared dependencies injected into doctor checks.
type Deps struct {
	Git      gitutil.GitRunner
	FS       FileReader
	LookPath LookPathFunc
}

// All returns every registered doctor.Check with the given dependencies.
// This is the single entry point for constructing checks — cmd/ox/doctor.go
// calls All() instead of constructing each check individually, keeping the
// wiring O(1) as new checks are added.
func All(d Deps) []doctor.Check {
	return []doctor.Check{
		NewSageoxDirectoryCheck(d.Git),
		NewGitignoreCheck(d.Git, d.FS),
		NewOxInPathCheck(d.LookPath),
		NewTerminalPatternsCheck(),
	}
}

// ByName returns a map of check name -> check for fast lookup.
// Useful when cmd/ox needs to run a specific check by slug.
func ByName(d Deps) map[string]doctor.Check {
	all := All(d)
	m := make(map[string]doctor.Check, len(all))
	for _, c := range all {
		m[c.Name()] = c
	}
	return m
}
