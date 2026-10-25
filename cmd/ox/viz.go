package main

import (
	"bytes"
	"fmt"
	"html"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/plan"
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
payoff, authoring recipe, and—where mature—its executable visual contract:
evidence slots, canvas, composition, hierarchy, typography, routing, variants,
and finishing pass. 'ox viz suggest' ranks patterns for an intent, 'ox viz
render' computes parameterized visuals from JSON, and 'ox viz lint' checks
portable SVG/HTML output. All selection is deterministic and local.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonOut, _ := cmd.Flags().GetBool("json")
			if len(args) == 1 {
				return runVizOne(cmd, args[0], jsonOut)
			}
			return runVizList(cmd, jsonOut)
		},
	}
	cmd.AddCommand(newVizSuggestCommand(), newVizRenderCommand(), newVizLintCommand(), newVizPRCommand())
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
			page, _ := cmd.Flags().GetBool("page")
			return runVizRender(cmd, args[0], dataPath, page)
		},
	}
	cmd.Flags().String("data", "", "JSON data file for the pattern (use - for stdin)")
	cmd.Flags().Bool("page", false, "wrap the fragment in a standalone themed HTML document (pipe to a file and open it)")
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

func newVizPRCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pr",
		Short: "Prepare a GitHub-compatible visualization for a pull request",
		Long: "Choose and ship a visualization that improves code review without " +
			"adding review-only binary files to the pull-request branch. ox never " +
			"posts or edits a pull request; GitHub's authenticated editor creates " +
			"the final attachment URL when the PNG is pasted or dragged into the " +
			"description or a comment.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			intent, _ := cmd.Flags().GetString("intent")
			jsonOut, _ := cmd.Flags().GetBool("json")
			return runVizPRIntent(cmd, intent, jsonOut)
		},
	}
	cmd.Flags().String("intent", "", "reviewer question or PR story to match against the catalog")
	cmd.Flags().Bool("json", false, "emit PR-oriented suggestions as JSON (requires --intent)")
	return cmd
}

func runVizRender(cmd *cobra.Command, pattern, dataPath string, page bool) error {
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
	if page {
		doc, err := vizStandalonePage(pattern, frag)
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), doc)
		return nil
	}
	fmt.Fprintln(cmd.OutOrStdout(), frag)
	return nil
}

// vizStandalonePage wraps a fragment in a minimal document carrying the SAME
// scaffold stylesheet the fragment ships against. Previewing a viz under any
// other stylesheet is not a preview — the tokens ARE the design, and a chart
// that looks right on a default white page can be unreadable in the artifact.
//
// It stays inline-only: no script, no external URL, so the output is still
// CSP-safe and openable straight from file://.
func vizStandalonePage(pattern, frag string) (string, error) {
	css, err := plan.ScaffoldCSS()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html lang=\"en\"><head><meta charset=\"utf-8\">")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">")
	b.WriteString("<title>ox viz · " + html.EscapeString(pattern) + "</title><style>\n")
	b.WriteString(css)
	// The plan scaffold assumes a .shell grid; a bare fragment needs its own
	// breathing room, and section padding here keeps the figure off the edge.
	b.WriteString("\nbody{padding:34px 38px;max-width:1080px;margin:0 auto}\n")
	b.WriteString("</style></head><body><main><section>\n")
	b.WriteString(frag)
	b.WriteString("\n</section></main></body></html>")
	return b.String(), nil
}

