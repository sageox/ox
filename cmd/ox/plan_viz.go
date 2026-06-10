package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/plan"
	"github.com/spf13/cobra"
)

// plan_viz.go surfaces the visualization-pattern catalog progressively. The
// agent runs `ox plan viz` to scan the menu cheaply (id + "use when"), then
// `ox plan viz <id>` to pull a single pattern's snippet only when it needs it —
// keeping the token cost proportional to what actually lands in the plan. This
// is the cross-agent path: the catalog ships in the binary, so Codex/Gemini/Amp/
// Pi explore the same visualization vocabulary as Claude.
// planVizCmd is Hidden (agent/authoring tier): humans don't browse a viz
// catalog; agents discover it via `ox agent prime` while authoring a plan.
var planVizCmd = &cobra.Command{
	Use:    "viz [id]",
	Hidden: true,
	Short:  "Browse visualization patterns for HTML plans (sparklines, dependency graphs, swimlanes, Tufte tables, mockups)",
	Long: `Browse the catalog of information-visualization patterns for HTML plans.

Run with no argument to list every pattern (id + when to use it); pass an id to
print that pattern's cognitive payoff and a copy-paste snippet. Parameterized
patterns render real HTML from JSON via 'ox plan viz render <id> --data <file>'.
Snippets use the plan scaffold's design-system classes, so they render and theme
correctly inside an 'ox plan render' page.

Explored WHILE authoring a plan: scan the menu, then weave in the few
visualizations that compress understanding for the reviewer.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonOut, _ := cmd.Flags().GetBool("json")
		if len(args) == 1 {
			return runPlanVizOne(cmd, args[0], jsonOut)
		}
		return runPlanVizList(cmd, jsonOut)
	},
}

// planVizRenderCmd renders a parameterized pattern to HTML from JSON data.
var planVizRenderCmd = &cobra.Command{
	Use:   "render <id>",
	Short: "Render a parameterized visualization pattern from JSON data to an HTML fragment",
	Long: `Render a parameterized pattern (bar-chart, stat-cards, file-impact-map,
risk-matrix, …) from its JSON data into a self-contained HTML fragment you paste
into the plan markdown. ox computes the geometry so the visual is correct by
construction. See the data shape with 'ox plan viz <id>'.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dataPath, _ := cmd.Flags().GetString("data")
		if dataPath == "" {
			return fmt.Errorf("--data is required: pass the JSON data file (or - for stdin)")
		}
		return runPlanVizRender(cmd, args[0], dataPath)
	},
}

// runPlanVizRender renders a parameterized pattern from a --data JSON file (or
// stdin with "-") into an HTML fragment the agent pastes straight into the plan
// markdown. ox computes the geometry so the visual is correct by construction.
func runPlanVizRender(cmd *cobra.Command, pattern, dataPath string) error {
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
	frag, err := plan.RenderViz(pattern, raw)
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), frag)
	return nil
}

func runPlanVizList(cmd *cobra.Command, jsonOut bool) error {
	out := cmd.OutOrStdout()
	patterns := plan.VizCatalog()

	if jsonOut {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(patterns)
	}

	fmt.Fprintln(out, cli.StyleBrand.Render("Visualization patterns for HTML plans"))
	fmt.Fprintln(out, cli.StyleDim.Render("Pull a snippet with `ox plan viz <id>`; weave in what compresses understanding."))
	fmt.Fprintln(out)
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, cli.StyleDim.Render("ID\tDATA\tUSE WHEN"))
	for _, p := range patterns {
		dataMark := "—"
		if p.Param != "" {
			dataMark = "✓" // supports `ox plan viz render <id> --data`
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", p.ID, dataMark, truncate(p.Use, 72))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(out)
	cli.PrintHint("DATA ✓ = parameterized: `ox plan viz render <id> --data <file.json>` emits the HTML for you.")
	return nil
}

func runPlanVizOne(cmd *cobra.Command, id string, jsonOut bool) error {
	out := cmd.OutOrStdout()
	p, ok := plan.VizPatternByID(id)
	if !ok {
		return fmt.Errorf("no visualization pattern %q (run `ox plan viz` to list available ids)", id)
	}

	if jsonOut {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(p)
	}

	fmt.Fprintln(out, cli.StyleBrand.Render(p.ID))
	if p.Use != "" {
		fmt.Fprintf(out, "%s %s\n", cli.StyleBold.Render("Use:"), p.Use)
	}
	if p.Why != "" {
		fmt.Fprintf(out, "%s %s\n", cli.StyleBold.Render("Why:"), p.Why)
	}
	if p.Param != "" {
		fmt.Fprintf(out, "%s ox plan viz render %s --data <file.json>\n", cli.StyleBold.Render("Data:"), p.ID)
		fmt.Fprintf(out, "%s %s\n", cli.StyleDim.Render("shape:"), p.Param)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, p.Body)
	return nil
}

func init() {
	planVizCmd.Flags().Bool("json", false, "emit the catalog (or a single pattern) as JSON")
	planVizRenderCmd.Flags().String("data", "", "JSON data file for the pattern (use - for stdin)")
	planVizCmd.AddCommand(planVizRenderCmd)
	planCmd.AddCommand(planVizCmd)
}
