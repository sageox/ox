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
no network, no SageOx account required.`,
}

var attestStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "The capability ladder for this repo",
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := repotools.FindRepoRoot(repotools.VCSGit)
		if err != nil {
			return fmt.Errorf("not in a git repository")
		}

		corpusFlag, _ := cmd.Flags().GetString("corpus")
		corpusRoot := filepath.Join(root, attest.DefaultCorpusDir)
		if corpusFlag != "" {
			if filepath.IsAbs(corpusFlag) {
				corpusRoot = corpusFlag
			} else {
				corpusRoot = filepath.Join(root, corpusFlag)
			}
		}

		corpus, err := attest.ScanCorpus(root, corpusRoot)
		if err != nil {
			return fmt.Errorf("%w\n  hint: pass --corpus <dir> if this project keeps its Attest corpus elsewhere", err)
		}
		plans, err := attest.LoadPlans(corpusRoot)
		if err != nil {
			return err
		}
		report := attest.BuildReport(corpus, plans)

		jsonOut, _ := cmd.Flags().GetBool("json")
		// Auto-detect: default to JSON when an agent is calling, matching
		// `ox code insights`. Agents parse; humans read.
		agentID, _ := detectAgentContext()
		if agentID != "" && !cmd.Flags().Changed("json") {
			jsonOut = true
		}

		domain, _ := cmd.Flags().GetString("domain")
		weakest, _ := cmd.Flags().GetInt("weakest")

		var outputBytes int
		if jsonOut {
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetIndent("", "  ")
			if err := enc.Encode(report); err != nil {
				return err
			}
			outputBytes = buf.Len()
			fmt.Print(buf.String())
		} else {
			rendered := renderAttestStatus(report, domain, weakest)
			outputBytes = len(rendered)
			fmt.Print(rendered)
		}

		if agentID != "" {
			slog.Debug("attest status context cost", "agent_id", agentID, "bytes", outputBytes)
			trackContextBytes(int64(outputBytes))
		}
		return nil
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

	fmt.Fprintf(&b, "\n%s  %d capabilities · %d feature files · %d domains · %d compiled plans\n\n",
		ui.RenderAccent("Attest"), r.Capabilities, r.Files, len(r.Domains), r.Plans)

	// Ladder, worst last so the eye lands on the strongest claim first, and the
	// count column is right-aligned so the distribution is readable as a shape.
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
	// surface area. A capability nobody has written a Rule for cannot appear
	// here at all, so a full bar is not the same as a defended product.
	fmt.Fprintf(&b, "  %s\n",
		ui.RenderMuted("denominator is authored capabilities, not the product's full surface"))

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
			fmt.Fprintf(&b, "       %s\n", ui.RenderMuted(detail))
		}
	}

	fmt.Fprintln(&b)
	return b.String()
}

func init() {
	attestStatusCmd.Flags().Bool("json", false, "structured JSON output for agents")
	attestStatusCmd.Flags().String("corpus", "", "corpus directory relative to the repo root (default tests/acceptance)")
	attestStatusCmd.Flags().String("domain", "", "list capabilities from one domain instead of the weakest overall")
	attestStatusCmd.Flags().Int("weakest", 10, "how many capabilities to list (0 for all)")

	attestCmd.AddCommand(attestStatusCmd)
	attestCmd.GroupID = "dev"
	rootCmd.AddCommand(attestCmd)
}
