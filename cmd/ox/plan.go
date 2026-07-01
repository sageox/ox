package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/identity"
	"github.com/sageox/ox/internal/plan"
	"github.com/spf13/cobra"
)

// planCmd is a pure command group (no RunE): bare `ox plan` prints help listing
// the human-facing verbs (enrich, render, review, list, view). Agent/CI verbs
// (save, lint, viz, feedback) are Hidden and taught via `ox agent prime`.
var planCmd = &cobra.Command{
	Use:   "plan",
	Short: "Work with implementation plans (enrich, render, review)",
	Long: `Work with SageOx-enriched implementation plans.

  enrich   compute team-context signals for a plan (JSON for agents)
  render   render a plan to a self-contained HTML page for human review
  review   serve a plan and collect human review feedback (the review loop)
  list     browse saved plans
  view     read a saved plan in the terminal

Agents: 'ox plan enrich' returns JSON team context (collision / prior-art /
expert-routing) at zero LLM/network cost. When a human is shaping a plan,
recommend 'ox plan render --open' to view it and 'ox plan review <slug>' for an
inline review loop — those are human-opt-in, never auto-run.`,
}

// planEnrichCmd is the agent entry: it emits the enrichment Result as JSON BY
// DEFAULT (the agent-facing, token-frugal, parseable output). --text switches to
// the human summary. Deterministic + network-free.
var planEnrichCmd = &cobra.Command{
	Use:   "enrich",
	Short: "Enrich an implementation plan with SageOx team context (JSON by default)",
	Long: `Enrich an agent-generated implementation plan with deterministic SageOx
signals (collision, prior-art, expert-routing) and a context bundle the agent
can reason over. ox computes badges locally — no LLM or network call.

Output is JSON by default (the agent/plumbing path). Use --text for a human
summary. Reads the plan from --file or stdin, else the newest ~/.claude/plans/*.md.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		file, _ := cmd.Flags().GetString("file")
		text, _ := cmd.Flags().GetBool("text")
		persist, _ := cmd.Flags().GetBool("persist")

		in, err := plan.Resolve(file, cmd.InOrStdin())
		if err != nil {
			return err
		}

		// No plan found anywhere: a clear message beats enriching empty input.
		if strings.TrimSpace(in.Raw) == "" {
			fmt.Fprintln(cmd.OutOrStdout(),
				"No plan found. Pass --file <plan.md>, pipe a plan on stdin, or save a plan-mode file to ~/.claude/plans/ first.")
			return nil
		}

		// gitRoot is best-effort: detectors are fail-open, so an empty root
		// simply yields fewer signals rather than an error.
		gitRoot := findGitRoot()
		result := plan.Enrich(context.Background(), in, gitRoot)

		if !text {
			// DEFAULT: JSON plumbing path — emit the Result and nothing else.
			// With --persist (the ExitPlanMode hook) also save + commit a draft;
			// the save writes only to logs/ledger so stdout JSON stays clean.
			if persist && gitRoot != "" && config.PlanSave(gitRoot) {
				savePlanWithProvenance(gitRoot, in, result, nil)
			}
			return writePlanJSON(cmd, result)
		}

		// --text: human porcelain — auto-save (if enabled), metric, summary.
		savedDir := maybeSavePlan(gitRoot, in, result)
		plan.RecordPlanGenerated(result, savedDir != "")
		return writePlanHuman(cmd, result, savedDir)
	},
}

var planListCmd = &cobra.Command{
	Use:   "list",
	Short: "Browse saved ledger plans",
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonOut, _ := cmd.Flags().GetBool("json")
		return runPlanList(cmd, jsonOut)
	},
}

var planViewCmd = &cobra.Command{
	Use:   "view <slug>",
	Short: "Read a saved plan in the terminal",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPlanView(cmd, args[0])
	},
}

// planRenderCmd is the single HTML entry point. No slug → render the plan from
// --file/stdin (enrich first). A slug → render a saved plan (with its review
// state). -o/--output writes to a path; --open opens in the browser. This
// absorbs the former `ox plan --render`, `ox plan --open`, and `ox plan view
// --open` entrypoints.
var planRenderCmd = &cobra.Command{
	Use:   "render [slug]",
	Short: "Render a plan to a self-contained HTML page",
	Long: `Render a plan to a self-contained HTML page.

No slug renders the plan from --file/stdin; a slug renders a saved plan with its
review state. With --open on a SAVED plan, ox launches the live review loop
(` + "`ox plan review`" + `) so your marks write straight back to the ledger
instead of a dead-end file/clipboard export — pass --static for a read-only page.
-o/--output and --artifact always write a static file.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		out, _ := cmd.Flags().GetString("output")
		open, _ := cmd.Flags().GetBool("open")
		artifact, _ := cmd.Flags().GetBool("artifact")
		static, _ := cmd.Flags().GetBool("static")
		if len(args) == 1 {
			slug := args[0]
			// A human opening a SAVED plan gets the live review LOOP by default, so
			// marks write back to the ledger instead of a dead-end file/clipboard
			// export. --static keeps the read-only page; --artifact / -o / headless
			// are non-interactive and stay static.
			if open && !static && !artifact && out == "" && !cli.IsHeadless() {
				return runPlanReview(cmd, slug, false, planReviewDefaultTimeout)
			}
			return runPlanRenderSaved(cmd, slug, out, open, artifact)
		}
		file, _ := cmd.Flags().GetString("file")
		return runPlanRenderFresh(cmd, file, out, open, artifact)
	},
}

var planSaveCmd = &cobra.Command{
	Use:    "save",
	Hidden: true, // agent/skill tier: persist merged badges; taught via prime, not human help
	Short:  "Persist a fully-enriched plan (merged badges + optional HTML) to the ledger",
	Long: `Persist a fully-enriched plan to the ledger. Unlike bare 'ox plan' — which
auto-saves only the deterministic, ox-computed annotations — 'ox plan save' is the
explicit full-plan persist path used by the html-plan renderer skill after it has
authored its judgment badges and (optionally) rendered the HTML.

  --plan        the plan markdown (source for plan.md + topic/slug derivation)
  --annotations a MERGED annotations.json: the 'ox plan enrich --json' Result with the
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

var planLintCmd = &cobra.Command{
	Use:    "lint <slug>",
	Hidden: true, // agent/CI tier: quality gate; taught via prime + CI docs, not human help
	Short:  "Check a saved plan's HTML render for SageOx attribution + self-contained invariants",
	Long: `Lint a saved plan's rendered HTML against the html-plan attribution contract:
when the plan carried SageOx enrichment the render must credit it (footer line +
an anchored OX marker), an un-enriched plan must not overclaim, and the SageOx
mark must be self-contained (no live remote avatar). Advisory by default; pass
--strict to exit non-zero on findings (for CI / golden checks).`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		strict, _ := cmd.Flags().GetBool("strict")
		return runPlanLint(cmd, args[0], strict)
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
	return savePlanWithProvenance(gitRoot, in, result, nil)
}

// savePlanWithProvenance is the shared capture path: it stamps the plan with
// provenance (session/agent/repo) + deterministic collaboration signals, writes
// it to the ledger (read-merge preserving lifecycle), records the reverse link
// on the live recording, and durably commits + pushes the plan dir. Returns the
// saved directory, or "" on any failure (capture is best-effort and never
// aborts the command the agent is waiting on).
func savePlanWithProvenance(gitRoot string, in plan.Input, result plan.Result, html []byte) string {
	topic := planTopic(in)
	slug := plan.Slugify(topic)

	prov, recState := resolvePlanProvenance(gitRoot)
	collab := deriveCollabSignals(recState)

	meta := plan.Meta{
		Topic:          topic,
		Slug:           slug,
		Authors:        planAuthors(gitRoot),
		CreatedAt:      time.Now().UTC(),
		SourcePlanPath: in.Path,
		Provenance:     prov,
		Collaboration:  collab,
	}

	dir, err := plan.Save(gitRoot, in, result, html, meta)
	if err != nil {
		return ""
	}

	// reverse link: record the slug on the live recording so it folds into the
	// session's meta.json at stop (no-op if there's no live recording).
	if prov != nil && prov.SessionName != "" {
		_ = appendProducedPlan(gitRoot, prov.AgentID, slug)
	}

	// durability: commit + push the plan dir now (sync). Best-effort — a push
	// failure leaves the local commit for the next push / `ox doctor`.
	if err := commitPlanToLedger(gitRoot, dir); err != nil {
		slog.Warn("plan: commit/push failed, deferring to next push/doctor", "error", err, "dir", dir)
	}

	slog.Info("plan_saved_provenance",
		"slug", slug,
		"session", provSessionLabel(prov),
		"agent_id", provAgentLabel(prov),
		"user_prompts", collabCount(collab, "user_prompts"),
		"agent_questions", collabCount(collab, "agent_questions"),
		"tool_calls", collabCount(collab, "tool_calls"),
		"duration_s", collabCount(collab, "duration_seconds"))

	return dir
}

// provSessionLabel / provAgentLabel render provenance fields for structured
// logs without panicking on a nil provenance.
func provSessionLabel(p *plan.Provenance) string {
	if p == nil {
		return ""
	}
	if p.SessionID != "" {
		return p.SessionID
	}
	return p.SessionName
}

func provAgentLabel(p *plan.Provenance) string {
	if p == nil {
		return ""
	}
	return p.AgentID
}

// collabCount reads a single collaboration count for logging; 0 when absent.
func collabCount(c *plan.CollabSignals, field string) int {
	if c == nil {
		return 0
	}
	switch field {
	case "user_prompts":
		return c.UserPrompts
	case "agent_questions":
		return c.AgentQuestions
	case "tool_calls":
		return c.ToolCalls
	case "duration_seconds":
		return c.DurationSeconds
	}
	return 0
}

// runPlanSave persists a fully-enriched plan to the ledger from a plan markdown
// file, a MERGED annotations.json (deterministic + judgment badges), and an
// optional pre-rendered HTML file. This is the explicit full-plan persist path
// the html-plan skill calls — it always saves (no auto-save config gate) and never
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

	// The skill path always persists (no plan.save gate) and reuses the shared
	// provenance/collaboration + read-merge + commit path so the hook's draft
	// and the skill's full save converge on the same dated-slug dir.
	gitRoot := findGitRoot()
	dir := savePlanWithProvenance(gitRoot, in, result, html)
	if dir == "" {
		return fmt.Errorf("save plan: no ledger configured for %q or write failed", gitRoot)
	}

	slog.Info("plan_saved", "dir", dir, "html", htmlPath != "", "annotations", len(result.Annotations))
	fmt.Fprintf(cmd.OutOrStdout(), "Saved plan to ledger: %s\n", dir)

	// Branding guarantee: every render the skill saves is checked for the
	// earned-and-conditional SageOx attribution. Warn-only — a missing credit
	// must never block the save (fail-open). Run `ox plan lint <slug>` to recheck.
	for _, f := range plan.LintRender(html, result) {
		cli.PrintHint(fmt.Sprintf("plan-lint [%s]: %s", f.Rule, f.Message))
	}
	return nil
}

// runPlanLint loads a saved plan's HTML render and reports SageOx-attribution
// findings. Advisory by default; --strict makes it exit non-zero on findings so
// a golden check or CI step can enforce the contract. Fail-open on a missing or
// LFS-dehydrated render (nothing local to lint).
func runPlanLint(cmd *cobra.Command, slug string, strict bool) error {
	out := cmd.OutOrStdout()
	gitRoot := findGitRoot()

	_, res, info, err := plan.Load(gitRoot, slug)
	if err != nil {
		return err
	}

	path, _, isPointer, exists := plan.PlanHTMLPath(info.Dir)
	if !exists {
		fmt.Fprintln(out, "No HTML render for this plan — nothing to lint.")
		return nil
	}
	if isPointer {
		cli.PrintHint("This plan's HTML is stored in LFS and not hydrated locally; cannot lint its content.")
		return nil
	}
	html, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read plan html %q: %w", path, err)
	}

	findings := plan.LintRender(html, res)
	if len(findings) == 0 {
		fmt.Fprintln(out, cli.StyleSuccess.Render("✓")+" SageOx attribution OK")
		return nil
	}
	for _, f := range findings {
		fmt.Fprintf(out, "%s [%s] %s\n", cli.StyleWarning.Render("!"), f.Rule, f.Message)
	}
	if strict {
		return fmt.Errorf("%d branding lint finding(s)", len(findings))
	}
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
// an enriched HTML render is available via the html-plan skill. The render
// recommendation fires when EITHER team-context signals (Material) OR structural
// substance (NonTrivial) warrant a human-review render — the same two axes the
// ExitPlanMode nudge uses, so porcelain and hook stay consistent.
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

	if s.Material || s.NonTrivial {
		lead := "Substantial plan."
		if s.Material {
			lead = "Material signals found."
		}
		fmt.Fprintf(&b, "\n%s Render a SageOx team-context-optimized plan with `ox plan render --open`, then start a live review loop with `ox plan review <slug>` — the human marks it up in the browser and the AI coworker receives it in-turn via `ox plan review await <slug>`, addressing each item live. `await` BLOCKS for feedback, so the coworker should confirm with the user before entering that loop (or use a short --timeout and poll).\n", lead)
	}

	fmt.Fprint(out, b.String())
	return nil
}

