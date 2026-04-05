package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

func handleDiagnose(p adapterprotocol.DiagnoseParams) (*adapterprotocol.DiagnoseResult, error) {
	var issues []adapterprotocol.DiagnoseIssue

	home, _ := os.UserHomeDir()
	factoryDir := filepath.Join(home, ".factory")
	if _, err := os.Stat(factoryDir); os.IsNotExist(err) {
		issues = append(issues, adapterprotocol.DiagnoseIssue{
			Slug:     "droid:not-installed",
			Severity: "warning",
			Title:    "Factory Droid not detected",
			Detail:   "~/.factory directory not found. Factory Droid may not be installed.",
		})
	}

	if p.RepoRoot != "" {
		settingsPath := filepath.Join(p.RepoRoot, ".factory", "settings.json")
		hooksInstalled := false

		// check for ox hook marker in the structured hooks format
		if data, err := os.ReadFile(settingsPath); err == nil {
			if strings.Contains(string(data), oxHookMarker) {
				hooksInstalled = true
			}
		}

		if !hooksInstalled {
			issues = append(issues, adapterprotocol.DiagnoseIssue{
				Slug:     "droid:hooks-missing",
				Severity: "warning",
				Title:    "ox hooks not installed for Factory Droid",
				Detail:   "Factory Droid hooks are not configured for this project. Session recording is disabled.",
				Fix:      "ox integrate install --droid",
				FixSafe:  true,
			})
		}
	}

	return &adapterprotocol.DiagnoseResult{
		OK:     len(issues) == 0,
		Issues: issues,
	}, nil
}
