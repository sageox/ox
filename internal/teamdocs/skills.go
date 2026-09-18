package teamdocs

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// TeamSkill is a skill authored in a team-context repository under
// agents/skills/<name>/SKILL.md.
//
// It reuses the RULE frontmatter contract verbatim — repos:, visibility:,
// status:, audience: — rather than inventing a second targeting mechanism. Teams
// already know that contract from writing rules, and `repos:` IS the per-repo
// filter; a parallel scheme would be one more thing to learn and one more place
// for the two to disagree.
type TeamSkill struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	RelPath     string   `json:"rel_path"` // relative to the skills root, e.g. "deploy/SKILL.md"
	AbsDir      string   `json:"abs_dir"`  // absolute skill directory on disk
	Repos       []string `json:"repos,omitempty"`
	Audience    string   `json:"audience,omitempty"`
	Visibility  string   `json:"visibility"`
	Status      string   `json:"status,omitempty"`
	// Files are the skill's own files, relative to AbsDir, sorted. Populated so a
	// caller can classify and materialize without re-walking the tree.
	Files []string `json:"files,omitempty"`
	// NameError is non-empty when ox REFUSES this skill's name. Such a skill is
	// reported but never installed: DiscoverSkills filters it out, so nothing on
	// the materialization or approval path can see it.
	//
	// It is carried rather than dropped because a skill that silently disappears
	// has no discoverable cause. A team that publishes `Deploy` today would watch
	// it vanish on upgrade with nothing, anywhere, saying it was ever seen — a
	// worse failure than the one the rejection fixes.
	NameError string `json:"name_error,omitempty"`
}

// skillManifestName is the file that makes a directory a skill.
const skillManifestName = "SKILL.md"

// TeamSkillNamePattern is the ONLY shape ox accepts for a team skill's name.
//
// The name is attacker-controlled — free text in `name:` frontmatter, from a
// repository any teammate can push to, over a pull path that verifies no
// signature — and downstream it becomes a DIRECTORY NAME inside the customer's
// repository. filepath.Join Cleans as it joins, so `..` segments in a name walk
// up out of a skills root that is only two segments deep, and the reserved-prefix
// ownership check then rubber-stamps the escape because the name still carries
// the prefix. Landing on .claude/settings.json is arbitrary code execution
// (PreToolUse hook, reconciled automatically on the team pull); landing on
// .sageox/team-skills.approvals.json forges approvals that the repo COMMITS and
// every teammate then pulls.
//
// Lowercase-only because a case-insensitive filesystem resolves `Deploy` and
// `deploy` to one directory while the reserved-prefix ignore globs are
// case-sensitive — the same collision caseVariantDirOnDisk already guards in the
// installer. One canonical case is the only way both can be right.
const TeamSkillNamePattern = `^[a-z0-9][a-z0-9._-]*$`
const MaxTeamSkillNameBytes = 64

var teamSkillNameRE = regexp.MustCompile(TeamSkillNamePattern)

// ValidTeamSkillName reports whether name is safe to use as a skill directory.
//
// REJECT, NEVER SANITIZE. Sanitizing two different bad names into one good name
// creates a collision between two skills, which is a worse bug than the one
// being fixed.
func ValidTeamSkillName(name string) bool {
	// `..` is checked separately because the pattern alone admits it mid-name
	// (`sageox-team-..`), and no legitimate skill name has ever needed it.
	return len(name) >= 1 && len(name) <= MaxTeamSkillNameBytes &&
		!strings.Contains(name, "..") && !strings.HasSuffix(name, ".") &&
		teamSkillNameRE.MatchString(name)
}

