package teamconverge

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/teamdocs"
)

// OriginResolver maps a normalized Team Context path to Pack ownership. A nil
// resolver means the artifact was authored directly by the team.
type OriginResolver func(sourcePath string) Origin

type FilesystemDiscovery struct {
	ResolveOrigin OriginResolver
}

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

	publishedSkills, err := teamdocs.PublishedSkills(request.TeamPath)
	if err != nil {
		return Snapshot{}, nil, fmt.Errorf("discover Team Context skills: %w", err)
	}
	for _, skill := range publishedSkills {
		rel := sourceRel(request.TeamPath, skill.AbsDir)
		applicable := teamdocs.SkillAppliesToRepo(skill, request.RepoSlug)
		artifact := Artifact{
			Kind: KindSkill, Name: skill.Name, SourcePath: rel,
			Origin: d.origin(rel), Applicable: applicable, Required: true,
			Visibility: skill.Visibility,
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
			Origin: d.origin(rel), Applicable: applicable, Required: true,
			Visibility: rule.Visibility, Description: rule.Description,
			Globs: append([]string(nil), rule.Globs...),
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
			Origin: d.origin(rel), Applicable: true, Required: true,
			Visibility: doc.Visibility,
		})
	}
	return snapshot, artifacts, nil
}

func (d FilesystemDiscovery) origin(sourcePath string) Origin {
	if d.ResolveOrigin != nil {
		if origin := d.ResolveOrigin(sourcePath); origin.Kind != "" {
			return origin
		}
	}
	return Origin{Kind: OriginLoose}
}

func sourceRel(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}
