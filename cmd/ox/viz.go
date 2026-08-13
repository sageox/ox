package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/viz"
	"github.com/spf13/cobra"
)

// vizCmd is the canonical, artifact-neutral visualization surface. The hidden
// planVizCmd below is built by the same factory for command compatibility.
var vizCmd = newVizCommand(false)
var planVizCmd = newVizCommand(true)

func newVizCommand(compat bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "viz [id]",
		Hidden: compat,
		Short:  "Choose, author, render, and lint visual explanations",
		Long: `Use a shared visualization vocabulary in plans, documentation, pull
requests, reports, and design notes.

Run with no argument to browse the catalog; pass an id to get its cognitive
payoff and authoring recipe. 'ox viz suggest' ranks patterns for an intent,
'ox viz render' computes parameterized visuals from JSON, and 'ox viz lint'
checks portable SVG/HTML output. All selection is deterministic and local.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonOut, _ := cmd.Flags().GetBool("json")
			if len(args) == 1 {
				return runVizOne(cmd, args[0], jsonOut)
			}
			return runVizList(cmd, jsonOut)
		},
	}
	cmd.AddCommand(newVizSuggestCommand(), newVizRenderCommand(), newVizLintCommand())
	return cmd
}

func newVizRenderCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "render <id>",
		Short: "Render a parameterized visualization from JSON data",
		Long: `Render a parameterized catalog pattern from JSON into an HTML/SVG
fragment. ox computes geometry; the AI coworker supplies only data. Inspect the
required shape with 'ox viz <id>'.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dataPath, _ := cmd.Flags().GetString("data")
			if dataPath == "" {
				return fmt.Errorf("--data is required: pass a JSON data file (or - for stdin)")
			}
			return runVizRender(cmd, args[0], dataPath)
		},
	}
	cmd.Flags().String("data", "", "JSON data file for the pattern (use - for stdin)")
	return cmd
}

func newVizSuggestCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "suggest <intent>",
		Short: "Suggest visual patterns for what you need to explain",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			limit, _ := cmd.Flags().GetInt("limit")
			if limit < 1 {
				return fmt.Errorf("--limit must be at least 1")
			}
			jsonOut, _ := cmd.Flags().GetBool("json")
			return runVizSuggest(cmd, strings.Join(args, " "), limit, jsonOut)
		},
	}
	cmd.Flags().Int("limit", 3, "maximum number of suggestions")
	return cmd
}

func newVizLintCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lint <file>",
		Short: "Check a visual fragment for accessibility, portability, and editorial quality",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			strict, _ := cmd.Flags().GetBool("strict")
			jsonOut, _ := cmd.Flags().GetBool("json")
			return runVizLint(cmd, args[0], strict, jsonOut)
		},
	}
	cmd.Flags().Bool("strict", false, "promote editorial warnings to a non-zero exit")
	return cmd
}

func runVizRender(cmd *cobra.Command, pattern, dataPath string) error {
	var raw []byte
	var err error
	if dataPath == "-" {
		raw, err = io.ReadAll(cmd.InOrStdin())
	} else {
		raw, err = os.ReadFile(dataPath)
	}
	if err != nil {
		return fmt.Errorf("read --data %q: %w", dataPath, err)
	}
	frag, err := viz.Render(pattern, raw)
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), frag)
	return nil
}

func runVizSuggest(cmd *cobra.Command, intent string, limit int, jsonOut bool) error {
	out := cmd.OutOrStdout()
	suggestions := viz.Suggest(intent, limit)
	if jsonOut {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(suggestions)
	}
	if len(suggestions) == 0 {
		fmt.Fprintln(out, "No confident visualization match. Browse the full catalog with `ox viz`.")
		return nil
	}
	fmt.Fprintln(out, cli.StyleBrand.Render("Suggested visual explanations"))
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, cli.StyleDim.Render("ID\tCATEGORY\tWHY\tNEXT"))
	for _, s := range suggestions {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.ID, s.Category, s.Reason, s.Next)
	}
	return tw.Flush()
}

func runVizLint(cmd *cobra.Command, file string, strict, jsonOut bool) error {
	var raw []byte
	var err error
	if file == "-" {
		raw, err = io.ReadAll(cmd.InOrStdin())
	} else {
		raw, err = os.ReadFile(file)
	}
	if err != nil {
		return fmt.Errorf("read visualization %q: %w", file, err)
	}
	findings := viz.Lint(raw, viz.LintOptions{})
	if jsonOut {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if err := enc.Encode(findings); err != nil {
			return err
		}
	} else if len(findings) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), cli.StyleSuccess.Render("✓")+" visualization checks OK")
	} else {
		for _, f := range findings {
			mark := cli.StyleWarning.Render("!")
			if f.Severity == viz.SeverityError {
				mark = cli.StyleError.Render("×")
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s [%s/%s] %s\n", mark, f.Severity, f.Rule, f.Message)
		}
	}
	if viz.HasErrors(findings) || (strict && len(findings) > 0) {
		return fmt.Errorf("%d visualization lint finding(s)", len(findings))
	}
	return nil
}

func runVizList(cmd *cobra.Command, jsonOut bool) error {
	out := cmd.OutOrStdout()
	patterns := viz.Catalog()
	if jsonOut {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(patterns)
	}
	fmt.Fprintln(out, cli.StyleBrand.Render("Visualization patterns"))
	fmt.Fprintln(out, cli.StyleDim.Render("Use `ox viz suggest <intent>` or pull a recipe with `ox viz <id>`."))
	fmt.Fprintln(out)
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, cli.StyleDim.Render("CATEGORY\tID\tAUTHORING\tUSE WHEN"))
	for _, p := range patterns {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.Category, p.ID, p.Authoring, truncate(p.Use, 68))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(out)
	cli.PrintHintTo(out, "AUTHORING ox-render = `ox viz render <id> --data <file.json>`; other recipes are author-guided.")
	return nil
}

func runVizOne(cmd *cobra.Command, id string, jsonOut bool) error {
	out := cmd.OutOrStdout()
	p, ok := viz.PatternByID(id)
	if !ok {
		return fmt.Errorf("no visualization pattern %q (run `ox viz` to list available ids)", id)
	}
	if jsonOut {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(p)
	}
	fmt.Fprintln(out, cli.StyleBrand.Render(p.ID))
	fmt.Fprintf(out, "%s %s · %s\n", cli.StyleBold.Render("Kind:"), p.Category, p.Authoring)
	fmt.Fprintf(out, "%s %s\n", cli.StyleBold.Render("Use:"), p.Use)
	fmt.Fprintf(out, "%s %s\n", cli.StyleBold.Render("Why:"), p.Why)
	if len(p.Tags) > 0 {
		fmt.Fprintf(out, "%s %s\n", cli.StyleBold.Render("Tags:"), strings.Join(p.Tags, ", "))
	}
	if p.Origin != "" {
		fmt.Fprintf(out, "%s %s\n", cli.StyleBold.Render("Adapted from:"), p.Origin)
	}
	if p.Param != "" {
		fmt.Fprintf(out, "%s ox viz render %s --data <file.json>\n", cli.StyleBold.Render("Data:"), p.ID)
		fmt.Fprintf(out, "%s %s\n", cli.StyleDim.Render("shape:"), p.Param)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, p.Body)
	return nil
}

func init() {
	vizCmd.GroupID = "dev"
	rootCmd.AddCommand(vizCmd)
	planCmd.AddCommand(planVizCmd)
}
