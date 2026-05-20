package main

// Knowledge-bubble project-config migration check (ADR-017).
//
// This check graduates a repo from "JSON only" (legacy) to "YAML present"
// (ADR-017). The hybrid loader (LoadProjectConfig) keeps both formats working
// during the staged migration window, but doctor is what actually moves repos
// forward. Four on-disk shapes are handled:
//
//   - neither file present → no-op pass (nothing to migrate)
//   - JSON only            → resolve kb_id via API, write config.yaml. Orphan
//                            repos (no matching kb row) return a Warning so
//                            the user can re-run `ox init` or wait for the
//                            server to provision.
//   - YAML only            → no-op pass (migration already complete)
//   - both present         → reconcile. If kb_id+repo_id are equivalent across
//                            the two files, archive the JSON. If they diverge,
//                            YAML wins (per ADR-017 §6) and the JSON is still
//                            archived but a divergence line is logged.
//
// Archiving = copy JSON to `.sageox/config.json.bak.<RFC3339>` then delete the
// original. We deliberately never delete without first writing the archive —
// the staged-deprecation plan in ADR-017 §7 promises the legacy data is
// recoverable until release N+3.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/config"
)

// CheckSlugKBProjectConfigMigrate is the doctor check slug for the
// JSON-to-YAML project-config migration. AutoFix because every write the
// check performs is non-destructive in the recoverable sense: new files
// (config.yaml) live next to the legacy JSON, and the JSON itself is
// archived to a timestamped `.bak` before removal.
const CheckSlugKBProjectConfigMigrate = "kb-project-config-migrate"

// kbMigrateAPITimeout caps the kb-API call. The check runs synchronously
// inside `ox doctor`, so a slow API must not block the user — a missed
// migration this pass just retries on the next doctor run.
const kbMigrateAPITimeout = 10 * time.Second

// checkKBProjectConfigMigrate dispatches to one of four handlers based on
// which files exist under .sageox/. Each handler is responsible for its own
// no-op / warning / fix path so the dispatcher stays trivial to audit.
func checkKBProjectConfigMigrate(fix bool) checkResult {
	const name = "kb project config migrate"

	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck(name, "not in a git repo", "")
	}

	sageoxPath := filepath.Join(gitRoot, ".sageox")
	jsonPath := filepath.Join(sageoxPath, "config.json")
	yamlPath := filepath.Join(sageoxPath, "config.yaml")

	jsonExists, err := pathExists(jsonPath)
	if err != nil {
		return SkippedCheck(name, "config.json status unreadable", err.Error())
	}
	yamlExists, err := pathExists(yamlPath)
	if err != nil {
		return SkippedCheck(name, "config.yaml status unreadable", err.Error())
	}

	switch {
	case !jsonExists && !yamlExists:
		return PassedCheck(name, "no project config files")
	case jsonExists && !yamlExists:
		return migrateJSONOnly(name, fix, gitRoot, sageoxPath, jsonPath, yamlPath)
	case !jsonExists && yamlExists:
		return PassedCheck(name, "yaml-only — migration complete")
	default: // both present
		return reconcileBoth(name, fix, sageoxPath, jsonPath, yamlPath)
	}
}

// migrateJSONOnly handles the legacy → new format transition. Orphan repos
// (no kb row matching repo_id) surface as a Warning rather than Failed: the
// repo still functions through the JSON config; what's missing is the
// server-side provisioning that would make the YAML binding possible.
func migrateJSONOnly(name string, fix bool, gitRoot, sageoxPath, _, yamlPath string) checkResult {
	cfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil {
		return SkippedCheck(name, "project config unreadable", err.Error())
	}
	if cfg == nil || cfg.RepoID == "" {
		// No repo_id means the project was never fully initialized via
		// `ox init`. Skip rather than invent a binding.
		return SkippedCheck(name, "project missing repo_id", "")
	}

	if !fix {
		return WarningCheck(name,
			"legacy .sageox/config.json without config.yaml",
			"run `ox doctor --fix` to write the new YAML binding (ADR-017)")
	}

	// Resolve kb_id from repo_id via the kb API. ErrKBAPIUnavailable is the
	// "feature flag off / no auth" sentinel — treat it as "try again later"
	// so offline doctor runs stay silent.
	ctx, cancel := context.WithTimeout(context.Background(), kbMigrateAPITimeout)
	defer cancel()
	kbID, lookupErr := resolveKBIDForRepo(ctx, cfg.RepoID)
	if lookupErr != nil {
		if errors.Is(lookupErr, api.ErrKBAPIUnavailable) {
			slog.Default().Debug("kb_doctor migrate: api unavailable, will retry next run",
				"repo_id", cfg.RepoID)
			return PassedCheck(name, "api unavailable; will retry next doctor run")
		}
		return WarningCheck(name, "kb lookup failed", lookupErr.Error())
	}
	if kbID == "" {
		// Orphan repo: repo_id exists locally but the server has no matching
		// knowledge bubble. The CLI cannot fix this — the user needs to
		// re-run init or wait for server-side provisioning. Warning, not
		// Failed: the repo continues to function via the JSON config.
		slog.Default().Info("kb_doctor migrate: orphan repo, no matching kb",
			"repo_id", cfg.RepoID)
		return WarningCheck(name,
			"orphan repo: repo_id="+cfg.RepoID+" has no matching kb in the API",
			"re-run `ox init` or wait for the server to provision a knowledge bubble for this repo")
	}

	payload := *cfg
	payload.KBID = kbID
	if err := writeProjectYAMLAtomic(sageoxPath, yamlPath, payload); err != nil {
		return FailedCheck(name, "write config.yaml failed", err.Error())
	}

	slog.Default().Info("kb_doctor migrate wrote config.yaml",
		"git_root", gitRoot,
		"kb_id", kbID,
		"repo_id", cfg.RepoID)
	return PassedCheck(name, "wrote .sageox/config.yaml (kb_id="+kbID+")")
}

