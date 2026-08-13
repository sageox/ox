package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestVizCommandCanonicalAndCompatibilitySurfaces(t *testing.T) {
	if vizCmd.Hidden {
		t.Error("top-level ox viz must be visible")
	}
	if !planVizCmd.Hidden {
		t.Error("ox plan viz compatibility command must stay hidden")
	}
	for _, cmd := range []*cobra.Command{vizCmd, planVizCmd} {
		for _, name := range []string{"suggest", "render", "lint"} {
			if child, _, err := cmd.Find([]string{name}); err != nil || child.Name() != name {
				t.Errorf("%s is missing %s: child=%v err=%v", cmd.CommandPath(), name, child, err)
			}
		}
	}
}

func TestVizListOneSuggestAndJSON(t *testing.T) {
	cmd, out := vizTestCommand()
	if err := runVizList(cmd, false); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "architecture") || !strings.Contains(got, "ox viz suggest") {
		t.Fatalf("catalog list is not actionable: %s", got)
	}

	cmd, out = vizTestCommand()
	if err := runVizOne(cmd, "architecture", true); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"id": "architecture"`, `"use":`, `"why":`, `"body":`,
		`"category": "diagram"`, `"authoring": "inline-svg"`,
		`"origin": "cathrynlavery/diagram-design@f3622cf"`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("pattern JSON missing %s: %s", want, out.String())
		}
	}

	cmd, out = vizTestCommand()
	if err := runVizSuggest(cmd, "branching validation gates fallback", 1, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "flowchart") || !strings.Contains(out.String(), "ox viz flowchart") {
		t.Fatalf("suggestion is not actionable: %s", out.String())
	}
}

func TestVizLintAdvisoryAndStrictModes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "diagram.svg")
	fragment := `<svg data-ox-viz="example" role="img" aria-labelledby="t d"><title id="t">Example</title><desc id="d">Example diagram</desc></svg>`
	if err := os.WriteFile(path, []byte(fragment), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd, _ := vizTestCommand()
	if err := runVizLint(cmd, path, false, false); err != nil {
		t.Fatalf("warnings must be advisory by default: %v", err)
	}
	cmd, _ = vizTestCommand()
	if err := runVizLint(cmd, path, true, false); err == nil {
		t.Fatal("strict mode must fail on editorial warnings")
	}
}

func vizTestCommand() (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	return cmd, out
}