func runVizSuggest(cmd *cobra.Command, intent string, limit int, jsonOut bool) error {
	out := cmd.OutOrStdout()
	suggestions := viz.Suggest(intent, limit)
	if jsonOut {
		return cli.PrintJSONTo(out, suggestions)
	}
	if len(suggestions) == 0 {
		fmt.Fprintln(out, "No confident visualization match. Browse the full catalog with `ox viz`.")
		return nil
	}
	fmt.Fprintln(out, cli.StyleBrand.Render("Suggested visual explanations"))
	fmt.Fprintln(out, cli.StyleDim.Render("First choose the smallest truthful form: prose/table, GitHub-safe Mermaid for a compact flow, then a rich visual only when it adds an unavailable dimension."))
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
	var findings []viz.Finding
	if bytes.HasPrefix(raw, []byte{'\x89', 'P', 'N', 'G', '\r', '\n', '\x1a', '\n'}) {
		findings = viz.LintPRPNG(raw)
	} else {
		findings = viz.Lint(raw, viz.LintOptions{})
	}
	if jsonOut {
		if err := cli.PrintJSONTo(cmd.OutOrStdout(), findings); err != nil {
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

func runVizPR(cmd *cobra.Command) error {
	return runVizPRIntent(cmd, "", false)
}

type prVisualSuggestions struct {
	Intent       string           `json:"intent"`
	Suggestions  []viz.Suggestion `json:"suggestions"`
	Decision     viz.PRDecision   `json:"decision"`
	MermaidFirst bool             `json:"mermaid_first"` // compatibility field; true only when Mermaid wins
	Guidance     string           `json:"guidance"`
}

func runVizPRIntent(cmd *cobra.Command, intent string, jsonOut bool) error {
	if jsonOut && strings.TrimSpace(intent) == "" {
		return fmt.Errorf("--json requires --intent")
	}
	if strings.TrimSpace(intent) != "" {
		decision := viz.DecidePRMedium(intent)
		if decision.Medium == viz.PRMediumRich && !config.PRVisualsRich(findGitRoot()) {
			decision = viz.PRDecision{Medium: viz.PRMediumMermaid, Reason: "rich PR-image generation is disabled by config; use the smallest GitHub-safe Mermaid flow that preserves the review fact", RequiredEvidence: "two to five named nodes and direct labeled edges"}
		}
		suggestions := viz.Suggest(intent, 5)
		if decision.Medium == viz.PRMediumRich && decision.Primary != "" {
			alreadySuggested := false
			for _, suggestion := range suggestions {
				alreadySuggested = alreadySuggested || suggestion.ID == decision.Primary
			}
			if primary, ok := viz.PatternByID(decision.Primary); ok && !alreadySuggested {
				suggestions = append([]viz.Suggestion{{ID: primary.ID, Category: primary.Category, Authoring: primary.Authoring, Reason: decision.Reason, Guidance: primary.Guidance, Next: "ox viz " + primary.ID}}, suggestions...)
			}
		}
		// PR bodies render the GitHub Mermaid subset directly; rank its recipes
		// before image recipes when both explain the same reviewer question.
		if decision.Medium == viz.PRMediumMermaid {
			sort.SliceStable(suggestions, func(i, j int) bool {
				return suggestions[i].Authoring == "mermaid" && suggestions[j].Authoring != "mermaid"
			})
		}
		if jsonOut {
			return cli.PrintJSONTo(cmd.OutOrStdout(), prVisualSuggestions{
				Intent:       intent,
				Suggestions:  suggestions,
				Decision:     decision,
				MermaidFirst: decision.Medium == viz.PRMediumMermaid,
				Guidance:     "Conceptual clarity is the gate. Follow decision.medium: prose/table before Mermaid, Mermaid before rich PNG, and rich only for a visual dimension the smaller form cannot preserve. Compare the candidate against the smallest truthful baseline; if a new reader cannot identify the start, primary path, changed fact, and outcome at least as quickly, use the baseline.",
			})
		}
		fmt.Fprintln(cmd.OutOrStdout(), cli.StyleBrand.Render("PR-oriented visual suggestions"))
		if decision.Medium == viz.PRMediumRich && decision.Primary != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "- Selected medium: rich %s (%s variant). Pull the construction contract with `ox viz %s`.\n", decision.Primary, decision.Variant, decision.Primary)
		}
		for _, suggestion := range suggestions {
			fmt.Fprintf(cmd.OutOrStdout(), "- %s — %s (%s)\n", suggestion.ID, suggestion.Reason, suggestion.Authoring)
		}
		return nil
	}

	out := cmd.OutOrStdout()
	projectRoot := findGitRoot()
	rich := config.PRVisualsRich(projectRoot)
	theme := config.PRVisualsTheme(projectRoot)
	fmt.Fprintln(out, cli.StyleBrand.Render("GitHub PR visual workflow"))
	fmt.Fprintln(out)
	fmt.Fprintln(out, "1. Conceptual clarity comes before design richness. Start with the minimum cognitive load: use no visual when one sentence answers the reviewer question; use GitHub-safe Mermaid for a 2–5-node linear flow; use a rich image only for data, parallel time, dense comparison, or a spatial relationship Mermaid cannot make clear. A polished image that weakens the reader's mental model is a failed visualization.")
	fmt.Fprintln(out, "2. For a simple source-adjacent flow, put GitHub-safe Mermaid directly in the PR body. Keep it direct: one path, direct labels, restrained color/weight, and no Mermaid Live-only extensions. Run ox viz suggest with what needs explaining when the choice is unclear.")
	if !rich {
		fmt.Fprintln(out, "3. The automated rich PNG/SVG PR-image workflow is disabled by effective config. This does not disable the ox viz catalog or `ox viz suggest`; use them for plans, docs, reports, or manual authoring. For this PR, keep the image-export workflow off and use a small GitHub-safe Mermaid diagram only when it materially improves review. Enable generated rich PR visuals with `ox config set pr_visuals.rich on` at personal, repo, or team scope.")
		return nil
	}
	fmt.Fprintf(out, "3. For a rich before/after, author a labeled HTML/SVG visual using the selected ox viz recipe (or Diagram Design when installed), in the %s theme, then export a 2x PNG under .context/pr-visuals/ — never add review-only media to the PR branch. Keep technical PR visuals unbranded by default: no SageOx wordmark, footer credit, or logo.\n", theme)
	fmt.Fprintln(out, "4. Before polishing, compare the draft with the smallest truthful baseline using the same reviewer question. In five seconds, a new reader must identify the start, primary path, changed fact, and outcome at least as quickly. Preserve clear topology and stable names; do not split one lifecycle into decorative bands or demote its central transition to an exception lane. If Mermaid is clearer, use Mermaid and improve only its typography, spacing, direction, and restrained emphasis.")
	fmt.Fprintln(out, "5. Reserve space for every label: connectors never pass under text; type must stay readable at GitHub's inline width; if content collides, enlarge or split the visual rather than shrinking text. Render and visually inspect the final PNG before upload.")
	fmt.Fprintln(out, "6. Export only the intended 2x inline canvas (normally no wider than 1920px) as indexed PNG (PNG-8: ≤256 colors), strip metadata, and quantize the limited diagram palette. Keep alpha only when it carries meaning. Aim for ≤500 KiB; 1 MiB is the hard ceiling. With pngquant: `pngquant --quality=85-100 --speed 1 --strip --force --output diagram.png diagram@2x.png`; then verify `ox viz lint diagram.png --strict`.")
	fmt.Fprintln(out, "7. Paste or drag the PNG into GitHub's PR editor. GitHub creates the attachment URL; ox deliberately does not create or post the PR body. Add concise alt text and one sentence that states the review-relevant conclusion. If wanted, put a subtle `enriched by SageOx` cue in the surrounding PR Markdown—not inside the diagram.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Before/after Markdown:")
	fmt.Fprintln(out, "  ## Request-path change")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  ![Before: Orders validates the session. After: the Gateway verifies it before routing.](github-user-attachment-url)")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  Validation moves to the Gateway; Orders receives only verified requests.")
	return nil
}

