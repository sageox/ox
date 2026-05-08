// detect.go handles adapter detection and diagnostics.
package main

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

func handleDetect() (*adapterprotocol.DetectResponse, error) {
	if os.Getenv("AGENT_ENV") == "pi" {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "AGENT_ENV=pi"}, nil
	}

	// pi-mono's CLI entry sets process.env.PI_CODING_AGENT = "true"
	// (packages/coding-agent/src/cli.ts) and Node child_process inherits
	// process.env, so this propagates to every subprocess pi spawns —
	// including the bash tool. Strongest available signal that we're
	// running inside pi right now.
	if v := os.Getenv("PI_CODING_AGENT"); v == "true" || v == "1" {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "PI_CODING_AGENT=" + v}, nil
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
			// accept either the current or legacy Pi marker — a pre-#527
			// install is still considered "installed" for diagnosis purposes
			if !piBlockAlreadyPresent(string(data)) {
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