// reconcileBoth handles the "both files present" case. The two files are
// considered equivalent when their kb_id and repo_id agree — those are the
// only fields the narrow YAML binding carries (ADR-017 §2), so any other
// JSON-only fields are preserved by the archive itself.
//
// On equivalence: archive JSON, done.
// On divergence: YAML wins (per ADR-017 §6), archive JSON, log which fields
// differed so the user has an audit trail of what was discarded.
func reconcileBoth(name string, fix bool, sageoxPath, jsonPath, yamlPath string) checkResult {
	jsonCfg, jsonErr := readJSONConfigBinding(jsonPath)
	if jsonErr != nil {
		return SkippedCheck(name, "config.json unreadable", jsonErr.Error())
	}
	yamlCfg, yamlErr := readYAMLConfigBinding(yamlPath)
	if yamlErr != nil {
		return SkippedCheck(name, "config.yaml unreadable", yamlErr.Error())
	}

	identical := configsEquivalent(jsonCfg, yamlCfg)

	if !fix {
		if identical {
			return WarningCheck(name,
				"legacy config.json present alongside config.yaml (identical)",
				"run `ox doctor --fix` to archive the legacy .sageox/config.json")
		}
		return WarningCheck(name,
			"config.json and config.yaml diverge",
			"run `ox doctor --fix` to archive the legacy JSON (YAML wins per ADR-017)")
	}

	if !identical {
		// Single-line key=value divergence log so it survives grep/log
		// aggregation. The archive itself is the recoverable record; the log
		// is the human-readable "here's what was different" trail.
		slog.Default().Info("kb_doctor migrate divergence",
			"json_kb_id", jsonCfg.KBID,
			"yaml_kb_id", yamlCfg.KBID,
			"json_repo_id", jsonCfg.RepoID,
			"yaml_repo_id", yamlCfg.RepoID,
			"winner", "yaml")
	}

	backupPath, err := archiveJSON(sageoxPath, jsonPath)
	if err != nil {
		return FailedCheck(name, "archive config.json failed", err.Error())
	}

	if identical {
		slog.Default().Info("kb_doctor migrate archived identical json",
			"backup", backupPath)
		return PassedCheck(name, "archived legacy config.json (identical to yaml)")
	}
	slog.Default().Info("kb_doctor migrate archived divergent json",
		"backup", backupPath)
	return PassedCheck(name, "archived legacy config.json (yaml won divergence)")
}

// configsEquivalent compares the JSON and YAML binding-relevant fields. The
// narrow YAML carries only kb_id + repo_id, so those are the only fields
// that have a meaningful "diverged" notion. An empty YAML kb_id when the
// JSON has one is treated as divergent — that's the case where the YAML was
// written incompletely (e.g. an interrupted migration) and the JSON would
// actually be carrying more information.
func configsEquivalent(jsonCfg, yamlCfg config.ProjectConfigYAML) bool {
	if jsonCfg.KBID != yamlCfg.KBID {
		return false
	}
	if jsonCfg.RepoID != yamlCfg.RepoID {
		return false
	}
	return true
}

