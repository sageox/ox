package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/adapterstamp"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/spf13/cobra"
)

// skills_list.go — `ox skills list`.
//
// One inventory of everything an AI coworker in this repository can actually
// reach, whoever put it there. The three provenances are the whole point: ox's
// own skills, the team's, and — the majority in any real repository — the ones a
// human hand-authored and ox does not own.
//
// Listing only ox's rows was the tempting shape, because those are the ones with
// a lockfile behind them. It is also the shape that teaches people the command
// lies: a repo with twenty hand-written skills would report three, and the next
// question ("where did my skill go?") has no answer anywhere in the tool. So
// this walks the skill roots on disk and classifies what it finds, rather than
// rendering ox's record of what it installed.

const (
	provenanceOx    = "ox"
	provenanceTeam  = "team"
	provenanceLocal = "local"
)

// provenanceRank groups the table: ox's own rows, then the team's, then the
// human's. Alphabetical across all three interleaves them, which buries the
// question a reader actually has ("which of these are mine?").
var provenanceRank = map[string]int{provenanceOx: 0, provenanceTeam: 1, provenanceLocal: 2}

var skillsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List every skill installed in this repository",
	Long: `List every skill installed in this repository, whoever installed it.

Three provenances appear in one table: ox for the skills the CLI ships, team for
the ones your Team Context publishes, and local for the ones a human authored
here. ox owns only the first two and will never modify or remove a local skill.

If a skill your team publishes is missing here, ` + "`ox skills status`" + ` says why.`,
	RunE: runSkillsList,
}

func init() {
	skillsListCmd.Flags().Bool("json", false, "Emit machine-readable JSON")
	skillsCmd.AddCommand(skillsListCmd)
}

// installedSkillRow is one skill on disk, in both renderings.
type installedSkillRow struct {
	Name       string `json:"name"`
	Provenance string `json:"provenance"`
	// Description is never omitempty: "this skill has no description" is a real
	// answer — an agent cannot select a skill it cannot see the trigger for — and
	// an absent key cannot be told apart from an ox that does not report it.
	Description string `json:"description"`
	// Roots are every selected skill directory the skill was found in. A repo
	// wired to both Claude Code and Codex has two, and a skill present in only
	// one of them is a real, invisible half-install.
	Roots []string `json:"roots"`
}

type skillsListOutput struct {
	Roots    []string            `json:"roots"`
	Skills   []installedSkillRow `json:"skills"`
	Problems []string            `json:"problems"`
	Guidance string              `json:"guidance"`
}

func runSkillsList(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")

	gitRoot := findGitRoot()
	if gitRoot == "" {
		return fmt.Errorf("not inside a git repository")
	}

	roots, err := resolveSkillRoots(gitRoot)
	if err != nil {
		return err
	}
	return emitSkillsList(cmd.OutOrStdout(), collectInstalledSkills(gitRoot, roots), asJSON)
}

// resolveSkillRoots returns the skill directories this repository actually uses.
//
// The committed lockfile is the authority: it records what `ox init` selected and
// is what reconcile writes into. Adapter detection is only the fallback for a
// repository that has never run `ox init` — it shells out to whatever
// ox-adapter-* binaries happen to be on PATH, so making it the primary source
// would turn this command's output into a property of the developer's machine
// rather than of the repository.
func resolveSkillRoots(repoRoot string) ([]string, error) {
	roots, err := skillTargetRoots(repoRoot)
	if err != nil {
		return nil, err
	}
	if len(roots) == 0 {
		detected, detectErr := detectedSkillTargets(repoRoot)
		if detectErr != nil {
			return nil, detectErr
		}
		for _, target := range detected {
			if target.Format == adapterprotocol.SkillFormatAgentSkillsV1 {
				roots = append(roots, target.Root)
			}
		}
	}
	return dedupeNames(roots), nil
}

// collectInstalledSkills walks the skill roots and classifies what is there,
// WITHOUT printing. repoRoot is a parameter rather than something this goes
// looking for, so a test can point it at a scratch repo without moving the
// process.
func collectInstalledSkills(repoRoot string, roots []string) skillsListOutput {
	out := skillsListOutput{Roots: dedupeNames(roots), Skills: []installedSkillRow{}, Problems: []string{}}
	out.Skills, out.Problems = inventorySkillRoots(repoRoot, out.Roots)
	sort.Slice(out.Skills, func(i, j int) bool {
		if a, b := provenanceRank[out.Skills[i].Provenance], provenanceRank[out.Skills[j].Provenance]; a != b {
			return a < b
		}
		return out.Skills[i].Name < out.Skills[j].Name
	})
	out.Guidance = skillsListGuidance(out)
	return out
}

