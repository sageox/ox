// detect.go handles adapter detection and diagnostics.
package main

import (
	"os"
	"os/exec"
	"path/filepath"

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
			// accept either the current or legacy Amp marker — a pre-#527
			// install is still considered "installed" for diagnosis purposes
			if !ampBlockAlreadyPresent(string(data)) {
				issues = append(issues, adapterprotocol.DiagnoseIssue{
					Slug:     "amp:hooks-missing",
					Severity: "warning",
					Title:    "Amp hooks not installed",
					Detail:   "AGENTS.md does not contain ox prime marker.",
					Fix:      "ox-adapter-amp install-hooks --repo-root " + p.RepoRoot + " --scope project",
					FixSafe:  true,
				})
			}
		}
	}

	// User-global ox-bridge plugin must exist for the adapter to read
	// any sessions on current Amp. install-hooks installs it as a
	// side-effect, so the same fix-it command surfaces it. If the home
	// directory itself can't be resolved, skip this check rather than
	// reporting a misleading "missing" issue — install would fail too.
	if bridgePath, err := userBridgePluginPath(); err == nil {
		if _, statErr := os.Stat(bridgePath); os.IsNotExist(statErr) {
			fix := "ox-adapter-amp install-hooks --scope project"
			if p.RepoRoot != "" {
				fix = "ox-adapter-amp install-hooks --repo-root " + p.RepoRoot + " --scope project"
			}
			issues = append(issues, adapterprotocol.DiagnoseIssue{
				Slug:     "amp:bridge-plugin-missing",
				Severity: "warning",
				Title:    "Amp ox-bridge plugin not installed",
				Detail:   "~/.config/amp/plugins/ox-bridge.ts is missing; the adapter has no session transcripts to read until install-hooks runs.",
				Fix:      fix,
				FixSafe:  true,
			})
		}
	}

	return &adapterprotocol.DiagnoseResult{OK: len(issues) == 0, Issues: issues}, nil
}
