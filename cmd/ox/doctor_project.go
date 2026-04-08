package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/config"
)

// isSageoxInitialized returns true if .sageox/ directory exists
func isSageoxInitialized(gitRoot string) bool {
	if gitRoot == "" {
		return false
	}
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	info, err := os.Stat(sageoxDir)
	return err == nil && info.IsDir()
}

func checkSageoxDirectory() checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck(".sageox/ directory", "not in git repo", "")
	}
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if _, err := os.Stat(sageoxDir); err == nil {
		return checkResult{
			name:    ".sageox/ directory",
			passed:  true,
			message: "",
		}
	}
	return checkResult{
		name:    ".sageox/ directory",
		passed:  false,
		message: "not found",
		detail:  "Run `ox init` to create it",
	}
}

// checkLegacySageoxMd warns if legacy SAGEOX.md files exist (deprecated in favor of progressive disclosure)
func checkLegacySageoxMd() checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return checkResult{
			name:    "Legacy SAGEOX.md",
			passed:  true,
			message: "none",
		}
	}
	rootPath := filepath.Join(gitRoot, "SAGEOX.md")
	sageoxPath := filepath.Join(gitRoot, ".sageox", "SAGEOX.md")

	_, rootErr := os.Stat(rootPath)
	_, sageoxErr := os.Stat(sageoxPath)

	if rootErr == nil || sageoxErr == nil {
		locations := []string{}
		if rootErr == nil {
			locations = append(locations, "SAGEOX.md")
		}
		if sageoxErr == nil {
			locations = append(locations, ".sageox/SAGEOX.md")
		}
		return checkResult{
			name:    "Legacy SAGEOX.md",
			passed:  true,
			warning: true,
			message: "found: " + strings.Join(locations, ", "),
			detail:  "SAGEOX.md is deprecated; use `ox agent prime` for guidance",
		}
	}
	return checkResult{
		name:    "Legacy SAGEOX.md",
		passed:  true,
		message: "none",
	}
}

// checkConfigFile verifies .sageox/config.json exists and is up to date.
// Uses ensureSageoxConfig() which is shared with ox init for idempotent behavior.
func checkConfigFile(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return checkResult{
			name:    "config.json",
			skipped: true,
			message: "not in git repo",
		}
	}

	// if .sageox/ doesn't exist, skip this check - user needs to run ox init
	if !isSageoxInitialized(gitRoot) {
		return SkippedCheck("config.json", ".sageox/ not initialized", "Run `ox init` first")
	}

	configPath := filepath.Join(gitRoot, ".sageox", "config.json")

	// check if config exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if fix {
			result := ensureSageoxConfig(gitRoot)
			if result == configError {
				return FailedCheck("config.json", "create failed", "")
			}
			return PassedCheck("config.json", "created")
		}
		// .sageox/ exists but config.json is missing - incomplete init
		return FailedCheck("config.json", "not found", "Run `ox doctor --fix` to create it")
	}

	// config exists - check if it needs upgrade
	cfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil {
		return FailedCheck("config.json", "parse error", err.Error())
	}

	if cfg.NeedsUpgrade() {
		if fix {
			result := ensureSageoxConfig(gitRoot)
			if result == configError {
				return FailedCheck("config.json", "upgrade failed", "")
			}
			return PassedCheck("config.json", fmt.Sprintf("upgraded to v%s", config.CurrentConfigVersion))
		}
		return WarningCheck("config.json", fmt.Sprintf("outdated (v%s)", cfg.ConfigVersion),
			fmt.Sprintf("Run `ox doctor --fix` to upgrade to v%s", config.CurrentConfigVersion))
	}

	return PassedCheck("config.json", fmt.Sprintf("v%s", cfg.ConfigVersion))
}

func checkConfigFields(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return checkResult{
			name:    "Fields valid",
			skipped: true,
			message: "not in git repo",
		}
	}

	configPath := filepath.Join(gitRoot, ".sageox", "config.json")

	data, err := os.ReadFile(configPath)
	if err != nil {
		return checkResult{
			name:    "Fields valid",
			passed:  false,
			message: "read failed",
			detail:  err.Error(),
		}
	}

	var rawCfg config.ProjectConfig
	if err := json.Unmarshal(data, &rawCfg); err != nil {
		return checkResult{
			name:    "Fields valid",
			passed:  false,
			message: "parse failed",
			detail:  err.Error(),
		}
	}

	validationErrors := config.ValidateProjectConfig(&rawCfg)

	if len(validationErrors) > 0 {
		if fix {
			cfg, err := config.LoadProjectConfig(gitRoot)
			if err != nil {
				return checkResult{
					name:    "Fields valid",
					passed:  false,
					message: "load failed",
					detail:  err.Error(),
				}
			}

			if err := config.SaveProjectConfig(gitRoot, cfg); err != nil {
				return checkResult{
					name:    "Fields valid",
					passed:  false,
					message: "fix failed",
					detail:  err.Error(),
				}
			}

			return checkResult{
				name:    "Fields valid",
				passed:  true,
				message: "fixed",
			}
		}

		return checkResult{
			name:    "Fields valid",
			passed:  false,
			message: strings.Join(validationErrors, "; "),
			detail:  "Run `ox doctor --fix`",
		}
	}

	return checkResult{
		name:    "Fields valid",
		passed:  true,
		message: "",
	}
}

// checkScoreThresholdRange validates that attribution.score_threshold is in [0.0, 1.0].
func checkScoreThresholdRange() checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Score threshold range", "not in git repo", "")
	}

	cfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil {
		return SkippedCheck("Score threshold range", "config not loaded", "")
	}

	resolved := resolveProjectAttribution(cfg)
	if resolved.ScoreThreshold < 0 || resolved.ScoreThreshold > 1 {
		return FailedCheck("Score threshold range",
			fmt.Sprintf("attribution.score_threshold=%g is out of range [0.0, 1.0]", resolved.ScoreThreshold),
			"Run `ox config set attribution.score_threshold 0.5`")
	}

	return PassedCheck("Score threshold range", fmt.Sprintf("%g", resolved.ScoreThreshold))
}