// inventorySkillRoots walks the selected roots and returns one row per skill
// found, plus a line for every root it could not read.
//
// Every read below goes through a handle pinned to repoRoot rather than through
// a joined path, and that is a security property rather than a style choice.
// CanonicalizeTargets vets the root STRING; nothing vets what the string
// RESOLVES to. A repository that ships `.claude/skills` as a symlink out of the
// checkout would have os.ReadDir follow it without complaint, and `ox skills
// list` would then publish some unrelated directory's SKILL.md descriptions to
// stdout and to --json — a repository-controlled read of files nobody asked
// about. os.Root resolves each component against the held descriptor and
// refuses any resolution that leaves the tree, so the escape is impossible
// rather than merely narrow: the swap has no window to land in.
//
// The per-component Lstat-and-identity-check dance skillmanager.openRepoDir
// performs is deliberately NOT repeated here. That code is about to WRITE files
// ox owns, so it refuses any symlinked component outright; this one only reads,
// and a link that resolves back inside the repository is still the
// repository's own content.
func inventorySkillRoots(repoRoot string, roots []string) ([]installedSkillRow, []string) {
	rows := []installedSkillRow{}
	problems := []string{}

	repo, err := os.OpenRoot(repoRoot)
	if err != nil {
		return rows, append(problems,
			sanitizeCell(fmt.Sprintf("ox could not open this repository to inventory its skills: %v", err)))
	}
	defer func() { _ = repo.Close() }()

	// Indexed by name, not by (root, name): the same skill in two roots is one
	// skill with two homes, and two rows would read as two skills.
	byName := map[string]*installedSkillRow{}
	for _, root := range roots {
		found, readErr := readSkillRoot(repo, root)
		if errors.Is(readErr, fs.ErrNotExist) {
			// A selected root that was never materialized is a normal state for a
			// fresh checkout, and `ox skills status` is the surface that explains it.
			continue
		}
		if readErr != nil {
			// Not silently empty: an unreadable root and an empty one look identical
			// in the table, and they need opposite fixes. A root that resolves
			// outside the repository arrives here too, and saying so out loud beats
			// both reading it and dropping it without a word.
			problems = append(problems, skillRootProblem(root, readErr))
			continue
		}
		for _, skill := range found {
			row, ok := byName[skill.name]
			if !ok {
				row = &installedSkillRow{
					Name:        skill.name,
					Provenance:  skillProvenance(skill.name, skill.oxOwned),
					Description: skill.description,
					Roots:       []string{},
				}
				byName[skill.name] = row
			} else if row.Provenance != skillProvenance(skill.name, skill.oxOwned) {
				// The same name has different ownership evidence in two roots. Calling
				// the combined row ox-owned would misattribute the unowned copy and
				// promise that ox can safely repair or remove it. Local is the
				// conservative answer until status reports the cross-root divergence.
				row.Provenance = provenanceLocal
			}
			row.Roots = append(row.Roots, root)
		}
	}
	for _, row := range byName {
		rows = append(rows, *row)
	}
	return rows, problems
}

// skillOnDisk is one directory under a skill root that carries a manifest ox
// will actually read.
type skillOnDisk struct {
	name        string
	description string
	oxOwned     bool
}

// readSkillRoot enumerates one selected root through a handle pinned inside
// repo. A root that does not exist comes back as fs.ErrNotExist so the caller
// can keep that case silent; everything else is a genuine problem to report.
func readSkillRoot(repo *os.Root, root string) ([]skillOnDisk, error) {
	dir, err := repo.OpenRoot(filepath.FromSlash(root))
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()

	entries, err := fs.ReadDir(dir.FS(), ".")
	if err != nil {
		return nil, err
	}
	found := make([]skillOnDisk, 0, len(entries))
	for _, entry := range entries {
		// IsDir is Lstat-shaped on a directory listing, so a symlinked "skill
		// directory" simply is not one — the same answer ox gives a symlinked
		// manifest below.
		if !entry.IsDir() {
			continue
		}
		description, oxOwned, isSkill := skillManifestDescription(dir, entry.Name())
		if !isSkill {
			continue
		}
		found = append(found, skillOnDisk{name: entry.Name(), description: description, oxOwned: oxOwned})
	}
	return found, nil
}

// skillRootProblem phrases an unreadable skill root for a human.
//
// The root name is authored by the committed lockfile and the error text
// carries it again, so both halves of this sentence are repository-controlled
// and both are written straight to a terminal — the same control-byte hazard
// sanitizeCell exists for on the description column. Sanitizing the finished
// sentence rather than only the root is what covers the path the os.Root error
// brings along with it.
func skillRootProblem(root string, err error) string {
	return sanitizeCell(fmt.Sprintf("ox could not read the skills directory %s: %v", root, err))
}

