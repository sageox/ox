package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/identity"
	"github.com/sageox/ox/internal/plan"
	"github.com/spf13/cobra"
)

var planCmd = &cobra.Command{
	Use:   "plan",
	Short: "Enrich an implementation plan with SageOx team context",
	Long: `Enrich an agent-generated implementation plan with deterministic SageOx
signals (collision, prior-art, expert-routing) and a context bundle the agent
can reason over. ox computes badges locally — it never makes an LLM or network
call in this path.

Reads the active plan from --file or stdin. Use --json for the plumbing path
(hook/agent), which emits the full Result as JSON and nothing else.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		file, _ := cmd.Flags().GetString("file")
		jsonOut, _ := cmd.Flags().GetBool("json")

		in, err := plan.Resolve(file, cmd.InOrStdin())
		if err != nil {
			return err
		}

		// No --file, no piped stdin, and nothing auto-discovered: there is no
		// plan to enrich. Tell the human clearly instead of silently enriching
		// an empty input (which yields a confusing all-zero summary).
		if strings.TrimSpace(in.Raw) == "" {
			fmt.Fprintln(cmd.OutOrStdout(),
				"No plan found. Pass --file <plan.md>, pipe a plan on stdin, or save a plan-mode file to ~/.claude/plans/ first.")
			return nil
		}

		// gitRoot is best-effort: detectors are fail-open, so an empty root
		// simply yields fewer signals rather than an error.
		gitRoot := findGitRoot()

		result := plan.Enrich(context.Background(), in, gitRoot)

		// --json is the plumbing path: no save, no metrics side effects that
		// could perturb stdout. Emit the Result and nothing else.
		if jsonOut {
			return writePlanJSON(cmd, result)
		}

		// Porcelain path: optionally capture the plan to the ledger, then
		// record a local metric (including whether it was saved), then print
		// a concise human summary.
		savedDir := maybeSavePlan(gitRoot, in, result)
		plan.RecordPlanGenerated(result, savedDir != "")
		return writePlanHuman(cmd, result, savedDir)
	},
}

var planListCmd = &cobra.Command{
	Use:   "list",
	Short: "Browse saved ledger plans",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPlanList(cmd)
	},
}

var planViewCmd = &cobra.Command{
	Use:   "view <slug>",
	Short: "Open a saved plan",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		open, _ := cmd.Flags().GetBool("open")
		return runPlanView(cmd, args[0], open)
	},
}

var planSaveCmd = &cobra.Command{
	Use:   "save",
	Short: "Persist a fully-enriched plan (merged badges + optional HTML) to the ledger",
	Long: `Persist a fully-enriched plan to the ledger. Unlike bare 'ox plan' — which
auto-saves only the deterministic, ox-computed annotations — 'ox plan save' is the
explicit full-plan persist path used by the ox-plan renderer skill after it has
authored its judgment badges and (optionally) rendered the HTML.

  --plan        the plan markdown (source for plan.md + topic/slug derivation)
  --annotations a MERGED annotations.json: the 'ox plan --json' Result with the
                agent-authored judgment badges appended (a full plan.Result)
  --html        optional pre-rendered HTML; size-gated plain-git-vs-LFS per store.Save

This command never renders HTML and never makes an LLM/network call — it only
materializes the already-produced artifacts into the ledger working tree. It
always saves (the skill is deliberately persisting), independent of the
plan.save config.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPlanSave(cmd)
	},
}

// maybeSavePlan captures the enriched plan to the ledger when auto-save is
// enabled and a ledger is configured. html is nil for now — the porcelain path
// never renders HTML just to save it (that's a skill-side, opt-in action).
// Returns the saved directory, or "" when nothing was saved (disabled, no
// ledger, or a write error — capture is best-effort and never aborts the
// command).
func maybeSavePlan(gitRoot string, in plan.Input, result plan.Result) string {
	if gitRoot == "" || !config.PlanSave(gitRoot) {
		return ""
	}

	topic := planTopic(in)
	meta := plan.Meta{
		Topic:          topic,
		Slug:           plan.Slugify(topic),
		Authors:        planAuthors(gitRoot),
		CreatedAt:      time.Now().UTC(),
		SourcePlanPath: in.Path,
	}

	dir, err := plan.Save(gitRoot, in, result, nil, meta)
	if err != nil {
		// best-effort: a missing ledger or write failure must not break the
		// enrichment output the agent is waiting on.
		return ""
	}
	return dir
}

