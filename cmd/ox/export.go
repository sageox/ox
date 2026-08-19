package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/ui"
	"github.com/spf13/cobra"
)

// exportPhilosophy is the one-line "you own your data" statement surfaced in
// both the JSON output and (expanded) the human output. SageOx stores all
// customer context in git repos the user fully controls — this command makes
// that ownership legible.
const exportPhilosophy = "SageOx is not a walled garden. Your Ledger and Team Contexts are ordinary " +
	"git repositories that you fully own — clone them, read them, push them to your own remote, " +
	"and take them anywhere. No proprietary format, no lock-in."

// exportOutput is the JSON payload for `ox export --json`.
type exportOutput struct {
	Philosophy   string              `json:"philosophy"`
	Ledgers      []exportLedger      `json:"ledgers"`
	TeamContexts []exportTeamContext `json:"team_contexts"`
	Synced       bool                `json:"synced"`
	Guidance     string              `json:"guidance,omitempty"`
	Note         string              `json:"note,omitempty"`
}

// exportLedger is one on-disk ledger checkout.
type exportLedger struct {
	RepoID  string `json:"repo_id,omitempty"`
	Path    string `json:"path"`
	Symlink string `json:"symlink,omitempty"` // short in-repo path (e.g. .sageox/ledger) if one resolves here
	Primary bool   `json:"primary"`           // true = this repo's ledger
}

// exportTeamContext is one on-disk team context checkout.
type exportTeamContext struct {
	TeamID  string `json:"team_id"`
	Name    string `json:"name"`
	Slug    string `json:"slug,omitempty"`
	Path    string `json:"path"`
	Primary bool   `json:"primary"` // true = this repo's team
}

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Show where your data lives and how to take it with you",
	Long: `Show where all your SageOx data lives on disk and how to access it locally.

SageOx is not a walled garden. Everything an AI coworker learns about your
work is stored in ordinary git repositories that you fully own and control:

  Ledger        this repo's history of work, decisions, and coding sessions
  Team Context  your team's shared knowledge: norms, conventions, decisions

Both are plain git repos on your machine. You can cd into them, run git log,
git pull, copy them elsewhere, or push them to a remote you control. There is
no proprietary format and no lock-in — your context is yours.

This command shows the exact on-disk location of each repo and how to reach
it. With --sync it first checks out and refreshes every team context and this
repo's ledger, so a copy you make right now is complete and current.`,
	RunE: runExport,
}

func init() {
	exportCmd.Flags().Bool("sync", false, "check out and refresh all team contexts and this repo's ledger before printing")
	exportCmd.Flags().Bool("json", false, "output as JSON")
	rootCmd.AddCommand(exportCmd)
	exportCmd.GroupID = "teams" // sits with `ox teams` — data you own and access
}

func runExport(cmd *cobra.Command, _ []string) error {
	jsonOutput, _ := cmd.Flags().GetBool("json")
	doSync, _ := cmd.Flags().GetBool("sync")
	out := cmd.OutOrStdout()

	projectRoot, _ := findProjectRoot()

	// --sync: ensure everything is checked out and fresh before we print paths.
	// A sync failure is surfaced but never blocks the docs — the user still
	// deserves to see where their data lives and how to reach it. Report the
	// sync we ACTUALLY completed, not the one requested: a failed refresh must
	// not claim synced=true in JSON, and must keep showing the refresh tip.
	syncSucceeded := false
	if doSync {
		if err := runExportSync(projectRoot, jsonOutput); err != nil {
			if !jsonOutput {
				cli.PrintWarning(fmt.Sprintf("Sync incomplete: %v", err))
				cli.PrintHint("Showing current on-disk locations anyway.")
			}
		} else {
			syncSucceeded = true
		}
	}

	ledgers := collectLedgers(projectRoot)
	teams := collectTeamContexts(projectRoot)

	if jsonOutput {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(buildExportOutput(ledgers, teams, syncSucceeded))
	}

	renderExportHuman(out, ledgers, teams, syncSucceeded)
	return nil
}

// buildExportOutput assembles the JSON payload. Pure (no I/O) so the guidance,
// philosophy, and shape are unit-testable without discovery or a daemon.
func buildExportOutput(ledgers []exportLedger, teams []exportTeamContext, synced bool) exportOutput {
	return exportOutput{
		Philosophy:   exportPhilosophy,
		Ledgers:      ledgers,
		TeamContexts: teams,
		Synced:       synced,
		Guidance: "Your data is in git repos at the paths below. cd into any of them and use plain git " +
			"(log, pull, or push to your own remote). Run 'ox export --sync' first to refresh all team " +
			"contexts and this repo's ledger.",
		Note: "Today --sync covers all team contexts + this repo's ledger. Syncing every ledger across " +
			"all your repos is coming.",
	}
}

