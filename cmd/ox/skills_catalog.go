package main

import (
	"fmt"
	"io"
	"io/fs"
	"path"

	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/cli"
	"github.com/spf13/cobra"
)

// skills_catalog.go — `ox skills catalog`.
//
// What ox SHIPS, bundle by bundle, with each skill marked installed or not.
//
// This only became a meaningful surface when the first non-default bundle
// existed. While every bundle defaulted on, nothing was ever "available but not
// installed": the catalog was the inventory, `ox skills install` had nothing to
// install, and a listing would have been a static rendering of a compiled-in
// constant.
//
// Separate command from `ox skills list` on purpose. `list` answers "what do my
// agents have" and reads this repository's disk, including skills ox does not
// own. `catalog` answers "what could I add" and reads the binary. Folding the
// second into a flag on the first makes one command whose row set changes
// meaning with a flag, which is how a reader stops trusting either answer.

// skillAvailable is the other half of skills_status.go's skillInstalled: ox
// ships it, this repository has not selected it.
const skillAvailable = "available"

var skillsCatalogCmd = &cobra.Command{
	Use:   "catalog",
	Short: "Show the skills ox ships and whether each is installed here",
	Long: `Show the skills ox ships, grouped by bundle, and whether each is installed here.

Bundles marked as defaults are installed into every project automatically. The
rest are opt-in: they appear here as available until you ask for them.

This lists only what the ox binary carries. To see everything your AI coworkers
can actually reach in this repository — including your team's skills and your
own — run ` + "`ox skills list`" + `.`,
	RunE: runSkillsCatalog,
}

func init() {
	skillsCatalogCmd.Flags().Bool("json", false, "Emit machine-readable JSON")
	skillsCmd.AddCommand(skillsCatalogCmd)
}

type catalogSkillRow struct {
	Name string `json:"name"`
	// Bundle is repeated on every row even though the rows are grouped, because
	// a caller that flattens the JSON otherwise loses the only thing that says
	// how the skill would be selected.
	Bundle      string `json:"bundle"`
	Status      string `json:"status"`
	Description string `json:"description"`
}

type catalogBundleGroup struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	// Default is not omitempty: "will a new project get this without asking?" is
	// the question this whole command exists to answer, and false is the answer
	// for every opt-in bundle.
	Default bool              `json:"default"`
	Skills  []catalogSkillRow `json:"skills"`
}

type skillsCatalogOutput struct {
	Bundles  []catalogBundleGroup `json:"bundles"`
	Guidance string               `json:"guidance"`
}

func runSkillsCatalog(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")

	gitRoot := findGitRoot()
	if gitRoot == "" {
		return fmt.Errorf("not inside a git repository")
	}

	roots, err := resolveSkillRoots(gitRoot)
	if err != nil {
		return err
	}
	out, err := collectSkillsCatalog(gitRoot, roots)
	if err != nil {
		return err
	}
	return emitSkillsCatalog(cmd.OutOrStdout(), out, asJSON)
}

// collectSkillsCatalog pairs the compiled-in catalog with what is on this
// repository's disk.
//
// Installed means "present in at least one selected skill root", which is the
// honest answer to this command's question — could I add this, or is it already
// here. Whether it is complete in EVERY root is a different question, and
// `ox skills status` is the surface that already answers it; reporting a
// half-installed skill as available here would tell a reader to install
// something they already have.
func collectSkillsCatalog(repoRoot string, roots []string) (skillsCatalogOutput, error) {
	listing := collectInstalledSkills(repoRoot, roots)
	if len(listing.Problems) > 0 {
		// A root collectInstalledSkills could not read is invisible to `installed`
		// below, so a skill sitting in it would come back "available" here even
		// though it is already on disk — worse than the half-installed case this
		// function already declines to judge, because the advice is not just
		// incomplete, it is wrong. A root that simply never got materialized is NOT
		// a problem (collectInstalledSkills skips that silently), so this only
		// fires on a genuine read failure. `ox skills list` already owns reporting
		// that failure alongside whatever it could still read; this command has no
		// partial mode, so it fails outright instead of guessing.
		return skillsCatalogOutput{}, fmt.Errorf("%s — fix its permissions, then re-run `ox skills catalog`", listing.Problems[0])
	}

	installed := map[string]bool{}
	for _, row := range listing.Skills {
		installed[row.Name] = true
	}

	out := skillsCatalogOutput{Bundles: []catalogBundleGroup{}}
	for _, bundle := range skills.Catalog {
		group := catalogBundleGroup{
			ID: bundle.ID, Description: bundle.Description,
			Default: bundle.Default, Skills: []catalogSkillRow{},
		}
		for _, name := range bundle.SkillIDs {
			// A retired name must never be offered. Retirement removes the files but
			// keeps the name listed for two releases so the reconciler can still
			// recognize and delete an old copy, and a catalog that advertised those
			// would be selling something the installer is actively removing.
			if skills.IsRetired(name) {
				continue
			}
			description, err := catalogSkillDescription(name)
			if err != nil {
				return skillsCatalogOutput{}, err
			}
			status := skillAvailable
			if installed[name] {
				status = skillInstalled
			}
			group.Skills = append(group.Skills, catalogSkillRow{
				Name: name, Bundle: bundle.ID, Status: status, Description: description,
			})
		}
		out.Bundles = append(out.Bundles, group)
	}
	out.Guidance = skillsCatalogGuidance(out)
	return out, nil
}

