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
	if os.Getenv("AGENT_ENV") == "aider" {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "AGENT_ENV=aider"}, nil
	}

	// check for aider chat history in current directory
	cwd, err := os.Getwd()
	if err == nil {
		historyFile := filepath.Join(cwd, ".aider.chat.history.md")
		if _, err := os.Stat(historyFile); err == nil {
			return &adapterprotocol.DetectResponse{Detected: true, Reason: "found .aider.chat.history.md"}, nil
		}
	}

	if _, err := exec.LookPath("aider"); err == nil {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "aider binary found in PATH"}, nil
	}

	return &adapterprotocol.DetectResponse{Detected: false, Reason: ".aider.chat.history.md not found and aider not in PATH"}, nil
}

func handleDiagnose(p adapterprotocol.DiagnoseParams) (*adapterprotocol.DiagnoseResult, error) {
	var issues []adapterprotocol.DiagnoseIssue

	if _, err := exec.LookPath("aider"); err != nil {
		issues = append(issues, adapterprotocol.DiagnoseIssue{
			Slug:     "aider:not-installed",
			Severity: "warning",
			Title:    "Aider not detected",
			Detail:   "aider binary not found in PATH.",
		})
	}

	if p.RepoRoot != "" {
		convPath := filepath.Join(p.RepoRoot, "CONVENTIONS.md")
		if data, err := os.ReadFile(convPath); err == nil {
			if !strings.Contains(string(data), aiderPrimeMarkerStart) {
				issues = append(issues, adapterprotocol.DiagnoseIssue{
					Slug:     "aider:hooks-missing",
					Severity: "warning",
					Title:    "Aider hooks not installed",
					Detail:   "CONVENTIONS.md does not contain ox prime marker.",
					Fix:      "Run: ox-adapter-aider install-hooks --repo-root " + p.RepoRoot + " --scope project",
					FixSafe:  true,
				})
			}
		}
	}

	return &adapterprotocol.DiagnoseResult{OK: len(issues) == 0, Issues: issues}, nil
}
