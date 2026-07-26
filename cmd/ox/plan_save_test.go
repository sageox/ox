package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// newPlanSaveTestCmd builds an isolated cobra command wired to runPlanSave with
// the same flags the real `ox plan save` registers. Isolated (not the global
// planSaveCmd) so flag state never leaks across tests.
func newPlanSaveTestCmd() *cobra.Command {
	cmd := &cobra.Command{
		RunE: func(c *cobra.Command, _ []string) error { return runPlanSave(c) },
	}
	cmd.Flags().String("plan", "", "")
	cmd.Flags().String("annotations", "", "")
	cmd.Flags().String("html", "", "")
	return cmd
}

func runPlanSaveWith(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newPlanSaveTestCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// TestPlanSave_RequiresPlanAndAnnotations verifies the cmd-level guards fire
// before any filesystem work. Failure prevented: a malformed skill invocation
// silently no-ops or panics instead of reporting the missing required input.
func TestPlanSave_RequiresPlanAndAnnotations(t *testing.T) {
	t.Run("missing --plan", func(t *testing.T) {
		_, err := runPlanSaveWith(t, "--annotations", "ann.json")
		if err == nil || !strings.Contains(err.Error(), "--file <plan.html|plan.md>") {
			t.Fatalf("want the pass---file guidance error, got %v", err)
		}
	})

	t.Run("missing --annotations", func(t *testing.T) {
		_, err := runPlanSaveWith(t, "--plan", "plan.md")
		if err == nil || !strings.Contains(err.Error(), "--annotations is required") {
			t.Fatalf("want --annotations required error, got %v", err)
		}
	})
}

// TestPlanSave_BadAnnotationsJSONErrors verifies a corrupt merged
// annotations.json surfaces a clear parse error rather than persisting a
// half-baked Result. The merged file is agent-authored, so malformed input is a
// realistic failure mode worth a precise message.
func TestPlanSave_BadAnnotationsJSONErrors(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.md")
	annPath := filepath.Join(dir, "ann.json")
	if err := os.WriteFile(planPath, []byte("## Plan\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(annPath, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := runPlanSaveWith(t, "--plan", planPath, "--annotations", annPath)
	if err == nil || !strings.Contains(err.Error(), "parse annotations") {
		t.Fatalf("want parse-annotations error, got %v", err)
	}
}

// TestPlanSave_MissingFilesError verifies missing input files are reported per
// flag, not swallowed. A skill passing a stale path must learn which file is
// absent.
func TestPlanSave_MissingFilesError(t *testing.T) {
	dir := t.TempDir()
	annPath := filepath.Join(dir, "ann.json")
	if err := os.WriteFile(annPath, []byte(`{"annotations":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := runPlanSaveWith(t, "--plan", filepath.Join(dir, "nope.md"), "--annotations", annPath)
	if err == nil {
		t.Fatal("want error for missing --plan file, got nil")
	}
}
