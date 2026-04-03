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
	if os.Getenv("AGENT_ENV") == "amp" {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "AGENT_ENV=amp"}, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return &adapterprotocol.DetectResponse{Detected: false, Reason: "cannot determine home directory"}, nil
	}

	ampDir := filepath.Join(home, ".amp")
	if info, err := os.Stat(ampDir); err == nil && info.IsDir() {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "found ~/.amp/"}, nil
	}

	if _, err := exec.LookPath("amp"); err == nil {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "amp binary found in PATH"}, nil
	}

	return &adapterprotocol.DetectResponse{Detected: false, Reason: "~/.amp/ not found and amp not in PATH"}, nil
}

func handleDiagnose(p adapterprotocol.DiagnoseParams) (*adapterprotocol.DiagnoseResult, error) {
	var issues []adapterprotocol.DiagnoseIssue

	home, _ := os.UserHomeDir()
	ampDir := filepath.Join(home, ".amp")
	if _, err := os.Stat(ampDir); os.IsNotExist(err) {
		issues = append(issues, adapterprotocol.DiagnoseIssue{
			Slug:     "amp:not-installed",
			Severity: "warning",
			Title:    "Amp CLI not detected",
			Detail:   "~/.amp directory not found.",
		})
	}

	if p.RepoRoot != "" {
		agentsPath := filepath.Join(p.RepoRoot, "AGENTS.md")
		if data, err := os.ReadFile(agentsPath); err == nil {
			if !strings.Contains(string(data), ampPrimeMarkerStart) {
				issues = append(issues, adapterprotocol.DiagnoseIssue{
					Slug:     "amp:hooks-missing",
					Severity: "warning",
					Title:    "Amp hooks not installed",
					Detail:   "AGENTS.md does not contain ox prime marker.",
					Fix:      "Run: ox-adapter-amp install-hooks --repo-root " + p.RepoRoot + " --scope project",
					FixSafe:  true,
				})
			}
		}
	}

	return &adapterprotocol.DiagnoseResult{OK: len(issues) == 0, Issues: issues}, nil
}
