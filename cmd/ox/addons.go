package main

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/sageox/ox/internal/addons"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/spf13/cobra"
)

// addons.go — `ox addons`, the Add-on Catalog's command surface.
//
// The boundary this command defends is ADR-032 D1, and it is the whole reason
// there is no `ox addons sync`:
//
//	ox addons chooses and updates what the team owns.
//	ox sync   distributes everything the team owns.
//
// Every verb here writes the Team Context checkout and stops. Delivery into a
// product repository is convergence's job, through the same pipeline
// hand-authored team content already travels — which is why installing an
// add-on needs no repository-side step and offers none.
//
// This surface used to be registered only behind FEATURE_ADDONS. The flag is
// gone (ADR-032, amended): gating the MECHANISM guarded nothing, because the
// only provider is compiled into this binary and ships bytes we wrote. The
// risk a gate exists to hold back is untrusted content, so the gate moved to
// the thing that will actually introduce it — a remote/third-party provider,
// which lands behind its own flag, default off.

var addonsCmd = &cobra.Command{
	Use:     "addons",
	Aliases: []string{"addon"},
	Short:   "Install skills, rules, and the context each skill carries",
	Long: `Browse, install, update and remove Add-ons for your team.

An add-on is a versioned bundle of three things your team selects once:

  skills   what an AI coworker should DO, and when
  rules    conventions it should follow
  context  the reference material those skills carry with them

That third one is the point people miss, so be precise about what it means
here: NOT your Team Context, and not arbitrary documents. It is the material
a specific skill needs in order to be worth following — the briefs, tables,
examples and source material that ship inside that skill, in its
` + "`references/`" + ` and ` + "`assets/`" + `.

Bundling it into the skill is deliberate. A document nothing points at is
never opened; material carried by a skill is found exactly when that skill
is. So an add-on hands a coworker the instruction and the evidence together,
rather than an instruction and a hope that it goes looking.

It installs into your Team Context — never into a single repository — so every
repository on the team receives it, and every teammate's AI coworker sees the
same selection. ` + "`ox sync`" + ` distributes it; this command chooses it.

Add-on content is pinned to an exact version and digest until someone runs
` + "`ox addons update`" + `. An update replaces the files the add-on owns and drops
the ones its new version no longer ships: they are not yours to edit, and your
Team Context git history is the undo. ox never overwrites a file it does not
own — a name that collides with something hand-authored is refused, not merged.

Today every add-on comes from the catalog compiled into this ox binary, so
` + "`ox addons list`" + ` shows what this build ships and nothing else — not
team-published skills (those are ` + "`ox skills publish`" + `) and not any remote
source. Add-ons from outside this binary are future work and will arrive behind
their own opt-in.

Add-on files are written non-executable, always — a provider cannot choose
otherwise. Content that arrives with runnable scripts still needs
` + "`ox skills approve <name>`" + ` before an AI coworker may read it, and that
command's ` + "`--allow-scripts`" + ` before anything becomes runnable. Installing an
add-on grants neither.`,
	// Same reasoning as `ox skills`: an unknown verb is named rather than
	// swallowed into generic help that reads like the command ran.
	Args: cobra.ArbitraryArgs,
	RunE: runAddonsDispatch,
}

// runAddonsDispatch prints help for a bare `ox addons` and fails loudly on an
// unknown verb. Valid subcommands are routed by cobra before RunE is reached,
// so anything arriving here is a token cobra could not match.
func runAddonsDispatch(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return cmd.Help()
	}
	return fmt.Errorf("unknown subcommand %q for %q\nRun 'ox addons --help' to see available commands", args[0], cmd.CommandPath())
}

var addonsListCmd = &cobra.Command{
	Use:   "list",
	Args:  cobra.NoArgs,
	Short: "Show available add-ons and which ones this team has installed",
	RunE:  runAddonsList,
}

var addonsInstallCmd = &cobra.Command{
	Use:   "install <name>",
	Args:  cobra.ExactArgs(1),
	Short: "Install an add-on into your Team Context",
	RunE:  runAddonsInstall,
}