// rejectTeamSkillName explains why ox refuses name, or "" when it is fine.
//
// The message carries the REMEDY, not just the rule: this text is what
// `ox skills status` shows the person whose skill did not appear, and a
// diagnostic that states a constraint without stating the fix is a diagnostic
// they have to escalate.
func rejectTeamSkillName(name string) string {
	if ValidTeamSkillName(name) {
		return ""
	}
	const remedy = " — rename it in the Team Context, both the agents/skills/<name>/ directory and the name: key in its SKILL.md"
	if strings.Contains(name, "..") {
		return "unusable name: a team skill name may not contain `..`, which would make it escape the skills directory" + remedy
	}
	if len(name) > MaxTeamSkillNameBytes {
		return fmt.Sprintf("unusable name: a team skill name must be at most %d bytes", MaxTeamSkillNameBytes) + remedy
	}
	if strings.HasSuffix(name, ".") {
		return "unusable name: a team skill name may not end with a dot" + remedy
	}
	return "unusable name: a team skill name must match " + TeamSkillNamePattern +
		" (lowercase letters, digits, then any of . _ -)" + remedy
}

// safeDisplayName makes a refused name safe to put in a terminal.
//
// A refused name is the one attacker-controlled string that still reaches human
// output, so it is the one place an ANSI escape could hide the rest of a
// diagnostic, or a 64KB frontmatter line could bury it. Quoting only when
// necessary keeps the ordinary case — a team that wrote `Deploy` — reading as
// the name they actually typed.
func safeDisplayName(name string) string {
	const maxRunes = 80
	if runes := []rune(name); len(runes) > maxRunes {
		name = string(runes[:maxRunes]) + "…"
	}
	for _, r := range name {
		if !unicode.IsPrint(r) {
			return strconv.Quote(name)
		}
	}
	return name
}

// SkillRoots are the directories inside a team-context checkout that may hold
// skills, in precedence order: agents/skills is canonical and wins a name
// collision, coworkers/skills is the legacy location.
//
// Exported because the daemon has to answer "did this pull touch a skill?" from
// a list of changed paths, and it must answer with the SAME roots discovery
// walks. A second copy of these strings is how a skill authored under the legacy
// root would be found by discovery but never trigger a refresh — present in the
// team repo, absent from every repository, with nothing logged either way.
var SkillRoots = []string{"agents/skills", "coworkers/skills"}

// DiscoverSkills returns the team skills that apply to repoSlug.
//
// Roots mirror DiscoverRules: agents/skills is canonical, coworkers/skills is the
// legacy location, and the canonical one wins on a name collision.
//
// A missing directory is not an error — a team that has authored no skills is the
// normal case, and this runs on paths that may not be checked out at all. Note
// that "the directory is not materialized" and "this team has no skills" produce
// the same empty result here; that ambiguity is GH #862's failure mode, and it is
// addressed upstream by flooring agents/ into the sparse set rather than by
// guessing here.
// Skills whose NAME ox refuses are filtered out here, so no caller on the
// materialization or approval path can see one. Use DiscoverSkillsWithRejections
// to also get the refusals, which a diagnostic must show the human.
func DiscoverSkills(teamPath, repoSlug string) ([]TeamSkill, error) {
	installable, _, err := DiscoverSkillsWithRejections(teamPath, repoSlug)
	return installable, err
}

// DiscoverSkillsWithRejections splits the skills that apply to repoSlug into the
// ones ox will install and the ones whose name it refuses.
//
// Two returns rather than one flagged slice, because the two halves feed
// opposite machinery: the first is projected into the customer's repository, the
// second is only ever rendered. A single slice is how a caller ends up
// materializing something it was told not to.
//
// Refusals are still filtered by `repos:` — a skill targeted at other
// repositories is not this repository's problem to report.
func DiscoverSkillsWithRejections(teamPath, repoSlug string) (installable, rejected []TeamSkill, err error) {
	published, err := PublishedSkills(teamPath)
	if err != nil {
		return nil, nil, err
	}
	for _, s := range published {
		if !SkillAppliesToRepo(s, repoSlug) {
			continue
		}
		if s.NameError != "" {
			rejected = append(rejected, s)
			continue
		}
		installable = append(installable, s)
	}
	return installable, rejected, nil
}

