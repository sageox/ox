// detect.go — detection and diagnostics for codex adapter.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

func handleDetect() (*adapterprotocol.DetectResponse, error) {
	if _, err := exec.LookPath("codex"); err == nil {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "found codex in PATH"}, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return &adapterprotocol.DetectResponse{Detected: false, Reason: "cannot determine home directory"}, nil
	}

	sessionsDir := filepath.Join(home, ".codex", "sessions")
	if info, err := os.Stat(sessionsDir); err == nil && info.IsDir() {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "found ~/.codex/sessions/"}, nil
	}

	return &adapterprotocol.DetectResponse{Detected: false, Reason: "codex executable and ~/.codex/sessions/ not found"}, nil
}

// projectUsesCodex reports whether a repo has opted into Codex, mirroring
// CodexAgent.DetectProject() on the ox side: a `.codex/` directory in the repo
// root. `ox init` creates it when the user configures Codex.
func projectUsesCodex(repoRoot string) bool {
	info, err := os.Stat(filepath.Join(repoRoot, codexProjectPath))
	return err == nil && info.IsDir()
}

func handleDiagnose(p adapterprotocol.DiagnoseParams) (*adapterprotocol.DiagnoseResult, error) {
	detected, err := handleDetect()
	if err != nil {
		return nil, err
	}
	if !detected.Detected {
		return &adapterprotocol.DiagnoseResult{Issues: []adapterprotocol.DiagnoseIssue{{
			Slug:     "codex:not-installed",
			Severity: "warning",
			Title:    "Codex CLI not detected",
			Detail:   detected.Reason,
		}}}, nil
	}

	var issues []adapterprotocol.DiagnoseIssue
	if p.RepoRoot != "" {
		// Only report missing hooks for a project that actually uses Codex.
		// Detection is deliberately broad (a `codex` on PATH is enough) so
		// `ox init` can offer to set Codex up, but the CLI merely being
		// installed is not consent to create project config: without this
		// gate every repo on the machine warns, and `ox doctor --fix` writes
		// .codex/ into repos that never touch Codex. ox doctor's core check
		// draws the same line — see checkAgentHooks, which returns a silent
		// skip for "CLI detected, no project config".
		if projectUsesCodex(p.RepoRoot) {
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
					Fix:      "ox integrate install --codex",
					FixArgv:  []string{"ox", "integrate", "install", "--codex"},
					FixSafe:  true,
				})
			}
		}

		legacyFeatureFlag, err := hasLegacyFeatureFlag(p.RepoRoot, p.Scope)
		if err != nil {
			issues = append(issues, adapterprotocol.DiagnoseIssue{
				Slug:     "codex:legacy-hooks-feature-unreadable",
				Severity: "warning",
				Title:    "Codex legacy hook feature could not be checked",
				Detail:   fmt.Sprintf("failed to read Codex config: %v", err),
			})
		} else if legacyFeatureFlag {
			issues = append(issues, adapterprotocol.DiagnoseIssue{
				Slug:     "codex:legacy-hooks-feature",
				Severity: "warning",
				Title:    "Codex deprecated hook feature is configured",
				Detail:   "features.codex_hooks is deprecated; Codex hooks are stable and enabled by default.",
				Fix:      "ox integrate install --codex",
				FixArgv:  []string{"ox", "integrate", "install", "--codex"},
				FixSafe:  true,
			})
		}
	}

	return &adapterprotocol.DiagnoseResult{OK: len(issues) == 0, Issues: issues}, nil
}