var addonsUpdateCmd = &cobra.Command{
	Use:   "update <name>",
	Args:  cobra.ExactArgs(1),
	Short: "Update an installed add-on to the catalog's current version",
	Long: `Update an installed add-on.

Every file the add-on owns is replaced, and owned files its new version no
longer ships are removed. There is no merge and no conflict state. If your team
edited an owned file, ox names it before overwriting so the change is visible in
` + "`git log -p`" + ` rather than lost silently.`,
	RunE: runAddonsUpdate,
}

var addonsRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Args:  cobra.ExactArgs(1),
	Short: "Remove an installed add-on from your Team Context",
	RunE:  runAddonsRemove,
}

func init() {
	for _, c := range []*cobra.Command{addonsListCmd, addonsInstallCmd, addonsUpdateCmd, addonsRemoveCmd} {
		c.Flags().Bool("json", false, "Emit machine-readable JSON")
	}
	addonsInstallCmd.Flags().String("version", "", "Install an exact version instead of the catalog's current one")
	addonsCmd.AddCommand(addonsListCmd, addonsInstallCmd, addonsUpdateCmd, addonsRemoveCmd)
	rootCmd.AddCommand(addonsCmd)
}

// teamContextForAddons resolves the Team Context every add-on verb writes.
//
// ADR-032 D1: a product repository carries no add-on selection and can never
// make one, so "which Team Context" is the only location question this command
// asks. Failing here with the reason is the point — an add-on verb that
// silently picked some other path would write the wrong team's selection.
func teamContextForAddons() (string, error) {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return "", fmt.Errorf("not inside a git repository, so ox cannot tell which team's add-ons you mean")
	}
	tc := config.FindRepoTeamContext(gitRoot)
	if tc == nil || tc.Path == "" {
		return "", fmt.Errorf("no Team Context is configured for this project, so there is nowhere to install add-ons — run `ox status` to see why")
	}
	return tc.Path, nil
}

// addonRow is one line of `ox addons list`, in both renderings.
type addonRow struct {
	Name      string `json:"name"`
	Summary   string `json:"summary"`
	Available string `json:"available_version"`
	Installed string `json:"installed_version,omitempty"`
	// UpdateAvailable is deliberately not omitempty: false is the ANSWER to
	// "should I run update", and an absent key cannot be told apart from an ox
	// too old to report it.
	UpdateAvailable bool `json:"update_available"`
	HasScripts      bool `json:"has_scripts"`
}

func runAddonsList(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")

	available, err := addons.NewEmbeddedProvider().List(cmd.Context())
	if err != nil {
		return fmt.Errorf("read the add-on catalog: %w", err)
	}

	// An unresolvable Team Context is not fatal for `list`: someone browsing
	// the catalog before configuring a team should still see what exists. The
	// installed column simply stays empty, and the JSON says so by omission
	// rather than by claiming nothing is installed.
	var lock addons.Lock
	if teamPath, tcErr := teamContextForAddons(); tcErr == nil {
		if lock, err = addons.LoadLock(teamPath); err != nil {
			return fmt.Errorf("read your team's add-on selection: %w", err)
		}
	}

	rows := make([]addonRow, 0, len(available))
	for _, d := range available {
		row := addonRow{
			Name: d.Name, Summary: d.Summary,
			Available: d.Version, HasScripts: d.HasScripts,
		}
		if installed, ok := lock.Find(d.Name); ok {
			row.Installed = installed.Version
			row.UpdateAvailable = installed.Digest != d.Digest
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })

	if asJSON {
		return encodeSkillsJSON(cmd.OutOrStdout(), map[string]any{"addons": rows})
	}
	return renderAddonRows(cmd.OutOrStdout(), rows)
}

// addonSummaryWidth bounds the single-line summary shown under each row.
// A Descriptor's Summary is written as an agent's auto-selection budget — it
// can run past 300 characters — and dumping it whole reads as a wall of text
// whose end a human cannot distinguish from a truncation. One line, clipped
// with intent, with the full text one `--json` away, is the fix.
const addonSummaryWidth = skillsTableWidth - 2 // 2-space indent, no trailing spare needed on the last cell

