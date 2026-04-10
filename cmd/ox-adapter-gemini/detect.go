// detect.go handles adapter detection and diagnostics.
package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

func handleDetect() (*adapterprotocol.DetectResponse, error) {
	if os.Getenv("AGENT_ENV") == "gemini" {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "AGENT_ENV=gemini"}, nil
	}
	if os.Getenv("GEMINI_API_KEY") != "" || os.Getenv("GOOGLE_API_KEY") != "" {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "Gemini API key found"}, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return &adapterprotocol.DetectResponse{Detected: false, Reason: "cannot determine home directory"}, nil
	}

	tmpDir := filepath.Join(home, ".gemini", "tmp")
	if _, err := os.Stat(tmpDir); os.IsNotExist(err) {
		return &adapterprotocol.DetectResponse{Detected: false, Reason: "~/.gemini/tmp not found"}, nil
	}

	entries, err := os.ReadDir(tmpDir)
	if err != nil || len(entries) == 0 {
		return &adapterprotocol.DetectResponse{Detected: false, Reason: "~/.gemini/tmp is empty"}, nil
	}

	return &adapterprotocol.DetectResponse{Detected: true, Reason: "found ~/.gemini/tmp with session data"}, nil
}

func handleDiagnose(p adapterprotocol.DiagnoseParams) (*adapterprotocol.DiagnoseResult, error) {
	var issues []adapterprotocol.DiagnoseIssue

	home, _ := os.UserHomeDir()
	geminiDir := filepath.Join(home, ".gemini")
	if _, err := os.Stat(geminiDir); os.IsNotExist(err) {
		issues = append(issues, adapterprotocol.DiagnoseIssue{
			Slug:     "gemini:not-installed",
			Severity: "warning",
			Title:    "Gemini CLI not detected",
			Detail:   "~/.gemini directory not found.",
		})
	}

	// check hooks installation
	if p.RepoRoot != "" {
		settingsPath, _ := resolveSettingsPath(p.RepoRoot, "project")
		hooksInstalled := false

		if data, err := os.ReadFile(settingsPath); err == nil {
			if strings.Contains(string(data), "ox agent hook") {
				hooksInstalled = true
			}
		}

		if !hooksInstalled {
			issues = append(issues, adapterprotocol.DiagnoseIssue{
				Slug:     "gemini:hooks-missing",
				Severity: "warning",
				Title:    "ox hooks not installed for Gemini CLI",
				Detail:   "Gemini CLI hooks are not configured for this project. Session recording is disabled.",
				Fix:      "ox-adapter-gemini install-hooks --repo-root " + p.RepoRoot + " --scope project",
				FixSafe:  true,
			})
		}
	}

	return &adapterprotocol.DiagnoseResult{OK: len(issues) == 0, Issues: issues}, nil
}