// PublishedSkills returns every skill the team publishes to AI coworkers,
// before the per-repo `repos:` filter is applied.
//
// Split out from DiscoverSkills so a diagnostic can tell "the team published
// nothing" apart from "the team published this for other repos." Those are
// indistinguishable once the filter has run, and they need opposite answers
// from the human: author a skill, versus widen a `repos:` list.
//
// The audience/visibility/status filters are NOT repo-specific and stay here: a
// draft or human-only skill is not published to coworkers anywhere, so surfacing
// it as "available elsewhere" would be wrong too.
func PublishedSkills(teamPath string) ([]TeamSkill, error) {
	if teamPath == "" {
		return nil, nil
	}

	var skills []TeamSkill
	for _, root := range SkillRoots {
		discovered, err := walkSkillsDir(filepath.Join(teamPath, root))
		if err != nil {
			return nil, err
		}
		skills = append(skills, discovered...)
	}

	// Dedupe by name; agents/skills is walked first, so it wins.
	seen := make(map[string]bool, len(skills))
	deduped := skills[:0]
	for _, s := range skills {
		if seen[s.Name] {
			continue
		}
		seen[s.Name] = true
		deduped = append(deduped, s)
	}
	skills = deduped

	filtered := skills[:0]
	for _, s := range skills {
		if s.Audience == RuleAudienceHuman {
			continue
		}
		if s.Visibility == VisibilityHidden {
			continue
		}
		if s.Status == RuleStatusDraft {
			continue
		}
		if strings.HasPrefix(s.Status, RuleStatusSupersededPrefix) {
			continue
		}
		filtered = append(filtered, s)
	}
	skills = filtered

	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills, nil
}

// SkillAppliesToRepo applies the `repos:` filter with the same semantics as
// rules: an empty list means every repo, and an unknown slug matches only
// unfiltered skills.
//
// The unknown-slug case is deliberately conservative. Defaulting to "include"
// when ox cannot tell which repo it is in would push a team skill into
// repositories its author never listed.
func SkillAppliesToRepo(s TeamSkill, repoSlug string) bool {
	if len(s.Repos) == 0 {
		return true
	}
	if repoSlug == "" {
		return false
	}
	for _, want := range s.Repos {
		if strings.EqualFold(strings.TrimSpace(want), repoSlug) {
			return true
		}
	}
	return false
}

// walkSkillsDir finds every <name>/SKILL.md one level under absRoot.
//
// Only one level: a skill is a directory with a manifest, and recursing would
// make a skill's own references/ or assets/ subdirectory look like a second
// skill.
func walkSkillsDir(absRoot string) ([]TeamSkill, error) {
	entries, err := os.ReadDir(absRoot)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var skills []TeamSkill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(absRoot, e.Name())
		manifest := filepath.Join(dir, skillManifestName)
		if info, statErr := os.Lstat(manifest); statErr != nil || !info.Mode().IsRegular() {
			continue // not a skill, or a manifest ox will not read through a symlink
		}

		fm := parseRuleFrontmatter(manifest)
		name := fm.Name
		if name == "" {
			name = e.Name()
		}
		// Refused rather than dropped: the skill stays in the result carrying its
		// reason, so `ox skills status` can say WHY it is missing. DiscoverSkills
		// filters it out before anything installs. It is NOT a hard error — one
		// malformed skill must not stop every other skill in the team from
		// reaching every repository.
		nameErr := rejectTeamSkillName(name)
		if nameErr != "" {
			slog.Warn("team skill refused: unusable name",
				"dir", e.Name(), "name", safeDisplayName(name), "pattern", TeamSkillNamePattern)
			name = safeDisplayName(name)
		}
		files, filesErr := skillFiles(dir)
		if filesErr != nil {
			return nil, filesErr
		}

		skills = append(skills, TeamSkill{
			Name:        name,
			NameError:   nameErr,
			Description: fm.Description,
			RelPath:     filepath.ToSlash(filepath.Join(e.Name(), skillManifestName)),
			AbsDir:      dir,
			Repos:       fm.Repos,
			Audience:    fm.Audience,
			Visibility:  defaultString(fm.Visibility, DefaultRuleVisibility),
			Status:      fm.Status,
			Files:       files,
		})
	}
	return skills, nil
}

// skillFiles lists a skill's regular files, relative to its directory.
//
// Symlinks are skipped rather than followed: the team checkout is mutable under
// the daemon, and following a link out of it would materialize content from an
// arbitrary path on the machine.
func skillFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !info.Mode().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(dir, p)
		if relErr != nil {
			return relErr
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

func defaultString(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