// openSavedPlanHTML backs `ox plan render --open`: it opens the render of the plan the
// porcelain path just saved, mirroring `ox plan view --open` but off the saved
// directory. Best-effort — enrichment already succeeded, so a missing render or
// a headless shell prints a hint instead of erroring.
func openSavedPlanHTML(cmd *cobra.Command, savedDir string) {
	if savedDir == "" {
		cli.PrintHint("No saved plan to open (plan capture is off or no ledger is configured).")
		return
	}
	path, _, _, exists := plan.PlanHTMLPath(savedDir)
	if !exists {
		cli.PrintHint("No HTML render yet — run `ox plan render --open` to render one.")
		return
	}
	if cli.IsHeadless() {
		fmt.Fprintf(cmd.OutOrStdout(), "Rendered HTML: %s\n", path)
		return
	}
	if err := openPlanHTML(savedDir); err != nil {
		cli.PrintHint("Could not open the rendered plan: " + err.Error())
	}
}

// runPlanRenderFresh renders a plan resolved from --file/stdin (enriching it
// first), persists it to the ledger when capture is on, and writes/opens per the
// flags. This is the cross-agent path: any agent gets the rich render here. The
// render injects SageOx attribution by construction (passes the lint contract).
// priorArtURLResolver builds the closure RenderOptions.PriorArtURL uses to turn a
// prior-art source into a SageOx web link (opened in a new tab from the enrichment
// panel). Sessions resolve to the canonical session view; murmurs have no
// individual web view, so they render unlinked; plans will get a web route soon
// (see TODO). Project config is loaded once; when it's unavailable the resolver
// yields "" and the render degrades to crisp text rather than failing.
func priorArtURLResolver(gitRoot string) func(refKind, ref string) string {
	cfg, _ := config.LoadProjectConfig(gitRoot)
	return func(refKind, ref string) string {
		switch refKind {
		case "session":
			return buildSessionURL(cfg, ref)
		// TODO(plan-web-route): when SageOx ships a per-plan web view, add
		// buildPlanURL(cfg, ref) here keyed on "plan" (bd: follow-up issue). The
		// resolver already branches on refKind, so it's a drop-in.
		default:
			return ""
		}
	}
}

