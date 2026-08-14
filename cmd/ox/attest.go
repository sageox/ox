package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/attest"
	"github.com/sageox/ox/internal/repotools"
	"github.com/sageox/ox/internal/ui"
	"github.com/spf13/cobra"
)

var attestCmd = &cobra.Command{
	Use:   "attest",
	Short: "What this repo can actually demonstrate",
	Long: `Read this repo's Attest corpus and report what it can demonstrate.

The unit is the CAPABILITY — a Gherkin ` + "`Rule:`" + ` block, a claim a customer
would recognize — not the test. Reads committed files only: no Node toolchain,
no network, no SageOx account required.

  ox attest status                 the capability ladder for this repo
  ox attest proof <capability>     the claim, the break, and the verbatim failure
  ox attest check                  what does my working diff invalidate
  ox attest record                 mint an attestation from a red/green run pair
  ox attest results                run reports on this machine
  ox attest publish                write the portable layout to a directory`,
}

// attestContext is everything the read commands need, loaded once.
type attestContext struct {
	RepoRoot   string
	CorpusRoot string
	Corpus     *attest.Corpus
	Plans      *attest.Plans
	Records    *attest.Records
}

// resolveCorpusRoot honors --corpus, absolute or repo-relative, and otherwise
// falls back to the Attest convention.
func resolveCorpusRoot(cmd *cobra.Command, repoRoot string) string {
	corpusFlag, _ := cmd.Flags().GetString("corpus")
	if corpusFlag == "" {
		return filepath.Join(repoRoot, attest.DefaultCorpusDir)
	}
	if filepath.IsAbs(corpusFlag) {
		return corpusFlag
	}
	return filepath.Join(repoRoot, corpusFlag)
}

// loadAttestContext resolves the repo, the corpus, and every committed artifact.
func loadAttestContext(cmd *cobra.Command) (*attestContext, error) {
	root, err := repotools.FindRepoRoot(repotools.VCSGit)
	if err != nil {
		return nil, fmt.Errorf("not in a git repository")
	}
	corpusRoot := resolveCorpusRoot(cmd, root)

	corpus, err := attest.ScanCorpus(root, corpusRoot)
	if err != nil {
		return nil, fmt.Errorf("%w\n  hint: pass --corpus <dir> if this project keeps its Attest corpus elsewhere", err)
	}
	plans, err := attest.LoadPlans(corpusRoot)
	if err != nil {
		return nil, err
	}
	records, err := attest.LoadRecords(corpusRoot)
	if err != nil {
		return nil, err
	}
	return &attestContext{
		RepoRoot: root, CorpusRoot: corpusRoot,
		Corpus: corpus, Plans: plans, Records: records,
	}, nil
}

// wantJSON resolves the --json flag, defaulting to ON when an agent is calling.
// Agents parse; humans read. Mirrors `ox code insights`.
func wantJSON(cmd *cobra.Command) (bool, string) {
	jsonOut, _ := cmd.Flags().GetBool("json")
	agentID, _ := detectAgentContext()
	if agentID != "" && !cmd.Flags().Changed("json") {
		jsonOut = true
	}
	return jsonOut, agentID
}

// emit writes either indented JSON or the rendered human text, and records the
// context cost when an agent is on the other end.
func emit(jsonOut bool, agentID string, payload any, human string, label string) error {
	var outputBytes int
	if jsonOut {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")
		if err := enc.Encode(payload); err != nil {
			return err
		}
		outputBytes = buf.Len()
		fmt.Print(buf.String())
	} else {
		outputBytes = len(human)
		fmt.Print(human)
	}
	if agentID != "" {
		slog.Debug(label+" context cost", "agent_id", agentID, "bytes", outputBytes)
		trackContextBytes(int64(outputBytes))
	}
	return nil
}

// addCorpusFlag registers the corpus override shared by every subcommand.
func addCorpusFlag(cmd *cobra.Command) {
	cmd.Flags().String("corpus", "", "corpus directory relative to the repo root (default tests/acceptance)")
}

var attestStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "The capability ladder for this repo",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, err := loadAttestContext(cmd)
		if err != nil {
			return err
		}
		report := attest.BuildReport(ctx.Corpus, ctx.Plans, ctx.Records)

		jsonOut, agentID := wantJSON(cmd)
		domain, _ := cmd.Flags().GetString("domain")
		weakest, _ := cmd.Flags().GetInt("weakest")
		return emit(jsonOut, agentID, report, renderAttestStatus(report, domain, weakest), "attest status")
	},
}

// verdictGlyph carries shape as well as color, so a verdict survives being read
// in grayscale, piped to a file, or spoken by a screen reader. The word is
// always printed beside it — the hue is never the only signal.
func verdictGlyph(v attest.Verdict) string {
	switch v {
	case attest.VerdictAttested:
		return "✓"
	case attest.VerdictStamped:
		return "◌"
	case attest.VerdictUnproven:
		return "◉"
	case attest.VerdictSkipped:
		return "▲"
	case attest.VerdictUntested:
		return "◍"
	default:
		return "·"
	}
}

