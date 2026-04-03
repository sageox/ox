// detect.go — detection and diagnostics for codex adapter.
package main

import (
	"os"
	"path/filepath"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

func handleDetect() (*adapterprotocol.DetectResponse, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return &adapterprotocol.DetectResponse{Detected: false, Reason: "cannot determine home directory"}, nil
	}

	sessionsDir := filepath.Join(home, ".codex", "sessions")
	if info, err := os.Stat(sessionsDir); err == nil && info.IsDir() {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "found ~/.codex/sessions/"}, nil
	}

	return &adapterprotocol.DetectResponse{Detected: false, Reason: "~/.codex/sessions/ not found"}, nil
}

func handleDiagnose(p adapterprotocol.DiagnoseParams) (*adapterprotocol.DiagnoseResult, error) {
	var issues []adapterprotocol.DiagnoseIssue

	home, _ := os.UserHomeDir()
	codexDir := filepath.Join(home, ".codex")
	if _, err := os.Stat(codexDir); os.IsNotExist(err) {
		issues = append(issues, adapterprotocol.DiagnoseIssue{
			Slug:     "codex:not-installed",
			Severity: "warning",
			Title:    "Codex CLI not detected",
			Detail:   "~/.codex directory not found.",
		})
	}

	if p.RepoRoot != "" {
		hooksPath := filepath.Join(p.RepoRoot, codexProjectPath, codexHooksFileName)
		hooksMap, _, err := readHooksFile(hooksPath)
		hooksOK := err == nil
		if hooksOK {
			for _, event := range codexHookEvents {
				if !eventHasOxHook(hooksMap[event]) {
					hooksOK = false
					break
				}
			}
		}
		if !hooksOK {
			issues = append(issues, adapterprotocol.DiagnoseIssue{
				Slug:     "codex:hooks-missing",
				Severity: "warning",
				Title:    "ox hooks not installed for Codex CLI",
				Detail:   "Codex CLI hooks are not configured for this project.",
				Fix:      "Run: ox-adapter-codex install-hooks --repo-root " + p.RepoRoot + " --scope project",
				FixSafe:  true,
			})
		}
	}

	return &adapterprotocol.DiagnoseResult{OK: len(issues) == 0, Issues: issues}, nil
}