func runPlanRenderFresh(cmd *cobra.Command, file, outPath string, open, artifact bool) error {
	in, err := plan.Resolve(file, cmd.InOrStdin())
	if err != nil {
		return err
	}
	if strings.TrimSpace(in.Raw) == "" {
		fmt.Fprintln(cmd.OutOrStdout(),
			"No plan found. Pass --file <plan.md>, pipe a plan on stdin, or save a plan-mode file to ~/.claude/plans/ first.")
		return nil
	}
	gitRoot := findGitRoot()
	result := plan.Enrich(context.Background(), in, gitRoot)

	htmlBytes, err := plan.RenderHTMLOpts(in, result, plan.RenderOptions{Slug: plan.Slugify(planTopic(in)), PriorArtURL: priorArtURLResolver(gitRoot), Artifact: artifact})
	if err != nil {
		return fmt.Errorf("render plan: %w", err)
	}
	// Surface broken/non-portable Mermaid (the page swallows render errors).
	for _, f := range plan.LintMermaidMarkdown(in.Raw) {
		cli.PrintHint(fmt.Sprintf("plan-diagram [%s]: %s", f.Rule, f.Message))
	}
	// Cross-agent design-craft check: did the render realize the visual craft ox
	// expected at enrich (a suggested diagram, a user-facing surface)? Record the
	// hint→realization metric, then surface any gaps as advisory nudges — never blocks.
	craft := plan.CraftRealization(result, htmlBytes)
	plan.RecordPlanCraft(craft)
	for _, f := range craft.Gaps {
		cli.PrintHint(fmt.Sprintf("plan-craft [%s]: %s", f.Rule, f.Message))
	}
	// Artifact mode is a pure export target — leave the ledger's canonical
	// (review-capable) render untouched, and check the page is CSP-clean.
	savedDir := ""
	if artifact {
		for _, f := range plan.LintArtifact(htmlBytes) {
			cli.PrintHint(fmt.Sprintf("plan-artifact [%s]: %s", f.Rule, f.Message))
		}
	} else if gitRoot != "" && config.PlanSave(gitRoot) {
		// Persist into the ledger when capture is on, so the render is re-openable.
		savedDir = savePlanWithProvenance(gitRoot, in, result, htmlBytes)
	}
	name := "plan"
	if in.Path != "" {
		name = strings.TrimSuffix(filepath.Base(in.Path), filepath.Ext(in.Path))
	}
	emitRenderedHTML(cmd, htmlBytes, savedDir, outPath, open, name)
	return nil
}

