package skillmanager

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	skills "github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/teamdocs"
	"github.com/sageox/ox/internal/teamskills"
)

// TeamSkillDecision records what happened to one discovered team skill, so a
// caller can tell a human WHY a skill they authored is not on disk.
//
// Silence is the failure mode this exists to prevent: a skill held for approval
// and a skill that was never discovered look identical from the repository.
type TeamSkillDecision struct {
	Name         string
	InstalledAs  string
	NeedsApprove bool
	Reason       string
}

// teamCatalog unions the binary's built-in catalog with the team skills this
// project is allowed to materialize.
//
// Team skills enter through the SAME seam as built-in ones, so they inherit the
// whole ownership model already proven for the CLI inventory: digest-tracked
// files, unconditional overwrite inside the reserved prefix, removal when they
// leave desired state. The alternative — a second installer beside the first —
// is exactly the two-mechanism split that let five orphaned command files
// survive across releases.
//
// They are COPIES, never links. The team checkout is mutable under the daemon (a
// sparse refresh deletes tracked files), and a background `git pull` must never
// silently change what a coding agent executes.
type teamCatalog struct {
	base      catalogSource
	teamFiles []skills.Skill
	digestSum string
}

// TeamSkillSource builds a catalog source that adds approved team skills.
//
// It reverses the pointer-rule decision recorded in the Claude adapter — "Team
// rules stay where the team writes them. No sync. No cleanup." — and does so
// deliberately: that ruling predates the reserved-prefix inventory, which is what
// makes cleanup safe. Saying so openly because the comment it contradicts is
// still in the tree.
//
// teamPath is the team-context checkout. It is READ ONLY here: no git handle is
// opened, so this cannot race the daemon's pull. Callers that need the checkout
// to be current must take ADR-030's per-clone lease around their own refresh
// before calling.
func TeamSkillSource(base catalogSource, teamPath, repoSlug, projectRoot string) (catalogSource, []TeamSkillDecision, error) {
	if base == nil {
		base = builtInCatalog{}
	}
	if teamPath == "" {
		return base, nil, nil
	}

	discovered, err := teamdocs.DiscoverSkills(teamPath, repoSlug)
	if err != nil {
		return nil, nil, fmt.Errorf("discover team skills: %w", err)
	}
	if len(discovered) == 0 {
		return base, nil, nil
	}

	approvals, err := teamskills.LoadApprovals(projectRoot)
	if err != nil {
		// A store ox cannot read is NOT an empty store. Materializing executable
		// content because the approval file was unparseable is the fail-open shape
		// this codebase keeps finding; refuse and let the human see it.
		return nil, nil, fmt.Errorf("team skill approvals unreadable, refusing to materialize: %w", err)
	}

	var (
		allowed   []skills.Skill
		decisions []TeamSkillDecision
	)
	for _, ts := range discovered {
		loaded, loadErr := loadTeamSkill(ts)
		if loadErr != nil {
			decisions = append(decisions, TeamSkillDecision{
				Name: ts.Name, Reason: "unreadable: " + loadErr.Error(),
			})
			continue
		}
		verdict := teamskills.Classify(loaded)
		if approvals.Decide(ts.Name, verdict) == teamskills.DecisionNeedsApproval {
			decisions = append(decisions, TeamSkillDecision{
				Name: ts.Name, NeedsApprove: true,
				Reason: "needs approval: " + verdict.Describe(),
			})
			continue
		}

		installed := TeamPrefix + ts.Name
		allowed = append(allowed, skills.Skill{
			Name:    installed,
			Content: manifestContent(loaded),
			Files:   toCatalogFiles(loaded, approvals.ScriptsExecutable(ts.Name, verdict)),
		})
		decisions = append(decisions, TeamSkillDecision{Name: ts.Name, InstalledAs: installed})
	}

	sort.Slice(allowed, func(i, j int) bool { return allowed[i].Name < allowed[j].Name })
	return &teamCatalog{base: base, teamFiles: allowed, digestSum: teamDigest(allowed)}, decisions, nil
}

func (c *teamCatalog) Digest() (string, error) {
	base, err := c.base.Digest()
	if err != nil {
		return "", err
	}
	// The team half must be IN the digest. Prime compares this against the recorded
	// revision to decide whether to re-plan, so a digest covering only the built-in
	// catalog would leave an edited team skill stale until something else changed.
	return base + "+team:" + c.digestSum, nil
}

func (c *teamCatalog) Select(version string, desired DesiredSkills) ([]skills.Skill, error) {
	base, err := c.base.Select(version, desired)
	if err != nil {
		return nil, err
	}
	out := make([]skills.Skill, 0, len(base)+len(c.teamFiles))
	out = append(out, base...)
	for _, s := range c.teamFiles {
		s.Version = version
		out = append(out, s)
	}
	return out, nil
}

// loadTeamSkill reads a discovered skill's bytes for classification.
func loadTeamSkill(ts teamdocs.TeamSkill) (teamskills.Skill, error) {
	out := teamskills.Skill{Name: ts.Name}
	for _, rel := range ts.Files {
		abs := filepath.Join(ts.AbsDir, filepath.FromSlash(rel))
		data, err := os.ReadFile(abs)
		if err != nil {
			return teamskills.Skill{}, err
		}
		out.Files = append(out.Files, teamskills.File{Path: rel, Content: data})
	}
	return out, nil
}

func manifestContent(s teamskills.Skill) []byte {
	for _, f := range s.Files {
		if f.Path == "SKILL.md" {
			return f.Content
		}
	}
	return nil
}

// toCatalogFiles converts a team skill's files for materialization.
//
// scripts/ are dropped entirely unless the approver explicitly allowed them.
// Writing them non-executable would still put runnable content on disk that an
// agent is invited to `sh` — the file mode is not the boundary, the presence of
// the file is.
func toCatalogFiles(s teamskills.Skill, allowScripts bool) []skills.File {
	var out []skills.File
	for _, f := range s.Files {
		clean := filepath.ToSlash(filepath.Clean(f.Path))
		if !allowScripts && (clean == "scripts" || strings.HasPrefix(clean, "scripts/")) {
			continue
		}
		out = append(out, skills.File{Path: clean, Content: f.Content})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func teamDigest(list []skills.Skill) string {
	h := sha256.New()
	for _, s := range list {
		fmt.Fprintf(h, "skill:%s\n", s.Name)
		for _, f := range s.Files {
			fmt.Fprintf(h, "file:%s:%d\n", f.Path, len(f.Content))
			h.Write(f.Content)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}