// catalogSkillDescription reads the description straight out of the embedded
// source tree rather than through skills.Selected, which revalidates and reads
// the whole catalog to answer one question about one file.
func catalogSkillDescription(name string) (string, error) {
	content, err := fs.ReadFile(skills.FS, path.Join(name, skills.SkillFileName))
	if err != nil {
		return "", fmt.Errorf("read the catalog entry for %q: %w", name, err)
	}
	description, _ := manifestDescription(content)
	return description, nil
}

func skillsCatalogGuidance(out skillsCatalogOutput) string {
	var total, available int
	firstAvailable := ""
	for _, bundle := range out.Bundles {
		for _, row := range bundle.Skills {
			total++
			if row.Status != skillAvailable {
				continue
			}
			available++
			if firstAvailable == "" {
				firstAvailable = row.Name
			}
		}
	}
	if available == 0 {
		return fmt.Sprintf("All %d skills ox ships are installed here. Run `ox skills list` to see everything your AI coworkers have, including your own.", total)
	}
	verb := "are"
	if available == 1 {
		verb = "is"
	}
	return fmt.Sprintf("%d of the %d skills ox ships %s not installed here. Run `ox skills install %s` to add one.",
		available, total, verb, firstAvailable)
}

func emitSkillsCatalog(w io.Writer, out skillsCatalogOutput, asJSON bool) error {
	if asJSON {
		return encodeSkillsJSON(w, out)
	}
	p := func(format string, args ...any) { fmt.Fprintf(w, format+"\n", args...) }

	for i, bundle := range out.Bundles {
		if i > 0 {
			p("")
		}
		// The bundle is a section header rather than a fifth column: at 80 columns
		// a repeated bundle id costs every row the width the description needs, to
		// restate something the grouping already says.
		// Short markers on purpose. "(installed by default)" pushed the lifecycle
		// bundle's header to 85 columns, and the clip fell on the marker — the one
		// part of the line a reader is scanning for.
		suffix := "opt-in"
		if bundle.Default {
			suffix = "on by default"
		}
		// Clipped against the id's own width: `lifecycle` plus its description plus
		// the suffix runs to 85 columns, and a wrapped header reads as a stray row.
		gutter := len([]rune(bundle.ID)) + 2
		p("%s  %s", cli.StyleAccent.Render(bundle.ID),
			truncateCell(fmt.Sprintf("%s (%s)", bundle.Description, suffix), skillsTableWidth-gutter))
		for _, row := range bundle.Skills {
			status := row.Status
			if row.Status == skillInstalled {
				status = cli.StyleSuccess.Render(row.Status)
			}
			p("  %-*s  %s  %s",
				nameColumn, truncateCell(row.Name, nameColumn),
				padStatus(status, row.Status),
				truncateCell(row.Description, catalogDescriptionColumn))
		}
	}

	if out.Guidance != "" {
		p("")
		writeWrapped(w, "", "", out.Guidance)
	}
	return nil
}

// Catalog rows are indented two columns under their bundle header, so the
// description gets two fewer than the flat table in `ox skills list`.
const (
	statusColumn             = 9 // len("available")
	catalogDescriptionColumn = listDescriptionColumn - 2
)

// padStatus right-pads a possibly-styled status to the column width. Padding the
// rendered string directly would count ANSI bytes as characters and knock every
// following column out of alignment, so the width comes from the plain value.
func padStatus(rendered, plain string) string {
	if pad := statusColumn - len([]rune(plain)); pad > 0 {
		return rendered + fmt.Sprintf("%*s", pad, "")
	}
	return rendered
}
