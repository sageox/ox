package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/paths"
)

// tryHTTPTeamContextFallback fetches a team's context over HTTP and writes
// it to the canonical team-context directory, then re-invokes the normal
// loader path. This is the ephemeral-mode fallback for environments
// where no daemon and no local clone of the team-context repo exist.
//
// Returns:
//   - (*teamContextInfo, nil) on success
//   - (nil, nil) when the cloud endpoint isn't deployed yet
//     (api.ErrTeamContextEndpointUnavailable) so callers fall through
//   - (nil, err) on a real failure (network, auth, decode)
//
// The caller is expected to already know teamID — usually from
// ProjectConfig.TeamID — and to have decided ephemeral mode is in effect.
func tryHTTPTeamContextFallback(ctx context.Context, projectRoot, teamID string) (*teamContextInfo, error) {
	if projectRoot == "" || teamID == "" {
		return nil, fmt.Errorf("projectRoot and teamID required")
	}

	ep := endpoint.GetForProject(projectRoot)
	if ep == "" {
		return nil, fmt.Errorf("no endpoint configured for project")
	}

	client := api.NewRepoClientForProject(projectRoot)
	if token, err := auth.GetTokenForEndpoint(ep); err == nil && token != nil && token.AccessToken != "" {
		client = client.WithAuthToken(token.AccessToken)
	}

	resp, err := client.GetTeamContextContent(ctx, teamID)
	if err != nil {
		if errors.Is(err, api.ErrTeamContextEndpointUnavailable) {
			slog.Debug("ephemeral team-context fallback: endpoint unavailable",
				"team_id", teamID, "endpoint", ep)
			return nil, nil
		}
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}

	teamCtxDir := paths.TeamContextDir(teamID, ep)
	if teamCtxDir == "" {
		return nil, fmt.Errorf("could not resolve team-context dir for team_id=%s", teamID)
	}

	if err := fetchTeamContextViaAPI(teamCtxDir, resp); err != nil {
		return nil, fmt.Errorf("failed to write team context: %w", err)
	}

	slog.Debug("ephemeral team-context fallback: wrote context",
		"team_id", teamID, "dir", teamCtxDir, "docs", len(resp.Docs))

	// re-invoke the existing loader path now that files are on disk
	repoSlug := ""
	if pc, err := config.LoadProjectConfig(projectRoot); err == nil && pc != nil {
		repoSlug = repoSlugFromProjectConfig(pc)
	}
	info := discoverTeamContextLocalOnly(projectRoot, repoSlug)
	return info, nil
}

// fetchTeamContextViaAPI writes the HTTP-fetched team context to disk
// under teamCtxDir. Each file uses an atomic temp-file + rename pattern
// so a partial fetch never leaves a half-written file in place.
func fetchTeamContextViaAPI(teamCtxDir string, resp *api.TeamContextContentResponse) error {
	if resp == nil {
		return fmt.Errorf("nil response")
	}
	if err := os.MkdirAll(teamCtxDir, 0o755); err != nil {
		return fmt.Errorf("mkdir team-context dir: %w", err)
	}

	for _, doc := range resp.Docs {
		if doc.Name == "" {
			continue
		}
		// preserve subdirectories embedded in doc.Name (e.g. "guides/x.md")
		rel := filepath.Clean(doc.Name)
		if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			// defensive: refuse to write outside teamCtxDir
			slog.Debug("ephemeral team-context fallback: skipping unsafe doc path",
				"name", doc.Name)
			continue
		}
		destPath := filepath.Join(teamCtxDir, "docs", rel)
		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return fmt.Errorf("mkdir doc parent %s: %w", destPath, err)
		}
		if err := atomicWriteFile(destPath, []byte(doc.Content), 0o644); err != nil {
			return fmt.Errorf("write doc %s: %w", destPath, err)
		}
	}

	type rootFile struct {
		name    string
		content string
	}
	rootFiles := []rootFile{
		{"AGENTS.md", resp.AgentsMD},
		{"CLAUDE.md", resp.ClaudeMD},
		{"MEMORY.md", resp.Memory},
	}
	for _, rf := range rootFiles {
		if rf.content == "" {
			continue
		}
		dest := filepath.Join(teamCtxDir, rf.name)
		if err := atomicWriteFile(dest, []byte(rf.content), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", rf.name, err)
		}
	}

	return nil
}

// atomicWriteFile writes content to path via a temp file in the same
// directory, then renames it into place. This keeps the destination
// file either intact (old content) or fully updated (new content) —
// never partially written.
func atomicWriteFile(path string, content []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	// best-effort cleanup if anything below fails before the rename
	defer func() {
		if _, statErr := os.Stat(tmpName); statErr == nil {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}

// discoverTeamContextLocalOnly is the local-only re-entry point used after
// fetchTeamContextViaAPI lands files on disk. It is a thin wrapper around
// the existing discoverTeamContext call — separating it out documents the
// fact that the second call must NOT recurse back into the HTTP fallback.
func discoverTeamContextLocalOnly(projectRoot, repoSlug string) *teamContextInfo {
	return discoverTeamContextWithFallback(projectRoot, repoSlug, false)
}

// repoSlugFromProjectConfig assembles an "owner/repo" slug from the
// project config fields that exist. Returns empty when no useful slug
// can be derived — the caller treats empty as "skip repo-scoped rule
// filtering" which is the safe default.
func repoSlugFromProjectConfig(pc *config.ProjectConfig) string {
	if pc == nil {
		return ""
	}
	if pc.Org != "" && pc.Project != "" {
		return pc.Org + "/" + pc.Project
	}
	return ""
}