func runVizList(cmd *cobra.Command, jsonOut bool) error {
	out := cmd.OutOrStdout()
	patterns := viz.Catalog()
	if jsonOut {
		return cli.PrintJSONTo(out, patterns)
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
		return cli.PrintJSONTo(out, p)
	}
	fmt.Fprintln(out, cli.StyleBrand.Render(p.ID))
	fmt.Fprintf(out, "%s %s · %s\n", cli.StyleBold.Render("Kind:"), p.Category, p.Authoring)
	fmt.Fprintf(out, "%s %s\n", cli.StyleBold.Render("Use:"), p.Use)
	fmt.Fprintf(out, "%s %s\n", cli.StyleBold.Render("Why:"), p.Why)
	if p.Guidance != "" {
		fmt.Fprintf(out, "%s %s\n", cli.StyleBold.Render("Design:"), p.Guidance)
	}
	if p.Contract != nil {
		printVisualContract(out, p.Contract)
	}
	fmt.Fprintln(out, cli.StyleDim.Render("Discipline: answer one question; omit product/category headers, decorative frames, rules, footers, and brand marks. If a compact table or GitHub-safe Mermaid says the same thing, use that instead."))
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

func printVisualContract(out io.Writer, c *viz.VisualContract) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, cli.StyleBold.Render("Visual contract:"))
	fmt.Fprintf(out, "  Reviewer question: %s\n", c.Question)
	fmt.Fprintf(out, "  Reject when: %s\n", c.RejectWhen)
	printContractRules(out, "Conceptual clarity — must pass before styling", c.Clarity)
	fmt.Fprintf(out, "  Canvas: %dx%d (%s), %dpx margin, %dpx grid, %s background\n", c.Canvas.Width, c.Canvas.Height, c.Canvas.AspectRatio, c.Canvas.Margin, c.Canvas.Grid, c.Canvas.Background)
	fmt.Fprintf(out, "  Type: title %dpx; section %dpx; label %dpx; detail %dpx; annotation %dpx; never below %dpx; max %d characters/line\n", c.Typography.Title, c.Typography.Section, c.Typography.Label, c.Typography.Detail, c.Typography.Annotation, c.Typography.Minimum, c.Typography.MaxLine)
	fmt.Fprintln(out, "  Evidence slots:")
	for _, slot := range c.Evidence {
		required := "optional"
		if slot.Required {
			required = "required"
		}
		fmt.Fprintf(out, "    - %s (%s, max %d): %s\n", slot.ID, required, slot.Maximum, slot.Prompt)
	}
	printContractRules(out, "Composition", c.Composition)
	printContractRules(out, "Hierarchy", c.Hierarchy)
	printContractRules(out, "Connector grammar", c.Connectors)
	printContractRules(out, "Color roles", c.Color)
	printContractRules(out, "Construction constraints", c.Constraints)
	fmt.Fprintln(out, "  Variants:")
	for _, variant := range c.Variants {
		fmt.Fprintf(out, "    - %s: %s Budget: %s.\n", variant.ID, variant.UseWhen, variant.Budget)
	}
	printContractRules(out, "Never", c.AntiPatterns)
	printContractRules(out, "Finishing pass", c.FinishingPass)
}

func printContractRules(out io.Writer, label string, rules []string) {
	if len(rules) == 0 {
		return
	}
	fmt.Fprintln(out, "  "+label+":")
	for _, rule := range rules {
		fmt.Fprintln(out, "    - "+rule)
	}
}

func init() {
	vizCmd.GroupID = "dev"
	rootCmd.AddCommand(vizCmd)
	planCmd.AddCommand(planVizCmd)
}
