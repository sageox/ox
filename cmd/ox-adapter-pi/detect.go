// detect.go handles adapter detection and diagnostics.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

func handleDetect() (*adapterprotocol.DetectResponse, error) {
	if os.Getenv("AGENT_ENV") == "pi" {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "AGENT_ENV=pi"}, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return &adapterprotocol.DetectResponse{Detected: false, Reason: "cannot determine home directory"}, nil
	}

	piDir := filepath.Join(home, ".pi")
	if info, err := os.Stat(piDir); err == nil && info.IsDir() {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "found ~/.pi/"}, nil
	}

	if _, err := exec.LookPath("pi"); err == nil {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "pi binary found in PATH"}, nil
	}

	return &adapterprotocol.DetectResponse{Detected: false, Reason: "~/.pi/ not found and pi not in PATH"}, nil
}

func handleDiagnose(p adapterprotocol.DiagnoseParams) (*adapterprotocol.DiagnoseResult, error) {
	var issues []adapterprotocol.DiagnoseIssue

	home, _ := os.UserHomeDir()
	piDir := filepath.Join(home, ".pi")
	if _, err := os.Stat(piDir); os.IsNotExist(err) {
		issues = append(issues, adapterprotocol.DiagnoseIssue{
			Slug:     "pi:not-installed",
			Severity: "warning",
			Title:    "Pi coding agent not detected",
			Detail:   "~/.pi directory not found.",
		})
	}

	if p.RepoRoot != "" {
		agentsPath := filepath.Join(p.RepoRoot, "AGENTS.md")
		if data, err := os.ReadFile(agentsPath); err == nil {
			if !strings.Contains(string(data), piPrimeMarkerStart) {
				issues = append(issues, adapterprotocol.DiagnoseIssue{
					Slug:     "pi:hooks-missing",
					Severity: "warning",
					Title:    "Pi hooks not installed",
					Detail:   "AGENTS.md does not contain ox prime marker.",
					Fix:      "ox-adapter-pi install-hooks --repo-root " + p.RepoRoot + " --scope project",
					FixSafe:  true,
				})
			}
		}
	}

	return &adapterprotocol.DiagnoseResult{OK: len(issues) == 0, Issues: issues}, nil
}
