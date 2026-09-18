package teamdocs

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
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
}

// skillManifestName is the file that makes a directory a skill.
const skillManifestName = "SKILL.md"

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
func DiscoverSkills(teamPath, repoSlug string) ([]TeamSkill, error) {
	published, err := PublishedSkills(teamPath)
	if err != nil {
		return nil, err
	}
	out := published[:0]
	for _, s := range published {
		if SkillAppliesToRepo(s, repoSlug) {
			out = append(out, s)
		}
	}
	return out, nil
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
		files, filesErr := skillFiles(dir)
		if filesErr != nil {
			return nil, filesErr
		}

		skills = append(skills, TeamSkill{
			Name:        name,
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
