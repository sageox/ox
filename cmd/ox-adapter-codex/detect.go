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
	for _, scope := range []string{"project", "user"} {
		if scope == "project" && p.RepoRoot == "" {
			continue
		}
		hooksPath := resolveHooksPath(p.RepoRoot, scope)
		hooks, _, err := readHooksFile(hooksPath)
		if err != nil {
			slug := "codex:hooks-invalid"
			if scope == "user" {
				slug += "-user"
			}
			issues = append(issues, adapterprotocol.DiagnoseIssue{
				Slug:     slug,
				Severity: "warning",
				Title:    "Codex hook file could not be parsed (" + scope + ")",
				Detail:   fmt.Sprintf("Repair %s before refreshing SageOx hooks: %v", hooksPath, err),
			})
			continue
		}
		// A CLI on PATH is not consent to create project configuration.
		if scope == "project" && projectUsesCodex(p.RepoRoot) {
			hooksOK := true
			for _, event := range codexHookEvents {
				if !eventHasOxHook(hooks[event]) {
					hooksOK = false
					break
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
		if !hookLabelsOutdated(hooks) {
			continue
		}
		slug := "codex:hook-labels-outdated"
		fix := "ox integrate install --codex"
		argv := []string{"ox", "integrate", "install", "--codex"}
		if scope == "user" {
			slug += "-user"
			fix += " --user"
			argv = append(argv, "--user")
		}
		issues = append(issues, adapterprotocol.DiagnoseIssue{
			Slug:     slug,
			Severity: "warning",
			Title:    "SageOx hook labels need updating (" + scope + ")",
			Detail:   "Refresh the labels so Codex explains each SageOx hook. Updated definitions require review and trust in Codex again.",
			Fix:      fix,
			FixArgv:  argv,
			FixSafe:  true,
		})
	}
	if p.RepoRoot != "" {
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