// runPlanRenderSaved renders a plan already in the ledger (with its review
// state), writing/opening per the flags. It never re-persists.
func runPlanRenderSaved(cmd *cobra.Command, slug, outPath string, open, artifact bool) error {
	gitRoot := findGitRoot()
	planMD, res, info, err := plan.Load(gitRoot, slug)
	if err != nil {
		return fmt.Errorf("load plan %q: %w", slug, err)
	}
	in := plan.Parse(planMD)
	review, _ := plan.AssembleReview(info.Dir)
	htmlBytes, err := plan.RenderHTMLOpts(in, res, plan.RenderOptions{Slug: slug, Review: review, PriorArtURL: priorArtURLResolver(gitRoot), Artifact: artifact})
	if err != nil {
		return fmt.Errorf("render plan: %w", err)
	}
	// Artifact mode is a pure export of the CSP-safe bytes — never open the
	// ledger's canonical (non-artifact) plan.html in its place, so pass no
	// savedDir and let emitRenderedHTML serve the artifact bytes directly.
	savedDir := info.Dir
	if artifact {
		savedDir = ""
		for _, f := range plan.LintArtifact(htmlBytes) {
			cli.PrintHint(fmt.Sprintf("plan-artifact [%s]: %s", f.Rule, f.Message))
		}
	}
	emitRenderedHTML(cmd, htmlBytes, savedDir, outPath, open, slug)
	return nil
}

