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
// The whole surface is registered only when the add-ons gate is on
// (syncFeatureGatedCommands in root.go). Cobra resolves commands and renders
// help before PersistentPreRunE, so a RunE-only guard would still advertise
// these verbs and a Hidden-only guard would still let them run.

var addonsCmd = &cobra.Command{
	Use:     "addons",
	Aliases: []string{"addon"},
	Short:   "Choose and update the add-ons your team owns",
	Long: `Browse, install, update and remove Add-ons for your team.

An add-on is a versioned bundle of skills and rules your team selects once. It
installs into your Team Context — never into a single repository — so every
repository on the team receives it, and every teammate's AI coworker sees the
same selection. ` + "`ox sync`" + ` distributes it; this command chooses it.

Add-on content is pinned to an exact version and digest until someone runs
` + "`ox addons update`" + `. An update replaces the files the add-on owns and drops
the ones its new version no longer ships: they are not yours to edit, and your
Team Context git history is the undo. ox never overwrites a file it does not
own — a name that collides with something hand-authored is refused, not merged.

Add-ons may be published by SageOx, by your own team, or in future by a third
party. Add-on files are written non-executable, always — a provider cannot
choose otherwise. Content that arrives with runnable scripts still needs
` + "`ox skills approve <name>`" + ` before an AI coworker may read it, and that
command's ` + "`--allow-scripts`" + ` before anything becomes runnable. Installing an
add-on grants neither.`,
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
	// NOT rootCmd.AddCommand: addonsCmd is registered by
	// syncFeatureGatedCommands once the add-ons gate has resolved.
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

func renderAddonRows(out io.Writer, rows []addonRow) error {
	if len(rows) == 0 {
		fmt.Fprintln(out, "No add-ons are available in this build's catalog.")
		return nil
	}
	for _, r := range rows {
		state := "available"
		switch {
		case r.Installed != "" && r.UpdateAvailable:
			state = "installed " + r.Installed + " · update available"
		case r.Installed != "":
			state = "installed " + r.Installed
		}
		fmt.Fprintf(out, "%-18s %-12s %s\n", r.Name, r.Available, state)
		if strings.TrimSpace(r.Summary) != "" {
			fmt.Fprintf(out, "  %s\n", r.Summary)
		}
		if r.HasScripts {
			fmt.Fprintf(out, "  carries runnable scripts — `ox skills approve %s --allow-scripts` gates them\n", r.Name)
		}
	}
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
	switch r.Op {
	case addons.OpRemove:
		fmt.Fprintf(out, "Removed %s from your Team Context (%d file(s)).\n", r.Addon, len(r.Removed))
	case addons.OpUpdate:
		fmt.Fprintf(out, "Updated %s to %s (%d written, %d removed).\n", r.Addon, r.Version, len(r.Written), len(r.Removed))
	default:
		fmt.Fprintf(out, "Installed %s %s (%d file(s)).\n", r.Addon, r.Version, len(r.Written))
	}

	// Named BEFORE the success guidance, not after: this is the one thing in
	// the output a human may need to act on, and ADR-032 D4 makes ox
	// responsible for saying it out loud rather than overwriting in silence.
	if len(r.Modified) > 0 {
		cli.PrintWarning(fmt.Sprintf("your team had edited %d file(s) this add-on owns; the new version replaced them: %s",
			len(r.Modified), strings.Join(r.Modified, ", ")))
		fmt.Fprintf(out, "  Recover any of them with `git -C %s log -p -- <path>`.\n", teamPath)
	}

	if r.Op != addons.OpRemove {
		fmt.Fprintln(out, "Distribution is automatic — run `ox sync` only if you need it immediately, then `ox skills status` to verify.")
	}
	return nil
}