// skillProvenance answers "who owns this directory?"
//
// The reserved namespaces are the ownership contract (skillmanager.IsReservedName)
// and settle almost every row. Two things they do not settle:
//
//   - The committed on-ramp is deliberately UNPREFIXED so it can never match a
//     reserved glob, so it needs an exact-match arm or ox's own file lands under
//     `local`.
//   - An older repository-scoped catalog install may be unprefixed. Its verified
//     in-band ownership stamp, not its catalog name, proves that ox wrote it.
//
// A catalog-name lookup is deliberately absent. Catalog discovery and ownership
// are different facts: a hand-authored skill does not become ox's because a later
// release happens to offer a Pack with the same ordinary-language name.
func skillProvenance(name string, oxOwned bool) string {
	switch {
	case strings.HasPrefix(name, skillmanager.TeamPrefix):
		return provenanceTeam
	case name == skillmanager.CommittedOnRamp || name == skillmanager.CLIBase ||
		strings.HasPrefix(name, skillmanager.CLIPrefix):
		return provenanceOx
	case oxOwned:
		return provenanceOx
	default:
		return provenanceLocal
	}
}

func skillsListGuidance(out skillsListOutput) string {
	if len(out.Problems) > 0 {
		return out.Problems[0]
	}
	if len(out.Roots) == 0 {
		return "This repository has not selected an AI coworker, so no skills are installed — run `ox init`."
	}
	if len(out.Skills) == 0 {
		// The roots are lockfile-authored, so naming them here carries the same
		// control-byte hazard as a skill directory name, and this sentence is
		// written straight to the terminal.
		safe := make([]string, 0, len(out.Roots))
		for _, root := range out.Roots {
			safe = append(safe, sanitizeCell(root))
		}
		return "No skills are installed in " + strings.Join(safe, ", ") + " — run `ox skills status` to see why."
	}
	counts := map[string]int{}
	for _, row := range out.Skills {
		counts[row.Provenance]++
	}
	return fmt.Sprintf("%s installed: %d from ox, %d from your team, %d your own. Run `ox skills status` to see what your team publishes and whether it reached this repository.",
		pluralSkills(len(out.Skills)), counts[provenanceOx], counts[provenanceTeam], counts[provenanceLocal])
}

func emitSkillsList(w io.Writer, out skillsListOutput, asJSON bool) error {
	if asJSON {
		return encodeSkillsJSON(w, out)
	}
	p := skillsPrintf(w)

	if len(out.Skills) > 0 {
		p("%s", cli.StyleAccent.Render(fmt.Sprintf("%-*s  %-*s  %s",
			provenanceColumn, "PROVENANCE", nameColumn, "NAME", "DESCRIPTION")))
		for _, row := range out.Skills {
			// Sanitized HERE and not on the way in: Name is the real on-disk
			// directory name everywhere else — a map key in this file, and what
			// `validatePublishNames` matches a user's argument against — so a
			// scrubbed copy stored in the struct would quietly stop matching the
			// directory it names. It is also a name ox did not choose: a checked-out
			// repository can hold a skill directory whose name embeds CSI or OSC
			// bytes, which on a POSIX terminal can forge a row, rewrite the window
			// title, or push text into the clipboard. Sanitizing BEFORE truncating
			// matters twice: the clip cannot land mid-escape-sequence, and the column
			// budget is spent on characters the reader can actually see.
			//
			// --json needs none of this; encoding/json escapes control bytes itself.
			p("%-*s  %-*s  %s", provenanceColumn, row.Provenance,
				nameColumn, truncateCell(sanitizeCell(row.Name), nameColumn),
				truncateCell(row.Description, listDescriptionColumn))
		}
	}

	if len(out.Problems) > 0 {
		if len(out.Skills) > 0 {
			p("")
		}
		writeSkillsProblems(w, cli.StyleError.Render("Directories ox could not read"), out.Problems)
	}

	if out.Guidance != "" {
		if len(out.Skills) > 0 || len(out.Problems) > 0 {
			p("")
		}
		writeWrapped(w, "", "", out.Guidance)
	}
	return nil
}

// Column widths for the tables in this command family.
//
// They are derived from skillsTableWidth (skills_out.go) rather than typed as
// literals so a change to one cannot silently push a row past the edge — a
// table that wraps is not a table, and the wrap only shows up on someone
// else's terminal.
const (
	provenanceColumn = 10 // len("PROVENANCE")
	nameColumn       = 26
	// Two two-space gutters, plus one column left spare: some terminals wrap when
	// the last cell is written rather than after it.
	listDescriptionColumn = skillsTableWidth - provenanceColumn - nameColumn - 5
)

