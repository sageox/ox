package checks

import (
	"context"
	"testing"

	"github.com/sageox/ox/internal/doctor"
)

// TestTerminalPatternsCheck_HappyPath verifies the framework probe
// returns pass in a healthy build. Failure prevented: a build-time
// regression in the YAML loader silently masquerades as "ok" if this
// check did not actually exercise LoadYAMLPatterns.
func TestTerminalPatternsCheck_HappyPath(t *testing.T) {
	c := NewTerminalPatternsCheck()
	res := c.Run(context.Background(), false)

	if res.Name != c.Name() {
		t.Fatalf("Name mismatch: got %q want %q", res.Name, c.Name())
	}
	if res.Status != doctor.StatusPass {
		t.Fatalf("expected StatusPass, got %q (msg=%q)", res.Status, res.Message)
	}
}

// TestTerminalPatternsCheck_MetadataStable guards against accidental
// renames that would break operator dashboards keyed on check name.
func TestTerminalPatternsCheck_MetadataStable(t *testing.T) {
	c := NewTerminalPatternsCheck()
	if c.Name() != "terminal-patterns framework" {
		t.Fatalf("check name drifted: got %q", c.Name())
	}
	if c.Category() != "Ecosystem" {
		t.Fatalf("check category drifted: got %q", c.Category())
	}
}