// emitRenderedHTML writes the render to outPath (when set) and opens it (when
// open). For opening it prefers a plain-file ledger render, else the explicit
// path, else a temp file backed by htmlBytes — so --open always has real HTML to
// show even when the saved ledger copy is an LFS pointer. Headless prints the
// path instead of opening.
func emitRenderedHTML(cmd *cobra.Command, htmlBytes []byte, savedDir, outPath string, open bool, name string) {
	if outPath != "" {
		if werr := os.WriteFile(outPath, htmlBytes, 0o644); werr != nil {
			cli.PrintHint("Could not write render to " + outPath + ": " + werr.Error())
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "Rendered HTML: %s\n", outPath)
		}
	}
	if !open {
		return
	}
	// Encourage the live review loop once the page is in front of a human — the
	// slug is real only when the plan is in the ledger (capture on).
	if savedDir != "" && !cli.IsHeadless() {
		cli.PrintHint("Start a live review loop: `ox plan review " + filepath.Base(savedDir) + "` — mark it up in the browser; your AI coworker receives it via `ox plan review await " + filepath.Base(savedDir) + "` and addresses each item live (it blocks for feedback — the coworker should confirm before entering that wait).")
	}
	if savedDir != "" {
		if _, _, isPointer, exists := plan.PlanHTMLPath(savedDir); exists && !isPointer {
			openSavedPlanHTML(cmd, savedDir)
			return
		}
	}
	target := outPath
	if target == "" {
		target = filepath.Join(os.TempDir(), "ox-plan-"+plan.Slugify(name)+".html")
		if werr := os.WriteFile(target, htmlBytes, 0o644); werr != nil {
			cli.PrintHint("Could not write render: " + werr.Error())
			return
		}
	}
	if cli.IsHeadless() {
		fmt.Fprintf(cmd.OutOrStdout(), "Rendered HTML: %s\n", target)
		return
	}
	if oerr := cli.OpenInBrowser(target); oerr != nil {
		cli.PrintHint("Could not open the rendered plan: " + oerr.Error())
	}
}

