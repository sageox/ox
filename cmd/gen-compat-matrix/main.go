// gen-compat-matrix generates an HTML compatibility matrix and GFM markdown
// from ox integration test results.
//
// Usage:
//
//	gen-compat-matrix --input test-results/ --html matrix.html
//	gen-compat-matrix --input test-results/ --gfm
//	gen-compat-matrix --input test-results/ --format github-actions
//	gen-compat-matrix resolve --agents claude-code,codex --versions 2
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
)

func main() {
	log.SetFlags(0) // no timestamp prefix in warnings
	log.SetPrefix("gen-compat-matrix: ")

	args := os.Args[1:]

	// Subcommand: resolve
	if len(args) > 0 && args[0] == "resolve" {
		if err := runResolve(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	if err := runGenerate(args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// runGenerate handles the default matrix generation path.
func runGenerate(args []string) error {
	cfg := struct {
		input  string
		html   string // empty = stdout
		gfm    bool
		format string // "github-actions" emits CI matrix JSON
	}{
		input: "test-results",
	}

	// Minimal flag parsing — stdlib only, no external deps.
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--gfm":
			cfg.gfm = true
		case strings.HasPrefix(arg, "--input="):
			cfg.input = strings.TrimPrefix(arg, "--input=")
		case arg == "--input" && i+1 < len(args):
			i++
			cfg.input = args[i]
		case strings.HasPrefix(arg, "--html="):
			cfg.html = strings.TrimPrefix(arg, "--html=")
		case arg == "--html" && i+1 < len(args):
			i++
			cfg.html = args[i]
		case strings.HasPrefix(arg, "--format="):
			cfg.format = strings.TrimPrefix(arg, "--format=")
		case arg == "--format" && i+1 < len(args):
			i++
			cfg.format = args[i]
		case arg == "--help" || arg == "-h":
			printUsage()
			return nil
		default:
			return fmt.Errorf("unknown flag %q (use --help for usage)", arg)
		}
	}

	ld, err := loadData(cfg.input)
	if err != nil {
		return fmt.Errorf("load data: %w", err)
	}

	if cfg.format == "github-actions" {
		return outputCIMatrix(ld)
	}

	if cfg.gfm {
		return writeGFM(os.Stdout, ld)
	}

	// Default: HTML output.
	if cfg.html == "" || cfg.html == "-" {
		return writeHTML(os.Stdout, ld)
	}

	f, err := os.Create(cfg.html)
	if err != nil {
		return fmt.Errorf("create %s: %w", cfg.html, err)
	}
	defer f.Close()
	if err := writeHTML(f, ld); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", cfg.html)
	return nil
}

// CIMatrixEntry is one entry in a GitHub Actions dynamic matrix.
type CIMatrixEntry struct {
	Agent        string `json:"agent"`
	Version      string `json:"version"`
	AgentPattern string `json:"agent_pattern"`
}

// CIMatrix is the JSON structure consumed by GitHub Actions `fromJSON()`.
type CIMatrix struct {
	Include []CIMatrixEntry `json:"include"`
}

// outputCIMatrix emits a GitHub Actions matrix JSON from the latest known version
// per agent (one version per agent for simplicity — extend with --versions N as needed).
func outputCIMatrix(ld *LoadedData) error {
	matrix := CIMatrix{}
	for _, ak := range ld.AgentOrder {
		run, ok := ld.LatestRunByAgent[ak]
		if !ok {
			continue
		}
		ai := ld.Compat.Agents[ak]
		pattern := agentPattern(ai.DisplayName)
		matrix.Include = append(matrix.Include, CIMatrixEntry{
			Agent:        ak,
			Version:      run.AgentVersion,
			AgentPattern: pattern,
		})
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(matrix)
}

// agentPattern converts a display name to a Go test pattern (e.g., "Claude Code" → "ClaudeCode").
func agentPattern(displayName string) string {
	var b strings.Builder
	for _, word := range strings.Fields(displayName) {
		if len(word) > 0 {
			b.WriteString(strings.ToUpper(word[:1]))
			b.WriteString(word[1:])
		}
	}
	return b.String()
}

// ResolveConfig holds flags for the resolve subcommand.
type ResolveConfig struct {
	agents   []string
	versions int
}

// runResolve queries for the last N versions of specified agents and outputs
// a GitHub Actions matrix JSON. In practice this would call agent-versions.sh
// or a registry API; here we emit a static example shape for CI to consume.
func runResolve(args []string) error {
	cfg := ResolveConfig{versions: 2}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case strings.HasPrefix(arg, "--agents="):
			for _, a := range strings.Split(strings.TrimPrefix(arg, "--agents="), ",") {
				if a = strings.TrimSpace(a); a != "" {
					cfg.agents = append(cfg.agents, a)
				}
			}
		case arg == "--agents" && i+1 < len(args):
			i++
			for _, a := range strings.Split(args[i], ",") {
				if a = strings.TrimSpace(a); a != "" {
					cfg.agents = append(cfg.agents, a)
				}
			}
		case strings.HasPrefix(arg, "--versions="):
			fmt.Sscanf(strings.TrimPrefix(arg, "--versions="), "%d", &cfg.versions)
		case arg == "--versions" && i+1 < len(args):
			i++
			fmt.Sscanf(args[i], "%d", &cfg.versions)
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(os.Stderr, "usage: gen-compat-matrix resolve [--agents agent1,agent2] [--versions N]")
			return nil
		default:
			return fmt.Errorf("unknown flag %q", arg)
		}
	}

	// Example static output shape. In CI this would call agent-versions.sh per agent.
	// The shape matches what GitHub Actions fromJSON() expects.
	entries := resolveAgentVersions(cfg)

	matrix := CIMatrix{Include: entries}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(matrix)
}

// resolveAgentVersions returns stub entries. Replace with real version lookups
// (e.g., npm view <pkg> versions, or scripts/agent-versions.sh) as needed.
func resolveAgentVersions(cfg ResolveConfig) []CIMatrixEntry {
	// Static example versions per agent — CI should replace this with dynamic resolution.
	knownVersions := map[string][]string{
		"claude-code": {"1.2.3", "1.2.2"},
		"codex":       {"0.1.5", "0.1.4"},
		"gemini":      {"0.1.2", "0.1.1"},
		"amp":         {"0.2.1", "0.2.0"},
		"opencode":    {"0.1.3", "0.1.2"},
		"continue":    {"0.9.1", "0.9.0"},
		"aider":       {"0.52.0", "0.51.0"},
	}

	agentDisplayNames := map[string]string{
		"claude-code": "Claude Code",
		"codex":       "Codex CLI",
		"gemini":      "Gemini CLI",
		"amp":         "Amp CLI",
		"opencode":    "OpenCode",
		"continue":    "Continue",
		"aider":       "Aider",
	}

	targets := cfg.agents
	if len(targets) == 0 {
		for k := range knownVersions {
			targets = append(targets, k)
		}
	}

	var entries []CIMatrixEntry
	for _, agent := range targets {
		versions, ok := knownVersions[agent]
		if !ok {
			continue
		}
		count := cfg.versions
		if count > len(versions) {
			count = len(versions)
		}
		pattern := agentPattern(agentDisplayNames[agent])
		for _, v := range versions[:count] {
			entries = append(entries, CIMatrixEntry{
				Agent:        agent,
				Version:      v,
				AgentPattern: pattern,
			})
		}
	}
	return entries
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `gen-compat-matrix — generate ox compatibility matrix

Usage:
  gen-compat-matrix [flags]
  gen-compat-matrix resolve [flags]

Generate flags:
  --input  DIR    path to test-results/ directory (default: test-results)
  --html   FILE   write HTML output to FILE (default: stdout, use - for stdout)
  --gfm           write GFM markdown to stdout (for GitHub Actions step summary)
  --format FORMAT output format; "github-actions" emits CI matrix JSON

Resolve flags (subcommand):
  --agents  LIST  comma-separated agent names to resolve versions for
  --versions N    number of recent versions per agent (default: 2)

Examples:
  gen-compat-matrix --input test-results/ --html matrix.html
  gen-compat-matrix --input test-results/ --gfm >> "$GITHUB_STEP_SUMMARY"
  gen-compat-matrix --input test-results/ --format github-actions
  gen-compat-matrix resolve --agents claude-code,codex --versions 3`)
}
