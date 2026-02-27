package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/signing"
	"github.com/sageox/ox/internal/ui"
	"github.com/spf13/cobra"
)

var agentRedactCmd = &cobra.Command{
	Use:   "redact",
	Short: "Show the complete redaction policy from all sources",
	Long: `Display the complete redaction policy assembled from all 4 sources:
  1. builtin  - Cryptographically signed patterns for common secrets
  2. team     - Team-level patterns from team context REDACT.md
  3. repo     - Repo-level patterns from .sageox/REDACT.md
  4. user     - User-level patterns from ~/.config/sageox/REDACT.md

Default output is JSON for agent consumption. Use --text for human-readable output.

Examples:
  ox agent redact              # JSON output (all sources)
  ox agent redact --text       # human-readable output
  ox agent redact test "AKIA1234567890123456"   # test redaction on sample text
  echo "my secret" | ox agent redact test -     # test redaction from stdin`,
	RunE: runAgentRedactPolicy,
}

var agentRedactTestCmd = &cobra.Command{
	Use:   "test [text|-]",
	Short: "Test redaction on sample text",
	Long: `Apply all redaction rules (built-in + custom) to sample text.

Pass text as an argument, or use '-' to read from stdin.

Examples:
  ox agent redact test "AKIA1234567890123456"
  echo "password='hunter2'" | ox agent redact test -`,
	Args: cobra.ExactArgs(1),
	RunE: runAgentRedactTest,
}

func init() {
	agentRedactCmd.AddCommand(agentRedactTestCmd)
}

// --- JSON output types ---

type redactSourceJSON struct {
	Name            string               `json:"name"`
	Description     string               `json:"description,omitempty"`
	Path            string               `json:"path,omitempty"`
	PatternCount    int                  `json:"pattern_count"`
	SignatureStatus string               `json:"signature_status,omitempty"`
	Patterns        []redactPatternJSON  `json:"patterns"`
}

type redactPatternJSON struct {
	Name    string `json:"name,omitempty"`
	Regex   string `json:"regex,omitempty"`
	Redact  string `json:"redact"`
	Type    string `json:"type,omitempty"`
	Pattern string `json:"pattern,omitempty"`
	Line    int    `json:"line,omitempty"`
}

type redactPolicyJSON struct {
	Sources       []redactSourceJSON `json:"sources"`
	TotalPatterns int                `json:"total_patterns"`
	ParseErrors   []string           `json:"parse_errors"`
}

type redactTestJSON struct {
	Input    string              `json:"input"`
	Output   string              `json:"output"`
	Matches  []redactMatchJSON   `json:"matches"`
	Redacted bool                `json:"redacted"`
}

type redactMatchJSON struct {
	Pattern  string `json:"pattern"`
	Original string `json:"original"`
	Position int    `json:"position"`
}

// --- command implementations ---

func runAgentRedactPolicy(cmd *cobra.Command, args []string) error {
	textMode, _ := cmd.Flags().GetBool("text")

	projectRoot, err := findProjectRoot()
	if err != nil {
		projectRoot = ""
	}

	// gather builtin source
	manifest := session.GenerateManifest()
	sigResult := session.VerifyRedactionSignature()

	builtinSource := redactSourceJSON{
		Name:         "builtin",
		Description:  "Built-in security patterns (cryptographically signed)",
		PatternCount: manifest.PatternCount(),
		Patterns:     make([]redactPatternJSON, 0, len(manifest.Patterns)),
	}
	switch sigResult.Status {
	case signing.StatusValid:
		builtinSource.SignatureStatus = "valid"
	case signing.StatusInvalid:
		builtinSource.SignatureStatus = "invalid"
	case signing.StatusNotConfigured:
		builtinSource.SignatureStatus = "not_configured"
	case signing.StatusError:
		builtinSource.SignatureStatus = "error"
	case signing.StatusMissing:
		builtinSource.SignatureStatus = "missing"
	}
	for _, p := range manifest.Patterns {
		builtinSource.Patterns = append(builtinSource.Patterns, redactPatternJSON{
			Name:   p.Name,
			Regex:  p.Regex,
			Redact: p.Redact,
		})
	}

	// gather custom sources (team, repo, user)
	customSources := session.DiscoverRedactSources(projectRoot)

	var allSources []redactSourceJSON
	allSources = append(allSources, builtinSource)

	var allErrors []string
	totalPatterns := builtinSource.PatternCount

	for _, src := range customSources {
		s := redactSourceJSON{
			Name:         string(src.Source),
			Path:         src.Path,
			PatternCount: len(src.Rules),
			Patterns:     make([]redactPatternJSON, 0, len(src.Rules)),
		}
		for _, rule := range src.Rules {
			s.Patterns = append(s.Patterns, redactPatternJSON{
				Type:    rule.Type,
				Pattern: rule.RawPattern,
				Redact:  rule.Replacement,
				Line:    rule.LineNumber,
			})
		}
		for _, e := range src.Errors {
			allErrors = append(allErrors, e.Error())
		}
		totalPatterns += s.PatternCount
		allSources = append(allSources, s)
	}

	if textMode {
		return renderRedactPolicyText(allSources, totalPatterns, allErrors, sigResult)
	}

	// JSON output
	policy := redactPolicyJSON{
		Sources:       allSources,
		TotalPatterns: totalPatterns,
		ParseErrors:   allErrors,
	}
	if policy.ParseErrors == nil {
		policy.ParseErrors = []string{}
	}

	data, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON: %w", err)
	}
	fmt.Println(string(data))
	return nil
}