// addonSummaryLines is how much room a summary gets before it is ellipsized.
// One line cut every add-on mid-sentence; three fits the descriptions ox ships
// without turning the list into a wall.
const addonSummaryLines = 3

// addonStateText is the STATE column's text — always present, so meaning
// never depends on color alone (NO_COLOR, ansi256 fallback, a colorblind
// reader all get the same answer from the word itself).
func addonStateText(r addonRow) string {
	switch {
	case r.Installed != "" && r.UpdateAvailable:
		return "⚠ update available"
	case r.Installed != "":
		return "✓ installed"
	default:
		return "available"
	}
}

// addonStateStyled applies the semantic color layered on top of addonStateText.
func addonStateStyled(r addonRow) string {
	text := addonStateText(r)
	switch {
	case r.Installed != "" && r.UpdateAvailable:
		return cli.StyleWarning.Render(text)
	case r.Installed != "":
		return cli.StyleSuccess.Render(text)
	default:
		return cli.StyleDim.Render(text)
	}
}

// addonVersionCell shows the version story in one field: the catalog version
// when nothing is installed yet, the installed version when it is current,
// or the installed→available transition when an update is sitting there —
// so the STATE column's "update available" always has a concrete answer to
// "update to what" right next to it.
func addonVersionCell(r addonRow) string {
	switch {
	case r.Installed != "" && r.UpdateAvailable:
		return r.Installed + " → " + r.Available
	case r.Installed != "":
		return r.Installed
	default:
		return r.Available
	}
}

// addonColumnWidths sizes NAME and VERSION from their header text and their
// widest cell. STATE is never padded — it is always the last thing on its
// line, so nothing downstream depends on its width, and padding a styled
// string with %-*s would count invisible ANSI bytes as columns and silently
// misalign every row after it.
func addonColumnWidths(rows []addonRow) (name, version int) {
	name, version = len("NAME"), len("VERSION")
	for _, r := range rows {
		if n := len([]rune(r.Name)); n > name {
			name = n
		}
		if n := len([]rune(addonVersionCell(r))); n > version {
			version = n
		}
	}
	return name, version
}

func renderAddonRows(out io.Writer, rows []addonRow) error {
	if len(rows) == 0 {
		fmt.Fprintln(out, "No add-ons are available in this build's catalog.")
		return nil
	}

	installed := 0
	for _, r := range rows {
		if r.Installed != "" {
			installed++
		}
	}

	p := skillsPrintf(out)
	p("%s", cli.StyleGroupHeader.Render("Add-ons"))
	p("%s", cli.StyleDim.Render(strings.Repeat("─", len("Add-ons"))))
	p("%s", cli.StyleDim.Render(fmt.Sprintf("%d in this build's catalog · %d installed", len(rows), installed)))
	p("")

	nameWidth, versionWidth := addonColumnWidths(rows)
	p("%s", cli.StyleAccent.Render(fmt.Sprintf("%-*s  %-*s  %s", nameWidth, "NAME", versionWidth, "VERSION", "STATE")))

	for _, r := range rows {
		p("%-*s  %-*s  %s", nameWidth, r.Name, versionWidth, addonVersionCell(r), addonStateStyled(r))
		for _, line := range wrapCapped(r.Summary, addonSummaryWidth, addonSummaryLines) {
			p("  %s", cli.StyleDim.Render(line))
		}
		if r.HasScripts {
			p("  %s", cli.StyleWarning.Render("⚠ runnable scripts — approve before an AI coworker reads them:"))
			p("    %s", cli.StyleDim.Render(fmt.Sprintf("ox skills approve %s --allow-scripts", r.Name)))
		}
		p("")
	}

	p("%s", cli.StyleDim.Render("Install one with `ox addons install <name>` · machine-readable: `--json`"))
	return nil
}

func runAddonsInstall(cmd *cobra.Command, args []string) error {
	version, _ := cmd.Flags().GetString("version")
	return runAddonOp(cmd, args[0], func(teamPath string) (addons.Result, error) {
		return addons.Install(cmd.Context(), teamPath, addons.NewEmbeddedProvider(), args[0], version)
	})
}