// collectLedgers returns the ledgers this machine holds for the project's
// endpoint: the current repo's ledger first (marked primary), then any other
// ledgers already cloned locally. Cross-repo enumeration is bounded by what is
// on disk — there is no account-wide ledger listing API yet.
func collectLedgers(projectRoot string) []exportLedger {
	var ledgers []exportLedger
	seen := make(map[string]bool)

	var ep string
	if projectRoot != "" {
		if ctx, err := config.LoadProjectContext(projectRoot); err == nil {
			ep = ctx.Endpoint()
			if primaryPath := ctx.DefaultLedgerPath(); primaryPath != "" {
				short := shortenPathViaSymlink(projectRoot, primaryPath, ".sageox/ledger")
				sym := ""
				if short != primaryPath {
					sym = short
				}
				ledgers = append(ledgers, exportLedger{
					RepoID:  ctx.RepoID(),
					Path:    primaryPath,
					Symlink: sym,
					Primary: true,
				})
				seen[filepath.Clean(primaryPath)] = true
			}
		}
	}

	// any other ledgers already cloned locally under this endpoint. Only real
	// git checkouts named repo_<id> — the enclosing dir also holds operational
	// siblings (.bak.<ts> reclone backups, .gc-cache, .gc-untracked) that are
	// not the user's data and must not be presented as portable repos.
	if ep != "" {
		base := paths.LedgersDataDir("", ep)
		if entries, err := os.ReadDir(base); err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				name := e.Name()
				// repo IDs are repo_<uuid> with no dots; a dot marks a
				// .bak.<ts>/.gc-cache/.gc-untracked operational sibling.
				if !strings.HasPrefix(name, "repo_") || strings.Contains(name, ".") {
					continue
				}
				p := filepath.Join(base, name)
				if seen[filepath.Clean(p)] {
					continue
				}
				if _, err := os.Stat(filepath.Join(p, ".git")); err != nil {
					continue // only actual git checkouts
				}
				ledgers = append(ledgers, exportLedger{RepoID: name, Path: p})
				seen[filepath.Clean(p)] = true
			}
		}
	}
	return ledgers
}

// collectTeamContexts returns all of the user's team contexts. Inside a project
// it uses the daemon-aware discovery; otherwise it falls back to credentials
// across every authenticated endpoint (same sourcing as `ox teams`).
func collectTeamContexts(projectRoot string) []exportTeamContext {
	var teams []enrichedTeam
	if projectRoot != "" {
		teams = discoverAllTeams(projectRoot)
	} else {
		teams = discoverTeamsGlobal()
	}

	result := make([]exportTeamContext, 0, len(teams))
	for _, t := range teams {
		result = append(result, exportTeamContext{
			TeamID:  t.TeamID,
			Name:    t.Name,
			Slug:    t.Slug,
			Path:    t.Path,
			Primary: t.Primary,
		})
	}
	return result
}

