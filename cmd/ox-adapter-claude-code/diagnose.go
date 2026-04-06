package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

func handleDiagnose(p adapterprotocol.DiagnoseParams) (*adapterprotocol.DiagnoseResult, error) {
	var issues []adapterprotocol.DiagnoseIssue

	// check if Claude Code is installed
	home, _ := os.UserHomeDir()
	claudeDir := filepath.Join(home, ".claude")
	if _, err := os.Stat(claudeDir); os.IsNotExist(err) {
		issues = append(issues, adapterprotocol.DiagnoseIssue{
			Slug:     "claude-code:not-installed",
			Severity: "warning",
			Title:    "Claude Code not detected",
			Detail:   "~/.claude directory not found. Claude Code may not be installed.",
		})
	}

	// check hooks
	if p.RepoRoot != "" {
		settingsPath := filepath.Join(p.RepoRoot, ".claude", "settings.json")
		hooksInstalled := false

		if data, err := os.ReadFile(settingsPath); err == nil {
			// quick check for ox hook presence
			if strings.Contains(string(data), "ox agent hook") {
				hooksInstalled = true
			}
		}

		if !hooksInstalled {
			issues = append(issues, adapterprotocol.DiagnoseIssue{
				Slug:     "claude-code:hooks-missing",
				Severity: "warning",
				Title:    "ox hooks not installed for Claude Code",
				Detail:   "Claude Code hooks are not configured for this project. Session recording is disabled.",
				Fix:      "ox integrate install",
				FixSafe:  true,
			})
		}

		// check rules
		checkResp, err := handleCheckRules(adapterprotocol.RulesParams{
			RepoRoot: p.RepoRoot,
			Version:  p.Version,
		})
		if err == nil && !checkResp.Installed {
			if len(checkResp.Missing) > 0 {
				issues = append(issues, adapterprotocol.DiagnoseIssue{
					Slug:     "claude-code:rules-missing",
					Severity: "warning",
					Title:    "ox rules not installed for Claude Code",
					Detail:   "Missing rule files: " + strings.Join(checkResp.Missing, ", "),
					Fix:      "ox integrate install",
					FixSafe:  true,
				})
			}
			if len(checkResp.Stale) > 0 {
				issues = append(issues, adapterprotocol.DiagnoseIssue{
					Slug:     "claude-code:rules-stale",
					Severity: "info",
					Title:    "ox rules are outdated for Claude Code",
					Detail:   "Stale rule files: " + strings.Join(checkResp.Stale, ", "),
					Fix:      "ox integrate install",
					FixSafe:  true,
				})
			}
		}
	}

	return &adapterprotocol.DiagnoseResult{
		OK:     len(issues) == 0,
		Issues: issues,
	}, nil
}