// runPlanSave persists a fully-enriched plan to the ledger from a plan markdown
// file, a MERGED annotations.json (deterministic + judgment badges), and an
// optional pre-rendered HTML file. This is the explicit full-plan persist path
// the ox-plan skill calls — it always saves (no auto-save config gate) and never
// renders HTML here (the skill already produced it).
func runPlanSave(cmd *cobra.Command) error {
	planPath, _ := cmd.Flags().GetString("plan")
	annPath, _ := cmd.Flags().GetString("annotations")
	htmlPath, _ := cmd.Flags().GetString("html")

	if planPath == "" {
		return fmt.Errorf("--plan is required: pass the plan markdown file")
	}
	if annPath == "" {
		return fmt.Errorf("--annotations is required: pass the merged annotations.json")
	}

	// The plan markdown drives plan.md + topic/slug derivation.
	in, err := plan.Resolve(planPath, nil)
	if err != nil {
		return err
	}

	// The merged annotations.json is a full plan.Result: ox's deterministic
	// badges plus the agent-authored judgment badges the skill appended.
	annBytes, err := os.ReadFile(annPath)
	if err != nil {
		return fmt.Errorf("read annotations %q: %w", annPath, err)
	}
	var result plan.Result
	if err := json.Unmarshal(annBytes, &result); err != nil {
		return fmt.Errorf("parse annotations %q: %w", annPath, err)
	}

	// Optional pre-rendered HTML. store.Save applies the size-gated
	// plain-git-vs-LFS rule; we never render here.
	var html []byte
	if htmlPath != "" {
		html, err = os.ReadFile(htmlPath)
		if err != nil {
			return fmt.Errorf("read html %q: %w", htmlPath, err)
		}
	}

	gitRoot := findGitRoot()
	topic := planTopic(in)
	meta := plan.Meta{
		Topic:          topic,
		Slug:           plan.Slugify(topic),
		Authors:        planAuthors(gitRoot),
		CreatedAt:      time.Now().UTC(),
		SourcePlanPath: planPath,
	}

	dir, err := plan.Save(gitRoot, in, result, html, meta)
	if err != nil {
		return fmt.Errorf("save plan: %w", err)
	}

	slog.Info("plan_saved", "dir", dir, "slug", meta.Slug, "html", htmlPath != "", "annotations", len(result.Annotations))
	fmt.Fprintf(cmd.OutOrStdout(), "Saved plan to ledger: %s\n", dir)
	return nil
}

// planTopic derives a human title for the plan: the first H1/H2 heading, else
// the first non-empty line, else a fallback. Used for the slug and meta.Topic.
func planTopic(in plan.Input) string {
	for _, s := range in.Sections {
		if h := strings.TrimSpace(s.Heading); h != "" {
			return h
		}
	}
	for _, line := range strings.Split(in.Raw, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(line, "# "))
		if line != "" {
			return line
		}
	}
	return "untitled plan"
}

// planAuthors returns the capturing coworker's display name (privacy-safe, not
// an email) when resolvable, else nil.
func planAuthors(gitRoot string) []string {
	ep := ""
	if ctx, err := config.LoadProjectContext(gitRoot); err == nil && ctx != nil {
		ep = ctx.Endpoint()
	}
	if name := identity.AttributionDisplayName(ep, ""); name != "" {
		return []string{name}
	}
	return nil
}

// writePlanJSON emits the Result as indented JSON and nothing else. This is the
// plumbing path the plan-exit hook and the agent call. It makes NO network/LLM
// call (Enrich is pure-local).
func writePlanJSON(cmd *cobra.Command, result plan.Result) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		return fmt.Errorf("encode plan result: %w", err)
	}
	_, err := buf.WriteTo(cmd.OutOrStdout())
	return err
}

// writePlanHuman prints a concise summary: signal counts plus one line per
// material annotation, where the plan was saved (if captured), and a hint that
// an enriched HTML render is available via the ox-plan skill.
func writePlanHuman(cmd *cobra.Command, result plan.Result, savedDir string) error {
	out := cmd.OutOrStdout()
	s := result.Signals

	var b strings.Builder
	fmt.Fprintf(&b, "Plan signals: %d collision(s), %d prior-art, %d expert route(s)\n",
		s.Collisions, s.PriorArt, s.ExpertRoutes)

	if len(result.Annotations) == 0 {
		b.WriteString("No team-context signals fired for this plan.\n")
	} else {
		for _, a := range result.Annotations {
			section := a.Section
			if section == "" {
				section = "(plan)"
			}
			fmt.Fprintf(&b, "  [%s] %s — %s\n", a.Type, section, a.Why)
		}
	}

	if savedDir != "" {
		fmt.Fprintf(&b, "\nSaved to ledger: %s\n", savedDir)
	}

	if s.Material {
		b.WriteString("\nMaterial signals found. Render an enriched HTML plan via the ox-plan skill for faster human review.\n")
	}

	fmt.Fprint(out, b.String())
	return nil
}

