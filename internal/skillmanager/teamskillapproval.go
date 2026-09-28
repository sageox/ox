package skillmanager

import (
	"fmt"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/repotools"
	"github.com/sageox/ox/internal/teamdocs"
	"github.com/sageox/ox/internal/teamskills"
)

// TeamSkillCandidate is one team skill that applies to a repository, paired with
// the trust classification of the bytes sitting in the team checkout right now.
type TeamSkillCandidate struct {
	Name             string
	Verdict          teamskills.Verdict
	ManifestRunnable bool
	// LoadErr is set when the skill was discovered but could not be read. It is
	// carried rather than returned so one unreadable skill does not hide every
	// other skill's verdict — the approval surface has to be able to say "these
	// four are fine and this one is broken."
	LoadErr error
}

// ClassifyTeamSkills discovers the team skills that apply to repoRoot and
// classifies each, WITHOUT consulting the approval store.
//
// It exists so that the approval command and the reconcile path derive a digest
// from the same bytes via the same code. Re-reading the skill directory in the
// command would be a second definition of "what was approved", and the two would
// drift the first time discovery changed which files it collects — producing an
// approval whose digest can never match, i.e. a gate that stays closed forever
// with no way to tell why. loadTeamSkill stays unexported for exactly that
// reason: one loader, two callers.
func ClassifyTeamSkills(repoRoot string) ([]TeamSkillCandidate, error) {
	tc := config.FindRepoTeamContext(repoRoot)
	if tc == nil || tc.Path == "" {
		return nil, nil
	}
	// A checkout that is absent or not yet materialized yields no candidates,
	// which is the honest answer: there is nothing to approve yet. The caller
	// distinguishes that from "the team publishes nothing" by way of
	// `ox skills status`, which already separates those five cases.
	// The ORIGIN-derived slug, never the directory-name fallback: this must be
	// the same identity catalogForRepo filters with (teamsource.go), or approval
	// classifies one set of candidates while Plan installs another. In a checkout
	// with no origin remote the fallback is the working directory's name, which
	// no team's `repos:` frontmatter has any reason to match — so the two sides
	// would disagree exactly where the comment above says they must not.
	repoSlug, _ := repotools.RepoSlugFromRemote(repoRoot)
	discovered, err := teamdocs.DiscoverSkills(tc.Path, repoSlug)
	if err != nil {
		return nil, fmt.Errorf("discover team skills: %w", err)
	}

	out := make([]TeamSkillCandidate, 0, len(discovered))
	for _, ts := range discovered {
		loaded, loadErr := loadTeamSkill(ts)
		if loadErr != nil {
			out = append(out, TeamSkillCandidate{Name: ts.Name, LoadErr: loadErr})
			continue
		}
		verdict := teamskills.Classify(loaded)
		out = append(out, TeamSkillCandidate{
			Name: ts.Name, Verdict: verdict,
			ManifestRunnable: manifestIsRunnable(loaded, verdict),
		})
	}
	return out, nil
}