// runPlanList renders the saved plans as a table, or JSON with --json (scripting
// path). Fail-open: outside a project or with no plans, it prints a friendly
// empty-state instead of erroring.
func runPlanList(cmd *cobra.Command, jsonOut bool) error {
	out := cmd.OutOrStdout()
	gitRoot := findGitRoot()

	plans, err := plan.List(gitRoot)
	if err != nil {
		return fmt.Errorf("list plans: %w", err)
	}
	if jsonOut {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if plans == nil {
			plans = []plan.PlanInfo{}
		}
		return enc.Encode(plans)
	}
	if len(plans) == 0 {
		fmt.Fprintln(out, "No saved plans yet. Run 'ox plan enrich --text' on an implementation plan to capture one.")
		return nil
	}

	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, cli.StyleDim.Render("SLUG\tDATE\tHTML\tREVIEW\tAUTHORS\tTOPIC"))
	anyOpen := false
	for _, p := range plans {
		open := openReviewCount(p.Dir)
		if open > 0 {
			anyOpen = true
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			p.Slug,
			planDate(p.CreatedAt),
			htmlMark(p.HasHTML),
			reviewMark(open),
			authorsLabel(p.Authors),
			truncate(p.Topic, 60),
		)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if anyOpen {
		cli.PrintHint("Plans with open review items — `ox plan feedback show <slug>`, address, then `ox plan feedback resolve`.")
	}
	return nil
}

// openReviewCount returns the number of OPEN, actionable review items for a plan
// dir (approvals don't count). Thin delegate to the ledger-side counter so the
// list table and the discovery aggregator can't drift.
func openReviewCount(planDir string) int {
	return plan.CountOpenFeedback(planDir)
}

// countOpenPlanFeedback returns how many saved plans have open human review
// feedback — the count `ox agent prime` surfaces so a NEW session (any agent)
// discovers waiting feedback even when the push task missed. Best-effort: 0 on
// any error (outside a project, no ledger, unreadable plans).
func countOpenPlanFeedback(gitRoot string) int {
	if gitRoot == "" {
		return 0
	}
	plans, err := plan.OpenFeedbackPlans(gitRoot)
	if err != nil {
		return 0
	}
	return len(plans)
}

// reviewMark renders the open-review count for the list table.
func reviewMark(open int) string {
	if open == 0 {
		return "—"
	}
	return fmt.Sprintf("%d open", open)
}

