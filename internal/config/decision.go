package config

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// DecisionConfig holds the `decision.*` settings namespace for `ox decision`.
// It configures WHERE this repo's Decision Records (DRs — ADRs are one type,
// DDRs another) live. Stored in the committed .sageox config so the whole team
// shares one answer; nil/empty means "unset" and discovery falls back to
// DefaultDecisionDirs.
//
// v1 is intentionally flat: authoritative DRs are committed files in THIS
// repo. Cross-repo corpora and https sources are a later release (and kb
// bubbles are dynamic memory — hints, never an authoritative DR source).
type DecisionConfig struct {
	// Paths are directories (scanned recursively for *.md) or doublestar
	// globs, relative to the repo root.
	Paths []string `yaml:"paths,omitempty" json:"paths,omitempty"`

	// Enrich toggles decision-record PRIMING: the <decision-record-guidance>
	// prime block and the decision hint in <code-search>. Default true (nil =
	// on). Off does NOT disable the `ox decision enrich` command itself — an
	// explicit invocation is explicit intent — nor the passive
	// doc_type:"decision" tagging on search results (metadata, not priming).
	Enrich *bool `yaml:"enrich,omitempty" json:"enrich,omitempty"`
}

// DefaultDecisionDirs are scanned (existing dirs only) when no decision config
// is set — the zero-config path that makes `ox decision` work day one in any
// repo that keeps ADRs in a conventional place.
var DefaultDecisionDirs = []string{
	"docs/adr",
	"docs/decisions",
	"adr",
	"docs/architecture/decisions",
}

// IsEmpty reports whether no decision path is explicitly configured.
func (c *DecisionConfig) IsEmpty() bool {
	return c == nil || len(c.Paths) == 0
}

// DecisionEnrichEnabled resolves whether decision-record priming is on for a
// project. Default TRUE — the committed `decision.enrich: false` is the
// explicit opt-out (mirrors the plan.* resolver shape, project-scope only).
func DecisionEnrichEnabled(projectRoot string) bool {
	if projectRoot == "" {
		return true
	}
	if cfg, _ := LoadProjectConfig(projectRoot); cfg != nil && cfg.Decision != nil && cfg.Decision.Enrich != nil {
		return *cfg.Decision.Enrich
	}
	return true
}

// ValidateDecisionConfig checks the decision block for the errors doctor and
// the loader surface. Nil config is valid (defaults apply).
func ValidateDecisionConfig(c *DecisionConfig) error {
	if c == nil {
		return nil
	}
	for i, p := range c.Paths {
		p = strings.TrimSpace(p)
		if p == "" {
			return fmt.Errorf("decision.paths[%d]: empty path entry", i)
		}
		if filepath.IsAbs(p) {
			return fmt.Errorf("decision.paths[%d]: %q must be relative to the repo root", i, p)
		}
		if p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "/../") {
			return fmt.Errorf("decision.paths[%d]: %q may not traverse above the repo root", i, p)
		}
		if !doublestar.ValidatePattern(filepath.ToSlash(p)) {
			return fmt.Errorf("decision.paths[%d]: %q is not a valid glob pattern", i, p)
		}
	}
	return nil
}