func runAddonsUpdate(cmd *cobra.Command, args []string) error {
	return runAddonOp(cmd, args[0], func(teamPath string) (addons.Result, error) {
		return addons.Update(cmd.Context(), teamPath, addons.NewEmbeddedProvider(), args[0])
	})
}

func runAddonsRemove(cmd *cobra.Command, args []string) error {
	return runAddonOp(cmd, args[0], func(teamPath string) (addons.Result, error) {
		return addons.Remove(cmd.Context(), teamPath, args[0])
	})
}

// runAddonOp is the one place the three mutating verbs share: resolve the Team
// Context, run the transaction, then render. Keeping the error translation here
// rather than in each verb is what stops the three from drifting into three
// different vocabularies for the same failure.
func runAddonOp(cmd *cobra.Command, name string, op func(teamPath string) (addons.Result, error)) error {
	asJSON, _ := cmd.Flags().GetBool("json")

	teamPath, err := teamContextForAddons()
	if err != nil {
		return err
	}

	result, opErr := op(teamPath)
	if opErr != nil {
		// A collision is a refusal, not a crash: it means nothing was written
		// and the human has a choice to make. Say which paths and why, because
		// "collides" without the list leaves them guessing.
		var collision *addons.CollisionError
		if errors.As(opErr, &collision) {
			return fmt.Errorf("%w\n\nox never overwrites content it does not own. Rename or delete the conflicting path(s) in your Team Context, then try again", opErr)
		}
		if errors.Is(opErr, addons.ErrAlreadyInstalled) {
			return fmt.Errorf("%q is already installed — run `ox addons update %s` to move it to the catalog's current version", name, name)
		}
		if errors.Is(opErr, addons.ErrNotInstalled) {
			return fmt.Errorf("%q is not installed in your Team Context — run `ox addons list` to see what is", name)
		}
		return opErr
	}

	if asJSON {
		return encodeSkillsJSON(cmd.OutOrStdout(), result)
	}
	return renderAddonResult(cmd.OutOrStdout(), teamPath, result)
}

func renderAddonResult(out io.Writer, teamPath string, r addons.Result) error {
	ok := cli.StyleSuccess.Render("✓")
	switch r.Op {
	case addons.OpRemove:
		fmt.Fprintf(out, "%s Removed %s from your Team Context (%d file(s)).\n", ok, r.Addon, len(r.Removed))
	case addons.OpUpdate:
		fmt.Fprintf(out, "%s Updated %s to %s (%d written, %d removed).\n", ok, r.Addon, r.Version, len(r.Written), len(r.Removed))
	default:
		fmt.Fprintf(out, "%s Installed %s %s (%d file(s)).\n", ok, r.Addon, r.Version, len(r.Written))
	}

	// Named BEFORE the success guidance, not after: this is the one thing in
	// the output a human may need to act on, and ADR-032 D4 makes ox
	// responsible for saying it out loud rather than overwriting in silence.
	if len(r.Modified) > 0 {
		cli.PrintWarning(fmt.Sprintf("your team had edited %d file(s) this add-on owns; the new version replaced them: %s",
			len(r.Modified), strings.Join(r.Modified, ", ")))
		// Plain, not backticked, and on its own line: teamPath is a real
		// absolute path with no shorter substitute, so this can legitimately
		// run past 80 columns on a deep checkout — but it must never look like
		// a fixed-width command that got cut off mid-token.
		fmt.Fprintf(out, "  Recover with: git -C %s log -p -- <path>\n", teamPath)
	}

	if r.Op != addons.OpRemove {
		// Two short lines, not one long one: the original single sentence ran
		// past 80 columns with two backticked commands in it, so a plain
		// terminal wrapped it wherever it landed — including mid-command,
		// which reads exactly like truncated output.
		fmt.Fprintln(out, "Distribution is automatic — run `ox sync` if you need it now.")
		fmt.Fprintln(out, "Verify with `ox skills status`.")
	}
	return nil
}