func renderVerdict(v attest.Verdict, text string) string {
	switch v {
	case attest.VerdictAttested:
		return ui.RenderPass(text)
	case attest.VerdictStamped, attest.VerdictUnproven:
		return ui.RenderWarn(text)
	case attest.VerdictSkipped, attest.VerdictUntested:
		return ui.RenderFail(text)
	default:
		return text
	}
}

func renderAttestStatus(r *attest.Report, domain string, weakest int) string {
	var b strings.Builder

	fmt.Fprintf(&b, "\n%s  %d capabilities · %d feature files · %d domains · %d compiled plans · %d records\n\n",
		ui.RenderAccent("Attest"), r.Capabilities, r.Files, len(r.Domains), r.Plans, r.Records)

	// Ladder, worst last so the eye lands on the strongest claim first.
	for i := len(attest.VerdictOrder) - 1; i >= 0; i-- {
		v := attest.VerdictOrder[i]
		label := fmt.Sprintf("%s %-9s", verdictGlyph(v), string(v))
		fmt.Fprintf(&b, "  %s %4d   %s\n",
			renderVerdict(v, label), r.Counts[v], ui.RenderMuted(v.Meaning()))
	}

	fmt.Fprintf(&b, "\n  %s  %d authored · %d dispatch · %d stamped\n",
		ui.RenderMuted("scenarios"), r.Scenarios.Authored, r.Scenarios.Dispatching, r.Scenarios.Stamped)

	if r.Scenarios.Unprovenanced > 0 {
		fmt.Fprintf(&b, "  %s\n", ui.RenderWarn(fmt.Sprintf(
			"%d stamped with no date and no run id — that claim cannot even be aged",
			r.Scenarios.Unprovenanced)))
	}

	// The honest caveat, printed every time rather than buried in docs: this
	// denominator is the capabilities somebody AUTHORED, not the product's real
	// surface area. A capability nobody wrote a Rule for cannot appear here at
	// all, so a full bar is not the same as a defended product.
	fmt.Fprintf(&b, "  %s\n",
		ui.RenderMuted("denominator is authored capabilities, not the product's full surface"))

	// An unreadable record is a proof that silently vanishes — it must never be
	// quietly absent from the counts.
	for path, reason := range r.InvalidRecords {
		fmt.Fprintf(&b, "  %s\n", ui.RenderFail(fmt.Sprintf("unreadable record %s: %s", filepath.Base(path), reason)))
	}

	if r.Counts[attest.VerdictAttested] == 0 {
		fmt.Fprintf(&b, "\n  %s\n",
			ui.RenderWarn("no attestation records yet — a stamp names a run id that nothing here can open"))
	}

	list := r.Weakest(weakest)
	if domain != "" {
		// Copy rather than alias the report's slice: the caller may render more
		// than once, and truncating in place would silently shrink the report.
		list = append([]attest.Assessment(nil), r.ByDomain[domain]...)
		if weakest > 0 && len(list) > weakest {
			list = list[:weakest]
		}
	}

	if len(list) > 0 {
		heading := "weakest first"
		if domain != "" {
			heading = "domain " + domain
		}
		fmt.Fprintf(&b, "\n  %s\n", ui.RenderMuted(heading))
		for _, a := range list {
			title := a.Capability.Title()
			if len(title) > 72 {
				title = title[:69] + "..."
			}
			fmt.Fprintf(&b, "    %s  %s\n",
				renderVerdict(a.Verdict, verdictGlyph(a.Verdict)), title)
			detail := fmt.Sprintf("%s · %d/%d dispatch", a.Capability.ID,
				a.Dispatching, len(a.Capability.Scenarios))
			if a.NoPlan {
				detail += " · no compiled plan"
			}
			if a.NewestStamp != nil && a.NewestStamp.Date != "" {
				detail += " · stamped " + a.NewestStamp.Date
			}
			if a.Record != nil {
				detail += " · proof " + a.Record.Proof.Verdict
			}
			fmt.Fprintf(&b, "       %s\n", ui.RenderMuted(detail))
		}
	}

	fmt.Fprintln(&b)
	return b.String()
}

func init() {
	attestStatusCmd.Flags().Bool("json", false, "structured JSON output for agents")
	attestStatusCmd.Flags().String("domain", "", "list capabilities from one domain instead of the weakest overall")
	attestStatusCmd.Flags().Int("weakest", 10, "how many capabilities to list (0 for all)")
	addCorpusFlag(attestStatusCmd)

	attestCmd.AddCommand(attestStatusCmd)
	attestCmd.GroupID = "dev"
	rootCmd.AddCommand(attestCmd)
}
