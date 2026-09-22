package teamconverge

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/addons"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/teamdocs"
)

// FilesystemDiscovery is ox's one production Discovery: it walks a Team
// Context git checkout for typed artifacts. Every artifact it produces is
// loose (hand-authored) — there is no Pack producer yet, so origin is always
// OriginLoose.
type FilesystemDiscovery struct{}

func (d FilesystemDiscovery) Discover(ctx context.Context, request Request) (Snapshot, []Artifact, error) {
	if request.TeamPath == "" {
		return Snapshot{}, nil, fmt.Errorf("team context path is required")
	}
	commit := strings.TrimSpace(request.TeamCommit)
	if commit == "" {
		out, err := gitutil.RunGit(ctx, request.TeamPath, "rev-parse", "--verify", "HEAD")
		if err != nil {
			return Snapshot{}, nil, fmt.Errorf("resolve Team Context commit: %w", err)
		}
		commit = strings.TrimSpace(out)
	}
	snapshot := Snapshot{Path: request.TeamPath, Commit: commit}
	var artifacts []Artifact

	// Loaded ONCE per pass, not per artifact: this walk produces every skill,
	// rule and doc in the Team Context, and re-reading the lock for each one
	// turns a single file read into a tax proportional to team content on
	// every `ox sync`.
	//
	// A read failure is returned, never swallowed. Treating an unreadable lock
	// as "no add-ons" would reattribute every add-on artifact to the team and
	// look exactly like a Team Context that has none.
	lock, err := addons.LoadLock(request.TeamPath)
	if err != nil {
		return Snapshot{}, nil, fmt.Errorf("read add-on selection: %w", err)
	}
	origins := NewOriginResolver(lock)

	publishedSkills, err := teamdocs.PublishedSkills(request.TeamPath)
	if err != nil {
		return Snapshot{}, nil, fmt.Errorf("discover Team Context skills: %w", err)
	}
	for _, skill := range publishedSkills {
		rel := sourceRel(request.TeamPath, skill.AbsDir)
		applicable := teamdocs.SkillAppliesToRepo(skill, request.RepoSlug)
		artifact := Artifact{
			Kind: KindSkill, Name: skill.Name, SourcePath: rel,
			Origin: origins.Resolve(rel), Applicable: applicable, Required: true,
			Visibility: skill.Visibility,
			skill:      &skill,
		}
		if !applicable {
			artifact.FilterReason = "repos filter does not include this repository"
		}
		artifacts = append(artifacts, artifact)
	}

	publishedRules, err := teamdocs.PublishedRules(request.TeamPath)
	if err != nil {
		return Snapshot{}, nil, fmt.Errorf("discover Team Context rules: %w", err)
	}
	for _, rule := range publishedRules {
		rel := sourceRel(request.TeamPath, rule.AbsPath)
		applicable := teamdocs.RuleAppliesToRepo(rule, request.RepoSlug)
		artifact := Artifact{
			Kind: KindRule, Name: rule.Name, SourcePath: rel,
			Origin: origins.Resolve(rel), Applicable: applicable, Required: true,
			Visibility: rule.Visibility, Description: rule.Description,
			Globs: append([]string(nil), rule.Globs...),
			rule:  &rule,
		}
		if !applicable {
			artifact.FilterReason = "repos filter does not include this repository"
		}
		artifacts = append(artifacts, artifact)
	}

	docs, err := teamdocs.DiscoverDocs(request.TeamPath)
	if err != nil {
		return Snapshot{}, nil, fmt.Errorf("discover Team Context docs: %w", err)
	}
	for _, doc := range docs {
		rel := sourceRel(request.TeamPath, doc.Path)
		artifacts = append(artifacts, Artifact{
			Kind: KindContext, Name: doc.Name, SourcePath: rel,
			Origin: origins.Resolve(rel), Applicable: true, Required: true,
			Visibility: doc.Visibility,
		})
	}
	return snapshot, artifacts, nil
}

func sourceRel(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}
