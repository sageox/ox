package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/internal/version"
	"github.com/spf13/cobra"
)

// skills_catalog.go — `ox skills catalog`.
//
// What ox SHIPS, bundle by bundle, with selection and projection reported
// separately from a same-named directory ox does not own.
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

const (
	// skillAvailable means the catalog offers it and the repository has neither
	// selected it nor placed same-named content in the way.
	skillAvailable        = "available"
	skillCatalogSelected  = "selected"
	skillCatalogProjected = "projected"
	skillCatalogConflict  = "conflicting"
	skillCatalogNameMatch = "name-match"
)

var skillsCatalogCmd = &cobra.Command{
	Use:   "catalog",
	Short: "Show the skills ox ships and their state in this repository",
	Long: `Show the skills ox ships, grouped by bundle, and their state here.

Bundles marked as defaults are installed into every project automatically. The
rest are opt-in: they appear here as available until you ask for them.

Projected means ox owns a complete copy in every selected skills directory.
Selected means the repository chose it but projection has not completed.
Conflicting means same-named content blocks that selection. Name-match means
same-named content exists without a selection; ox does not claim it as its own.

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
	Selected    bool   `json:"selected"`
	Projected   bool   `json:"projected"`
	Conflicting bool   `json:"conflicting"`
	NameMatch   bool   `json:"name_match"`
	Detail      string `json:"detail,omitempty"`
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
// A directory name is not ownership. Selection comes from the committed intent;
// projection requires an ox-owned manifest in every selected root; any other
// same-name content is reported without being claimed.
func collectSkillsCatalog(repoRoot string, roots []string) (skillsCatalogOutput, error) {
	listing := collectInstalledSkills(repoRoot, roots)
	if len(listing.Problems) > 0 {
		// A root collectInstalledSkills could not read makes ownership unknowable. A
		// same-named skill sitting there could otherwise come back "available" even
		// though an install would collide — advice that is not just incomplete but
		// wrong. A root that simply never got materialized is NOT
		// a problem (collectInstalledSkills skips that silently), so this only
		// fires on a genuine read failure. `ox skills list` already owns reporting
		// that failure alongside whatever it could still read; this command has no
		// partial mode, so it fails outright instead of guessing.
		return skillsCatalogOutput{}, fmt.Errorf("%s — fix its permissions, then re-run `ox skills catalog`", listing.Problems[0])
	}

	desired, targets, err := skillmanager.LoadDesired(repoRoot)
	if err != nil {
		return skillsCatalogOutput{}, err
	}
	selected, err := selectedCatalogNames(desired)
	if err != nil {
		return skillsCatalogOutput{}, err
	}
	plan, err := skillmanager.Plan(repoRoot, version.Version, desired, targets)
	if err != nil {
		return skillsCatalogOutput{}, err
	}
	roots = dedupeStrings(roots)
	planStates := catalogPlanStates(plan, roots)

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
			present, err := catalogNamePresence(repoRoot, roots, name)
			if err != nil {
				return skillsCatalogOutput{}, err
			}
			row := classifyCatalogSkill(name, bundle.ID, description, roots, present, planStates[name], selected[name])
			group.Skills = append(group.Skills, row)
		}
		out.Bundles = append(out.Bundles, group)
	}
	out.Guidance = skillsCatalogGuidance(out)
	return out, nil
}

type catalogPlanState struct {
	pending     bool
	conflicting bool
}

func catalogPlanStates(plan *skillmanager.ReconcilePlan, roots []string) map[string]catalogPlanState {
	out := map[string]catalogPlanState{}
	if plan == nil {
		return out
	}
	actions := append(append(append([]skillmanager.FileAction{}, plan.Creates...), plan.Updates...), plan.Removes...)
	for _, action := range actions {
		if name := catalogSkillNameFromPath(action.Path, roots); name != "" {
			state := out[name]
			state.pending = true
			out[name] = state
		}
	}
	for _, conflict := range plan.Conflicts {
		if name := catalogSkillNameFromPath(conflict.Path, roots); name != "" {
			state := out[name]
			state.conflicting = true
			out[name] = state
		}
	}
	return out
}

func catalogSkillNameFromPath(filePath string, roots []string) string {
	filePath = filepath.ToSlash(filePath)
	for _, root := range roots {
		prefix := strings.TrimSuffix(filepath.ToSlash(root), "/") + "/"
		rest, ok := strings.CutPrefix(filePath, prefix)
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(rest, "/")
		return name
	}
	return ""
}

func classifyCatalogSkill(name, bundle, description string, roots, present []string, planned catalogPlanState, selected bool) catalogSkillRow {
	row := catalogSkillRow{
		Name: name, Bundle: bundle, Description: description, Status: skillAvailable, Selected: selected,
	}
	if selected {
		if planned.conflicting {
			row.Status = skillCatalogConflict
			row.Conflicting = true
			row.Detail = "selected, but same-named content ox does not own blocks projection"
			return row
		}
		if len(roots) > 0 && !planned.pending {
			row.Status = skillCatalogProjected
			row.Projected = true
			row.Detail = "selected and projected by ox into every selected skills directory"
			return row
		}
		row.Status = skillCatalogSelected
		row.Detail = "selected, but not yet projected into every selected skills directory"
		return row
	}
	if len(present) > 0 {
		row.Status = skillCatalogNameMatch
		row.NameMatch = true
		row.Detail = "same-named content exists, but this repository did not select it and ox does not own it as this catalog entry"
	}
	return row
}

// catalogNamePresence includes incomplete directories and non-directory files.
// Inventory intentionally ignores those because they are not skills; catalog
// must see them because they still block projection at that exact path.
func catalogNamePresence(repoRoot string, roots []string, name string) ([]string, error) {
	repo, err := os.OpenRoot(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("open repository: %w", err)
	}
	defer func() { _ = repo.Close() }()

	var present []string
	for _, root := range roots {
		rel := filepath.Join(filepath.FromSlash(root), name)
		if _, err := repo.Lstat(rel); err == nil {
			present = append(present, root)
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("inspect catalog path %s: %w", filepath.ToSlash(rel), err)
		}
	}
	return present, nil
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
	var total, available, selected, conflicts, matches int
	firstAvailable := ""
	for _, bundle := range out.Bundles {
		for _, row := range bundle.Skills {
			total++
			switch row.Status {
			case skillAvailable:
				available++
				if firstAvailable == "" {
					firstAvailable = row.Name
				}
			case skillCatalogSelected:
				selected++
			case skillCatalogConflict:
				conflicts++
			case skillCatalogNameMatch:
				matches++
			}
		}
	}
	if conflicts > 0 {
		return fmt.Sprintf("%d selected catalog %s blocked by same-named content ox does not own. Run `ox doctor` for the conflicting paths; ox will not overwrite them.",
			conflicts, pluralNoun(conflicts, "skill is", "skills are"))
	}
	if selected > 0 {
		return fmt.Sprintf("%d catalog %s selected but not fully projected. Run `ox doctor` to repair the missing copies.",
			selected, pluralNoun(selected, "skill is", "skills are"))
	}
	if matches > 0 {
		return fmt.Sprintf("%d catalog %s only match content this repository did not select. ox has not claimed those copies; rename or remove them before installing the catalog entries.",
			matches, pluralNoun(matches, "name", "names"))
	}
	if available == 0 {
		return fmt.Sprintf("All %d skills ox ships are projected here. Run `ox skills list` to see everything your AI coworkers have, including your own.", total)
	}
	verb := "are"
	if available == 1 {
		verb = "is"
	}
	return fmt.Sprintf("%d of the %d skills ox ships %s available for selection. Run `ox skills install %s` to add one.",
		available, total, verb, firstAvailable)
}

func pluralNoun(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
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
		// Short markers on purpose. "(installed by default)" once pushed the lifecycle
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
			switch row.Status {
			case skillCatalogProjected:
				status = cli.StyleSuccess.Render(row.Status)
			case skillCatalogConflict:
				status = cli.StyleError.Render(row.Status)
			case skillCatalogSelected, skillCatalogNameMatch:
				status = cli.StyleWarning.Render(row.Status)
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
	statusColumn             = 11 // len("conflicting")
	catalogDescriptionColumn = skillsTableWidth - nameColumn - statusColumn - 7
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
