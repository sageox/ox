package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/skillmanager"
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
//
// `list` is deliberately not `catalog` with a flag. They answer different
// questions — "what do my agents have" versus "what could I add" — and only one
// of them is about this repository's disk.

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

To see what ox ships that is NOT installed here, run ` + "`ox skills catalog`" + `.`,
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
			roots = append(roots, target.Root)
		}
	}
	return dedupeStrings(roots), nil
}

// collectInstalledSkills walks the skill roots and classifies what is there,
// WITHOUT printing. repoRoot is a parameter rather than something this goes
// looking for, so a test can point it at a scratch repo without moving the
// process.
func collectInstalledSkills(repoRoot string, roots []string) skillsListOutput {
	out := skillsListOutput{Roots: dedupeStrings(roots), Skills: []installedSkillRow{}, Problems: []string{}}

	// Indexed by name, not by (root, name): the same skill in two roots is one
	// skill with two homes, and two rows would read as two skills.
	byName := map[string]*installedSkillRow{}
	for _, root := range out.Roots {
		dir := filepath.Join(repoRoot, filepath.FromSlash(root))
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			// A selected root that was never materialized is a normal state for a
			// fresh checkout, and `ox skills status` is the surface that explains it.
			continue
		}
		if err != nil {
			// Not silently empty: an unreadable root and an empty one look identical
			// in the table, and they need opposite fixes.
			out.Problems = append(out.Problems, fmt.Sprintf("ox could not read the skills directory %s: %v", root, err))
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			manifest := filepath.Join(dir, entry.Name(), skills.SkillFileName)
			info, statErr := os.Lstat(manifest)
			if statErr != nil || !info.Mode().IsRegular() {
				continue // not a skill, or a manifest ox will not read through a symlink
			}
			row, ok := byName[entry.Name()]
			if !ok {
				row = &installedSkillRow{
					Name:        entry.Name(),
					Provenance:  skillProvenance(entry.Name()),
					Description: manifestDescriptionFile(manifest),
					Roots:       []string{},
				}
				byName[entry.Name()] = row
			}
			row.Roots = append(row.Roots, root)
		}
	}

	for _, row := range byName {
		out.Skills = append(out.Skills, *row)
	}
	sort.Slice(out.Skills, func(i, j int) bool {
		if a, b := provenanceRank[out.Skills[i].Provenance], provenanceRank[out.Skills[j].Provenance]; a != b {
			return a < b
		}
		return out.Skills[i].Name < out.Skills[j].Name
	})
	out.Guidance = skillsListGuidance(out)
	return out
}

// skillProvenance answers "who owns this directory?"
//
// The reserved namespaces are the ownership contract (skillmanager.IsReservedName)
// and settle almost every row. Two things they do not settle:
//
//   - The committed on-ramp is deliberately UNPREFIXED so it can never match a
//     reserved glob, so it needs an exact-match arm or ox's own file lands under
//     `local`.
//   - The catalog may ship a skill under a name in no reserved namespace at all —
//     `post-cutoff`, the first entry in the opt-in `team` bundle, is one. Nothing
//     about its name says ox, so the catalog itself has to be asked, last.
//
// That final arm means a hand-authored skill that happens to share a name with
// one ox ships is reported as ox's. That is the lesser error: ox's reconcile
// already claims the path, so calling it `local` would promise a protection the
// installer does not actually give.
func skillProvenance(name string) string {
	switch {
	case strings.HasPrefix(name, skillmanager.TeamPrefix):
		return provenanceTeam
	case name == skillmanager.CommittedOnRamp || name == skillmanager.CLIBase ||
		strings.HasPrefix(name, skillmanager.CLIPrefix):
		return provenanceOx
	case skills.IsKnown(name):
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
		return "No skills are installed in " + strings.Join(out.Roots, ", ") + " — run `ox skills catalog` to see what ox ships."
	}
	counts := map[string]int{}
	for _, row := range out.Skills {
		counts[row.Provenance]++
	}
	return fmt.Sprintf("%s installed: %d from ox, %d from your team, %d your own. Run `ox skills catalog` to see what else ox ships.",
		pluralSkills(len(out.Skills)), counts[provenanceOx], counts[provenanceTeam], counts[provenanceLocal])
}

func emitSkillsList(w io.Writer, out skillsListOutput, asJSON bool) error {
	if asJSON {
		return encodeSkillsJSON(w, out)
	}
	p := func(format string, args ...any) { fmt.Fprintf(w, format+"\n", args...) }

	if len(out.Skills) > 0 {
		p("%s", cli.StyleAccent.Render(fmt.Sprintf("%-*s  %-*s  %s",
			provenanceColumn, "PROVENANCE", nameColumn, "NAME", "DESCRIPTION")))
		for _, row := range out.Skills {
			p("%-*s  %-*s  %s", provenanceColumn, row.Provenance,
				nameColumn, truncateCell(row.Name, nameColumn),
				truncateCell(row.Description, listDescriptionColumn))
		}
	}

	if len(out.Problems) > 0 {
		if len(out.Skills) > 0 {
			p("")
		}
		p("%s", cli.StyleError.Render("Directories ox could not read"))
		for _, problem := range out.Problems {
			writeWrapped(w, "  • ", "    ", problem)
		}
	}

	if out.Guidance != "" {
		if len(out.Skills) > 0 || len(out.Problems) > 0 {
			p("")
		}
		writeWrapped(w, "", "", out.Guidance)
	}
	return nil
}

// writeWrapped prints text broken on spaces so no line runs past
// skillsTableWidth, with first prefixing the opening line and cont every
// continuation.
//
// A long word is never split: a path, a filename, or a backticked command broken
// across two lines cannot be copied out of the terminal, and every guidance
// string in this family exists to be copied.
func writeWrapped(w io.Writer, first, cont, text string) {
	prefix := first
	line := ""
	flush := func() {
		fmt.Fprintf(w, "%s%s\n", prefix, line)
		prefix, line = cont, ""
	}
	for _, word := range strings.Fields(text) {
		switch {
		case line == "":
			line = word
		case len([]rune(prefix))+len([]rune(line))+1+len([]rune(word)) <= skillsTableWidth:
			line += " " + word
		default:
			flush()
			line = word
		}
	}
	if line != "" {
		flush()
	}
}

// Column widths for the tables in this command family.
//
// They are derived from skillsTableWidth rather than typed as literals so a
// change to one cannot silently push a row past the edge — a table that wraps is
// not a table, and the wrap only shows up on someone else's terminal.
const (
	skillsTableWidth = 80

	provenanceColumn = 10 // len("PROVENANCE")
	nameColumn       = 26
	// Two two-space gutters, plus one column left spare: some terminals wrap when
	// the last cell is written rather than after it.
	listDescriptionColumn = skillsTableWidth - provenanceColumn - nameColumn - 5
)

func encodeSkillsJSON(w io.Writer, payload any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

// manifestDescriptionFile reads a SKILL.md's description, tolerating an
// unreadable file: a listing must not fail because one skill's manifest is
// missing a permission bit.
func manifestDescriptionFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	description, _ := manifestDescription(data)
	return description
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
//   - `ox skills install --team` wants the SHAPE, because the team-side parser
//     has no block-scalar support for `description:` at all — it would store the
//     marker and drop the text. folded is what lets that command refuse rather
//     than publish a skill whose activation surface reads ">-".
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

func dedupeStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func pluralSkills(n int) string {
	if n == 1 {
		return "1 skill"
	}
	return fmt.Sprintf("%d skills", n)
}