func renderRedactPolicyText(sources []redactSourceJSON, total int, errors []string, sigResult *signing.VerificationResult) error {
	fmt.Println(redactBold.Render("Redaction Policy (all sources)"))
	fmt.Println()
	fmt.Printf("  Total patterns: %d\n", total)
	fmt.Println()

	for _, src := range sources {
		header := redactBold.Render(fmt.Sprintf("[%s]", src.Name))
		if src.Path != "" {
			header += " " + ui.MutedStyle.Render(src.Path)
		}
		fmt.Println(header)

		if src.SignatureStatus != "" {
			label := "  Signature: "
			switch src.SignatureStatus {
			case "valid":
				fmt.Println(label + redactGreen.Render("valid"))
			case "not_configured":
				fmt.Println(label + redactYellow.Render("not configured (dev build)"))
			case "invalid":
				fmt.Println(label + redactRed.Render("INVALID"))
			default:
				fmt.Println(label + redactRed.Render(src.SignatureStatus))
			}
		}

		fmt.Printf("  Patterns: %d\n", src.PatternCount)

		if src.PatternCount > 0 {
			fmt.Println()
			for _, p := range src.Patterns {
				if p.Name != "" {
					// builtin pattern
					fmt.Printf("    %s\n", redactBold.Render(p.Name))
					fmt.Printf("      Regex:  %s\n", p.Regex)
					fmt.Printf("      Redact: %s\n", p.Redact)
				} else {
					// custom pattern
					fmt.Printf("    L%d %s %q -> %s\n", p.Line, p.Type, p.Pattern, p.Redact)
				}
			}
		}
		fmt.Println()
	}

	if len(errors) > 0 {
		fmt.Println(redactRed.Render("Parse Errors:"))
		for _, e := range errors {
			fmt.Printf("  %s %s\n", ui.IconFail, e)
		}
		fmt.Println()
	}

	return nil
}

func runAgentRedactTest(cmd *cobra.Command, args []string) error {
	textMode, _ := cmd.Flags().GetBool("text")

	// read input: argument or stdin
	var input string
	if args[0] == "-" {
		data, err := io.ReadAll(bufio.NewReader(os.Stdin))
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
		input = strings.TrimRight(string(data), "\n")
	} else {
		input = args[0]
	}

	projectRoot, err := findProjectRoot()
	if err != nil {
		projectRoot = ""
	}

	redactor, parseErrors := session.NewRedactorWithCustomRules(projectRoot)
	output, results := redactor.RedactStringWithDetails(input)

	if textMode {
		return renderRedactTestText(input, output, results, parseErrors)
	}

	// JSON output
	matches := make([]redactMatchJSON, 0, len(results))
	for _, r := range results {
		matches = append(matches, redactMatchJSON{
			Pattern:  r.PatternName,
			Original: r.Original,
			Position: r.Position,
		})
	}

	result := redactTestJSON{
		Input:    input,
		Output:   output,
		Matches:  matches,
		Redacted: input != output,
	}

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON: %w", err)
	}
	fmt.Println(string(data))

	// emit parse errors to stderr
	for _, e := range parseErrors {
		fmt.Fprintf(os.Stderr, "parse error: %s\n", e.Error())
	}

	return nil
}

func renderRedactTestText(input, output string, results []session.RedactionResult, parseErrors []session.RedactParseError) error {
	fmt.Println(redactBold.Render("Redaction Test"))
	fmt.Println()
	fmt.Printf("  Input:  %s\n", input)
	fmt.Printf("  Output: %s\n", output)
	fmt.Println()

	if len(results) == 0 {
		fmt.Println("  " + ui.MutedStyle.Render("No patterns matched."))
	} else {
		fmt.Printf("  Matches (%d):\n", len(results))
		for _, r := range results {
			fmt.Printf("    %s %s matched %q at position %d\n",
				ui.IconPass,
				redactBold.Render(r.PatternName),
				r.Original,
				r.Position,
			)
		}
	}
	fmt.Println()

	if len(parseErrors) > 0 {
		fmt.Println(redactRed.Render("Parse Errors:"))
		for _, e := range parseErrors {
			fmt.Printf("  %s %s\n", ui.IconFail, e.Error())
		}
		fmt.Println()
	}

	return nil
}