// readJSONConfigBinding extracts just the kb_id + repo_id from a config.json,
// returned in the same shape as a YAML binding so the equivalence comparison
// is symmetric. ProjectConfig.KBID isn't JSON-serialized (it's a yaml-only
// tag), so we read both kb_id and repo_id by re-decoding the raw JSON.
func readJSONConfigBinding(path string) (config.ProjectConfigYAML, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return config.ProjectConfigYAML{}, err
	}
	var raw struct {
		KBID   string `json:"kb_id,omitempty"`
		RepoID string `json:"repo_id,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return config.ProjectConfigYAML{}, fmt.Errorf("parse json: %w", err)
	}
	return config.ProjectConfigYAML{KBID: raw.KBID, RepoID: raw.RepoID}, nil
}

// readYAMLConfigBinding loads the narrow project-side YAML binding.
func readYAMLConfigBinding(path string) (config.ProjectConfigYAML, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return config.ProjectConfigYAML{}, err
	}
	var y config.ProjectConfigYAML
	if err := yaml.Unmarshal(data, &y); err != nil {
		return config.ProjectConfigYAML{}, fmt.Errorf("parse yaml: %w", err)
	}
	return y, nil
}

// archiveJSON copies the legacy JSON to `.sageox/config.json.bak.<RFC3339>`
// then removes the original. The copy is done via temp + rename so a crash
// mid-archive cannot leave the project with neither file (the original is
// only unlinked after the backup is durably renamed into place).
//
// Returns the final backup path so callers can log it.
func archiveJSON(sageoxDir, jsonPath string) (string, error) {
	// RFC3339 contains ":" which is invalid in Windows filenames; use a
	// filesystem-safe layout that still sorts lexicographically.
	ts := time.Now().UTC().Format("20060102T150405Z")
	finalBackup := filepath.Join(sageoxDir, "config.json.bak."+ts)
	tmpBackup := finalBackup + ".tmp"

	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return "", fmt.Errorf("read json: %w", err)
	}
	if err := os.WriteFile(tmpBackup, data, 0o644); err != nil {
		return "", fmt.Errorf("write backup tmp: %w", err)
	}
	if err := os.Rename(tmpBackup, finalBackup); err != nil {
		_ = os.Remove(tmpBackup)
		return "", fmt.Errorf("rename backup: %w", err)
	}
	if err := os.Remove(jsonPath); err != nil {
		// Backup is durable; surface the unlink failure but don't roll back
		// (the project is now in a "both backup + original" state, which is
		// strictly safer than "neither").
		return finalBackup, fmt.Errorf("remove original json: %w", err)
	}
	return finalBackup, nil
}

// pathExists is a small predicate used to keep the dispatch switch readable.
// Distinguishes ENOENT (legitimately absent) from other stat errors
// (permission/IO) so a misconfigured directory doesn't silently route to the
// wrong migration branch.
func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("stat %s: %w", path, err)
}

// resolveKBIDForRepo asks the kb API for all bubbles the caller can access
// and returns the kb_id of the kb_type=repo row whose repo_id matches. Empty
// string + nil error means "the kb API responded but no matching bubble
// exists" — surfaced to the user via the orphan-repo Warning in migrateJSONOnly.
func resolveKBIDForRepo(ctx context.Context, repoID string) (string, error) {
	listFn := kbHookList()
	bubbles, err := listFn(ctx)
	if err != nil {
		return "", err
	}
	for _, b := range bubbles {
		if b.KBType == api.KBTypeRepo && b.RepoID == repoID {
			return b.KBID, nil
		}
	}
	return "", nil
}

// writeProjectYAMLAtomic marshals the project-side YAML and writes it via a
// temp + rename so a crash mid-write cannot leave a half-written
// config.yaml behind. Permissions match the existing config.json (0644)
// because this file is intended to be committed to git.
func writeProjectYAMLAtomic(sageoxDir, finalPath string, payload any) error {
	data, err := marshalProjectYAML(payload)
	if err != nil {
		return fmt.Errorf("marshal yaml: %w", err)
	}
	tmp := filepath.Join(sageoxDir, "config.yaml.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write temp: %w", err)
	}
	if err := os.Rename(tmp, finalPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename temp: %w", err)
	}
	return nil
}

func marshalProjectYAML(payload any) ([]byte, error) {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	raw := map[string]any{}
	if len(jsonData) > 0 {
		if err := json.Unmarshal(jsonData, &raw); err != nil {
			return nil, err
		}
	}

	switch p := payload.(type) {
	case config.ProjectConfig:
		if p.KBID != "" {
			raw["kb_id"] = p.KBID
		}
	case *config.ProjectConfig:
		if p != nil && p.KBID != "" {
			raw["kb_id"] = p.KBID
		}
	case config.ProjectConfigYAML:
		if p.KBID != "" {
			raw["kb_id"] = p.KBID
		}
		if p.RepoID != "" {
			raw["repo_id"] = p.RepoID
		}
	case *config.ProjectConfigYAML:
		if p != nil {
			if p.KBID != "" {
				raw["kb_id"] = p.KBID
			}
			if p.RepoID != "" {
				raw["repo_id"] = p.RepoID
			}
		}
	}

	return yaml.Marshal(raw)
}