// runExportSync checks out and refreshes all team contexts and (inside a
// project) this repo's ledger. Pull operations are daemon-owned, so this
// delegates to the same daemon paths as `ox sync`, auto-starting the daemon
// if needed. Per-repo cross-account ledger sync is deferred (no account-wide
// ledger API yet).
func runExportSync(projectRoot string, jsonOutput bool) error {
	if err := ensureDaemonRunning(jsonOutput); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var result SyncResult // populated by the sync helpers for their side effects
	var problems []string

	if err := syncAllTeamContexts(ctx, jsonOutput, &result); err != nil {
		problems = append(problems, fmt.Sprintf("team contexts: %v", err))
	}
	if projectRoot != "" {
		if err := syncViaDaemon(ctx, jsonOutput, &result); err != nil {
			problems = append(problems, fmt.Sprintf("ledger: %v", err))
		}
	}

	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// ensureDaemonRunning starts the background daemon if it is not already healthy
// and waits briefly for it to come up. Mirrors the auto-start behavior in
// `ox sync` (pull operations require the daemon).
func ensureDaemonRunning(jsonOutput bool) error {
	if daemon.IsHealthy() == nil {
		return nil
	}
	if !jsonOutput {
		fmt.Println("Starting daemon...")
	}
	if err := autoStartDaemon(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	for i := 0; i < 50; i++ {
		if daemon.IsHealthy() == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("daemon did not start in time")
}

// renderExportHuman prints the teaching output: the ownership philosophy, where
// each repo lives, how to reach it with plain git, and related commands.
func renderExportHuman(out io.Writer, ledgers []exportLedger, teams []exportTeamContext, synced bool) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, ui.MutedStyle.Render(ui.SeparatorHeavy))
	fmt.Fprintln(out, cli.StyleBold.Render("Take your data with you"))
	fmt.Fprintln(out, ui.MutedStyle.Render(ui.SeparatorHeavy))
	fmt.Fprintln(out)

	// philosophy — the point of the command
	fmt.Fprintln(out, "SageOx is not a walled garden. Your "+cli.StyleBold.Render("Ledger")+" and "+
		cli.StyleBold.Render("Team Contexts")+" are ordinary")
	fmt.Fprintln(out, "git repositories that you fully own — clone them, read them, push them")
	fmt.Fprintln(out, "to a remote you control, and take them anywhere. No lock-in.")

	// where your data lives
	fmt.Fprintln(out)
	fmt.Fprintln(out, ui.RenderCategory("Where your data lives"))
	fmt.Fprintln(out)

	fmt.Fprintln(out, cli.StyleBold.Render("  Ledger")+cli.StyleDim.Render("  — this repo's work, decisions, and sessions"))
	if len(ledgers) == 0 {
		fmt.Fprintln(out, "    "+cli.StyleDim.Render("(none checked out locally yet — run 'ox export --sync')"))
	}
	var otherLedgers []exportLedger
	for _, l := range ledgers {
		if !l.Primary {
			otherLedgers = append(otherLedgers, l)
			continue
		}
		display := l.Path
		if l.Symlink != "" {
			display = l.Symlink
		}
		fmt.Fprintln(out, "    "+cli.StyleFile.Render(display)+" "+cli.StyleSuccess.Render("★ this repo"))
	}
	// Collapse other locally-cloned ledgers to a single pointer rather than
	// listing every repo you've ever opened.
	if len(otherLedgers) > 0 {
		base := filepath.Dir(otherLedgers[0].Path)
		fmt.Fprintln(out, "    "+cli.StyleDim.Render(fmt.Sprintf("+ %d more ledger(s) you've worked in, under:", len(otherLedgers))))
		fmt.Fprintln(out, "      "+cli.StyleFile.Render(base))
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, cli.StyleBold.Render("  Team Context")+cli.StyleDim.Render("  — your team's shared knowledge"))
	if len(teams) == 0 {
		fmt.Fprintln(out, "    "+cli.StyleDim.Render("(none found — run 'ox login' then 'ox init')"))
	}
	for _, t := range teams {
		name := cli.StyleBold.Render(t.Name)
		if t.Primary {
			name += " " + cli.StyleSuccess.Render("★ this repo")
		}
		fmt.Fprintln(out, "    "+name)
		fmt.Fprintln(out, "      "+cli.StyleFile.Render(t.Path))
	}

	// how to access it
	fmt.Fprintln(out)
	fmt.Fprintln(out, ui.RenderCategory("How to access it — it's just git"))
	fmt.Fprintln(out)
	example := "<path-above>"
	if len(ledgers) > 0 {
		example = ledgers[0].Path
		if ledgers[0].Symlink != "" {
			example = ledgers[0].Symlink
		}
	}
	fmt.Fprintln(out, "    "+cli.StyleCommand.Render(fmt.Sprintf("cd %s && git log", example))+
		cli.StyleDim.Render("   # browse the full history"))
	fmt.Fprintln(out, "    "+cli.StyleCommand.Render("git pull")+
		cli.StyleDim.Render("   # get the latest"))
	fmt.Fprintln(out, "    "+cli.StyleCommand.Render("git remote add backup <your-remote> && git push backup")+
		cli.StyleDim.Render("   # back it up"))

	// related commands
	fmt.Fprintln(out)
	fmt.Fprintln(out, ui.RenderCategory("Related commands"))
	fmt.Fprintln(out)
	fmt.Fprintf(out, "    %s        %s\n", cli.StyleCommand.Render("ox teams"), cli.StyleDim.Render("list teams and their context paths"))
	fmt.Fprintf(out, "    %s       %s\n", cli.StyleCommand.Render("ox status"), cli.StyleDim.Render("sync state and locations"))
	fmt.Fprintf(out, "    %s %s\n", cli.StyleCommand.Render("ox session list"), cli.StyleDim.Render("browse recorded sessions"))

	if !synced {
		fmt.Fprintln(out)
		fmt.Fprintf(out, "  Tip: run %s to check out and refresh everything before you copy it.\n",
			cli.StyleCommand.Render("ox export --sync"))
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, cli.StyleDim.Render("  Today --sync covers all team contexts + this repo's ledger."))
	fmt.Fprintln(out, cli.StyleDim.Render("  Syncing every ledger across all your repos is coming."))
}
