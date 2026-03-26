package main

import (
	"os"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
)

func TestDirExists_ExistingDir(t *testing.T) {
	t.Parallel()
	assert.True(t, dirExists(t.TempDir()))
}

func TestDirExists_NonExistentPath(t *testing.T) {
	t.Parallel()
	assert.False(t, dirExists("/nonexistent/path/xyz"))
}

func TestDirExists_FileNotDir(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	f := tmp + "/file.txt"
	os.WriteFile(f, []byte("content"), 0o644)
	assert.False(t, dirExists(f), "file should not count as directory")
}

func TestShouldHeartbeat_SkipsVersionHelp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cmdName  string
		expected bool
	}{
		{"version", "version", false},
		{"help", "help", false},
		{"completion", "completion", false},
		{"init", "init", true},
		{"doctor", "doctor", true},
		{"status", "status", true},
		{"sync", "sync", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// create a command hierarchy: root -> cmd
			root := &cobra.Command{Use: "ox"}
			child := &cobra.Command{Use: tt.cmdName}
			root.AddCommand(child)
			assert.Equal(t, tt.expected, shouldHeartbeat(child))
		})
	}
}

func TestShouldHeartbeat_NestedSubcommand(t *testing.T) {
	t.Parallel()

	// root -> agent -> prime (should heartbeat because "agent" is the top-level)
	root := &cobra.Command{Use: "ox"}
	agent := &cobra.Command{Use: "agent"}
	prime := &cobra.Command{Use: "prime"}
	root.AddCommand(agent)
	agent.AddCommand(prime)

	assert.True(t, shouldHeartbeat(prime))
}

func TestFormatDescription_WithStepMarker(t *testing.T) {
	t.Parallel()

	result := formatDescription("Authenticate with SageOx (Step 1)")
	// should contain the step marker text (styled, but present)
	assert.Contains(t, result, "Step 1")
	assert.Contains(t, result, "Authenticate")
}

func TestFormatDescription_WithoutStepMarker(t *testing.T) {
	t.Parallel()

	result := formatDescription("Check setup and sync status")
	assert.Contains(t, result, "Check setup")
}

func TestFormatUsage_NoFilePaths(t *testing.T) {
	t.Parallel()

	result := formatUsage("enable verbose output")
	assert.Contains(t, result, "enable verbose output")
}

func TestFormatUsage_WithFilePath(t *testing.T) {
	t.Parallel()

	result := formatUsage("config file path (default: .sageox/config.yaml)")
	// the path should appear in the output (with styling)
	assert.Contains(t, result, ".sageox/config.yaml")
}

func TestFormatUsage_MultipleFilePaths(t *testing.T) {
	t.Parallel()

	result := formatUsage("reads from ./input and writes to ~/output")
	assert.Contains(t, result, "./input")
	assert.Contains(t, result, "~/output")
}

func TestFlagValueLabel_WithAnnotation(t *testing.T) {
	t.Parallel()

	flag := &pflag.Flag{
		Name:        "config",
		Annotations: map[string][]string{"cobra_annotation_flag_value_name": {"file"}},
		Value:       newStringValue(""),
	}
	assert.Equal(t, "file", flagValueLabel(flag))
}

func TestFlagValueLabel_FallbackToType(t *testing.T) {
	t.Parallel()

	flag := &pflag.Flag{
		Name:  "verbose",
		Value: newStringValue(""),
	}
	assert.Equal(t, "string", flagValueLabel(flag))
}

func TestFlagValueLabel_EmptyAnnotation(t *testing.T) {
	t.Parallel()

	flag := &pflag.Flag{
		Name:        "config",
		Annotations: map[string][]string{"cobra_annotation_flag_value_name": {}},
		Value:       newStringValue(""),
	}
	// empty annotation slice falls back to type
	assert.Equal(t, "string", flagValueLabel(flag))
}

func TestStepPattern_Matches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		match string
	}{
		{"Authenticate (Step 1)", "(Step 1)"},
		{"Initialize repo (Step 2)", "(Step 2)"},
		{"No step here", ""},
		{"(Step 10) at start", "(Step 10)"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got := stepPattern.FindString(tt.input)
			assert.Equal(t, tt.match, got)
		})
	}
}

func TestFilePathPattern_Matches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input   string
		matches []string
	}{
		{".sageox/config.yaml", []string{".sageox/config.yaml"}},
		{"./foo/bar", []string{"./foo/bar"}},
		{"~/config", []string{"~/config"}},
		{"/absolute/path", []string{"/absolute/path"}},
		{"no paths here", nil},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got := filePathPattern.FindAllString(tt.input, -1)
			if tt.matches == nil {
				assert.Empty(t, got)
			} else {
				assert.Equal(t, tt.matches, got)
			}
		})
	}
}

func TestCommandHighlight_Struct(t *testing.T) {
	t.Parallel()

	h := commandHighlight{hasStar: true, step: 1, reason: "test"}
	assert.True(t, h.hasStar)
	assert.Equal(t, 1, h.step)
	assert.Equal(t, "test", h.reason)
}

// stringValue implements pflag.Value for testing
type stringValue struct {
	val string
}

func newStringValue(val string) *stringValue {
	return &stringValue{val: val}
}

func (s *stringValue) String() string   { return s.val }
func (s *stringValue) Set(v string) error { s.val = v; return nil }
func (s *stringValue) Type() string     { return "string" }

// writeTestFile already declared in helpers_coverage_test.go