// runPlanList renders the saved plans as a table. Fail-open: outside a project
// or with no plans, it prints a friendly empty-state instead of erroring.
func runPlanList(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	gitRoot := findGitRoot()

	plans, err := plan.List(gitRoot)
	if err != nil {
		return fmt.Errorf("list plans: %w", err)
	}
	if len(plans) == 0 {
		fmt.Fprintln(out, "No saved plans yet. Run 'ox plan' on an implementation plan to capture one.")
		return nil
	}

	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, cli.StyleDim.Render("SLUG\tDATE\tHTML\tAUTHORS\tTOPIC"))
	for _, p := range plans {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			p.Slug,
			planDate(p.CreatedAt),
			htmlMark(p.HasHTML),
			authorsLabel(p.Authors),
			truncate(p.Topic, 60),
		)
	}
	return tw.Flush()
}

// runPlanView prints a saved plan's markdown plus a badge summary. When a
// plan.html exists, it mentions the file; with --open (and a non-headless
// terminal) it opens it in the browser, hydrating an LFS pointer first.
func runPlanView(cmd *cobra.Command, slug string, open bool) error {
	out := cmd.OutOrStdout()
	gitRoot := findGitRoot()

	planMD, res, info, err := plan.Load(gitRoot, slug)
	if err != nil {
		return err
	}

	fmt.Fprintln(out, cli.StyleBrand.Render(info.Topic))
	fmt.Fprintf(out, "%s  %s\n", cli.StyleDim.Render("slug:"+info.Slug), cli.StyleDim.Render(planDate(info.CreatedAt)))
	if len(info.Authors) > 0 {
		fmt.Fprintln(out, cli.StyleDim.Render("authors: "+strings.Join(info.Authors, ", ")))
	}
	fmt.Fprintln(out)

	fmt.Fprintln(out, planMD)

	writeBadgeSummary(out, res)

	htmlPath, _, _, hasHTML := plan.PlanHTMLPath(info.Dir)
	if !hasHTML {
		return nil
	}

	fmt.Fprintf(out, "\nRendered HTML: %s\n", cli.StyleFile.Render(htmlPath))
	if !open {
		cli.PrintHint("Re-run with --open to view the rendered plan in your browser.")
		return nil
	}
	if cli.IsHeadless() {
		cli.PrintHint("Headless environment: open the HTML file above manually.")
		return nil
	}
	return openPlanHTML(info.Dir)
}

// openPlanHTML opens a captured plan's plan.html in the browser. When the
// in-place file is an LFS pointer (large render stored out-of-band), it is
// hydrated to a temp file first so the browser opens real content, never a
// 130-byte pointer. Hydration uses pure-Go LFS — never the git-lfs binary.
func openPlanHTML(dir string) error {
	path, _, isPointer, exists := plan.PlanHTMLPath(dir)
	if !exists {
		return fmt.Errorf("plan.html not found in %s", dir)
	}
	if !isPointer {
		return cli.OpenInBrowser(path)
	}
	// Large render stored as an LFS pointer. Hydration via the Batch API is a
	// follow-up (mirrors session hydrate); for now surface a clear message
	// rather than opening a pointer stub.
	cli.PrintHint("This plan's HTML is stored in LFS and not yet hydrated locally. Hydrate the ledger to view it.")
	return nil
}

// writeBadgeSummary prints the stored Result's signal rollup and annotations.
func writeBadgeSummary(out io.Writer, res plan.Result) {
	s := res.Signals
	fmt.Fprintf(out, "%s %d collision(s), %d prior-art, %d expert route(s)\n",
		cli.StyleBold.Render("Signals:"), s.Collisions, s.PriorArt, s.ExpertRoutes)
	for _, a := range res.Annotations {
		section := a.Section
		if section == "" {
			section = "(plan)"
		}
		fmt.Fprintf(out, "  [%s] %s — %s\n", a.Type, section, a.Why)
	}
}

func planDate(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("2006-01-02")
}

func htmlMark(has bool) string {
	if has {
		return "yes"
	}
	return "—"
}

func authorsLabel(authors []string) string {
	if len(authors) == 0 {
		return "—"
	}
	return strings.Join(authors, ", ")
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func init() {
	planCmd.Flags().Bool("json", false, "emit the enrichment Result as JSON (plumbing path; no network/LLM call)")
	planCmd.Flags().String("file", "", "plan source file (default: stdin, else newest ~/.claude/plans/*.md)")

	planViewCmd.Flags().Bool("open", false, "open the rendered plan.html in your browser (if one was saved)")

	planSaveCmd.Flags().String("plan", "", "plan markdown file (required; source for plan.md + topic/slug)")
	planSaveCmd.Flags().String("annotations", "", "merged annotations.json: ox --json badges + agent judgment badges (required)")
	planSaveCmd.Flags().String("html", "", "optional pre-rendered HTML; size-gated plain-git-vs-LFS on save")

	planCmd.AddCommand(planListCmd)
	planCmd.AddCommand(planViewCmd)
	planCmd.AddCommand(planSaveCmd)

	planCmd.GroupID = "dev"
	rootCmd.AddCommand(planCmd)
}
