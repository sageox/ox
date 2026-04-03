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
	}

	return &adapterprotocol.DiagnoseResult{
		OK:     len(issues) == 0,
		Issues: issues,
	}, nil
}