// runPlanView prints a saved plan's markdown plus a badge summary in the
// terminal. To open the HTML render in a browser, use `ox plan render <slug>
// --open` (view is a pure terminal reader).
func runPlanView(cmd *cobra.Command, slug string) error {
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
	if meta, err := plan.ReadPlanMeta(gitRoot, info.Slug); err == nil {
		if line := planProvenanceLine(meta); line != "" {
			fmt.Fprintln(out, cli.StyleDim.Render(line))
		}
		if line := planCollabLine(meta); line != "" {
			fmt.Fprintln(out, cli.StyleDim.Render(line))
		}
	}
	fmt.Fprintln(out)

	fmt.Fprintln(out, planMD)

	writeBadgeSummary(out, res)

	htmlPath, _, _, hasHTML := plan.PlanHTMLPath(info.Dir)
	if !hasHTML {
		return nil
	}

	fmt.Fprintf(out, "\nRendered HTML: %s\n", cli.StyleFile.Render(htmlPath))
	cli.PrintHint("Open it in your browser with `ox plan render " + slug + " --open`.")
	return nil
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

// planProvenanceLine renders the one-line forward link (session · agent · model
// · outcome) for a saved plan, or "" when the plan carries no provenance.
func planProvenanceLine(meta plan.Meta) string {
	p := meta.Provenance
	if p == nil {
		return ""
	}
	var parts []string
	switch {
	case p.SessionID != "":
		parts = append(parts, "session: "+p.SessionID)
	case p.SessionName != "":
		parts = append(parts, "session: "+p.SessionName)
	}
	if p.AgentID != "" {
		parts = append(parts, "agent: "+p.AgentID)
	}
	if p.Model != "" {
		parts = append(parts, "model: "+p.Model)
	}
	if p.SessionOutcome != "" && p.SessionOutcome != plan.SessionOutcomeActive {
		parts = append(parts, "session "+p.SessionOutcome)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ")
}

// planCollabLine renders the one-line collaboration fingerprint (status +
// effort proxies) for a saved plan, or "" when there are no collaboration
// signals.
func planCollabLine(meta plan.Meta) string {
	c := meta.Collaboration
	if c == nil {
		return ""
	}
	parts := []string{
		fmt.Sprintf("%d prompts", c.UserPrompts),
		fmt.Sprintf("%d questions", c.AgentQuestions),
		fmt.Sprintf("%d tool calls", c.ToolCalls),
	}
	if c.DurationSeconds > 0 {
		parts = append(parts, fmt.Sprintf("%ds", c.DurationSeconds))
	}
	prefix := ""
	if meta.Status != "" {
		prefix = string(meta.Status) + " · "
	}
	return "collaboration: " + prefix + strings.Join(parts, " · ")
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
	// enrich: JSON by default; --text for humans.
	planEnrichCmd.Flags().String("file", "", "plan source file (default: stdin, else newest ~/.claude/plans/*.md)")
	planEnrichCmd.Flags().Bool("text", false, "human-readable summary instead of the default JSON output")
	planEnrichCmd.Flags().Bool("persist", false, "also save + commit a draft to the ledger (used by the ExitPlanMode hook)")
	planEnrichCmd.Flags().Bool("json", false, "(deprecated; JSON is the default) emit the Result as JSON")
	_ = planEnrichCmd.Flags().MarkHidden("json")

	// render: single HTML entry point.
	planRenderCmd.Flags().String("file", "", "plan source file when no slug is given (default: stdin, else newest ~/.claude/plans/*.md)")
	planRenderCmd.Flags().StringP("output", "o", "", "write the rendered HTML to this path")
	planRenderCmd.Flags().Bool("open", false, "open the rendered HTML in your browser")
	planRenderCmd.Flags().Bool("static", false, "with --open on a saved plan, open a read-only static page instead of launching the live review loop")
	planRenderCmd.Flags().Bool("artifact", false, "render a strictly self-contained, CSP-safe page for publishing as a Claude Code Artifact (no external fonts/scripts, no review loop; enrichment links preserved)")

	planListCmd.Flags().Bool("json", false, "emit the plan list as JSON (scripting path)")

	planSaveCmd.Flags().String("plan", "", "plan markdown file (required; source for plan.md + topic/slug)")
	planSaveCmd.Flags().String("annotations", "", "merged annotations.json: enrich badges + agent judgment badges (required)")
	planSaveCmd.Flags().String("html", "", "optional pre-rendered HTML; size-gated plain-git-vs-LFS on save")

	planLintCmd.Flags().Bool("strict", false, "exit non-zero when the render has attribution findings (for CI / golden checks)")

	planCmd.AddCommand(planEnrichCmd)
	planCmd.AddCommand(planRenderCmd)
	planCmd.AddCommand(planListCmd)
	planCmd.AddCommand(planViewCmd)
	planCmd.AddCommand(planSaveCmd)
	planCmd.AddCommand(planLintCmd)

	planCmd.GroupID = "dev"
	rootCmd.AddCommand(planCmd)
}