// skillManifestDescription reads one skill's SKILL.md through a handle pinned
// to that skill's own directory, and reports whether the directory is a skill
// at all. A directory with no manifest is not one — otherwise a skill's own
// references/ subdirectory would be listed as a skill in its own right.
//
// The Lstat decides the no-symlink policy and the SameFile check is what makes
// the decision stick. Lstat-then-read is two syscalls, and in the window
// between them the manifest can be replaced by a symlink that the read would
// follow; comparing the file ox actually opened against the one it inspected
// collapses that window into a refusal. skillmanager.inspectRootFile performs
// the same dance for the files ox owns. It is not shared with this because the
// two disagree about what a refusal MEANS: there, a file ox cannot read is
// fatal, because ox is about to reconcile it; here it is an answer, because
// whatever that directory holds, it is not a skill this listing can describe.
func skillManifestDescription(rootDir *os.Root, name string) (description string, oxOwned bool, isSkill bool) {
	dir, err := rootDir.OpenRoot(name)
	if err != nil {
		return "", false, false
	}
	defer func() { _ = dir.Close() }()

	info, err := dir.Lstat(skills.SkillFileName)
	if err != nil || !info.Mode().IsRegular() {
		return "", false, false // not a skill, or a manifest ox will not read through a symlink
	}
	// Past this point the directory IS a skill — a regular SKILL.md is what makes
	// one — so every remaining failure costs the description and never the row. A
	// listing must not drop a skill because its manifest is missing a permission
	// bit; a row with an empty description is still an answer.
	file, err := dir.Open(skills.SkillFileName)
	if err != nil {
		return "", false, true
	}
	defer func() { _ = file.Close() }()
	actual, err := file.Stat()
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) {
		return "", false, true // swapped between the Lstat and the open; ox declines to read it
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return "", false, true
	}
	description, _ = manifestDescription(data)
	return description, adapterstamp.StampVerifies(data, "ox"), true
}

// manifestDescription extracts `description:` from an Agent Skills manifest.
//
// It reads within the same envelope the Team Context parser uses
// (internal/teamdocs.parseRuleFrontmatter): the first 30 lines, unindented keys
// only. The two returns exist because two callers need different halves of the
// same read:
//
//   - The listings want TEXT, so a block scalar's indented continuation lines
//     are folded in. Rendering the literal ">-" in a description column would
//     make every folded skill in ox's own catalog look broken.
//   - `ox skills publish` wants the SHAPE, because the team-side parser
//     has no block-scalar support for `description:` at all — it would store the
//     marker and drop the text. folded is what lets it refuse rather than send a
//     skill whose activation surface reads ">-" to the team.
func manifestDescription(content []byte) (description string, folded bool) {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	// A description is one long line; the default 64KB token limit is ample, but
	// a manifest with no newline at all would otherwise abort the scan.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var folding bool
	var parts []string
	for line := 0; scanner.Scan() && line < 30; line++ {
		text := strings.TrimRight(scanner.Text(), "\r")
		if line == 0 {
			if text != "---" {
				return "", false // no frontmatter
			}
			continue
		}
		if text == "---" {
			break
		}
		if folding {
			// Indentation is what continues a block scalar; the first unindented
			// line is the next key and ends it.
			if trimmed := strings.TrimSpace(text); trimmed != "" && (text[0] == ' ' || text[0] == '\t') {
				parts = append(parts, trimmed)
				continue
			}
			break
		}
		value, ok := strings.CutPrefix(text, "description:")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch value {
		case ">", ">-", ">+", "|", "|-", "|+":
			folding = true
			continue
		}
		return sanitizeCell(strings.Trim(value, `"'`)), false
	}
	return sanitizeCell(strings.Join(parts, " ")), folding
}

// sanitizeCell makes a manifest-authored string safe to put in a terminal cell.
// A SKILL.md is ordinary user content, but it is content ox did not write, and a
// stray control byte in it can hide the rest of a row.
func sanitizeCell(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, s)
}

// truncateCell clips to width runes, spending the last one on an ellipsis so the
// reader can tell a clipped cell from a short one.
func truncateCell(s string, width int) string {
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	if width <= 1 {
		return string(runes[:width])
	}
	return string(runes[:width-1]) + "…"
}

func pluralSkills(n int) string {
	if n == 1 {
		return "1 skill"
	}
	return fmt.Sprintf("%d skills", n)
}
