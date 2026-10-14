// detect.go handles adapter detection and diagnostics.
package main

import (
	"fmt"
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
					Fix:      installFix,
					FixArgv:  installFixArgv,
					FixSafe:  true,
				})
			}
		}
	}

	issues = append(issues, diagnoseBridgePlugin(p.RepoRoot)...)

	return &adapterprotocol.DiagnoseResult{OK: len(issues) == 0, Issues: issues}, nil
}

// installFix and installFixArgv repair every Amp issue diagnose reports:
// `ox integrate install --amp` runs install-hooks, which writes the AGENTS.md
// marker and the user-global bridge plugin. argv[0] is "ox" because ox doctor
// auto-runs only git and ox commands; an ox-adapter-amp argv would leave a
// FixSafe repair that never runs.
const installFix = "ox integrate install --amp"

var installFixArgv = []string{"ox", "integrate", "install", "--amp"}

// diagnoseBridgePlugin reports when Amp has no ox-bridge plugin to load, so no
// session is recorded. That happens two ways: it was never installed, or ox
// installed it at ~/.config/amp/plugins/ before honoring XDG_CONFIG_HOME and
// Amp reads $XDG_CONFIG_HOME/amp/plugins/ instead. The second gets its own slug
// so the coworker learns why an install that looked fine records nothing.
//
// An unresolvable home directory or an unreadable path reports nothing rather
// than a misleading "missing"; install would fail the same way.
func diagnoseBridgePlugin(repoRoot string) []adapterprotocol.DiagnoseIssue {
	bridgePath, err := userBridgePluginPath()
	if err != nil {
		return nil
	}
	if _, statErr := os.Stat(bridgePath); !os.IsNotExist(statErr) {
		return nil
	}

	// install-hooks needs a repository for its AGENTS.md half, and so does
	// `ox integrate install`; outside one the fix can only be shown.
	var fixArgv []string
	if repoRoot != "" {
		fixArgv = installFixArgv
	}

	if homePath, err := homeBridgePluginPath(); err == nil && homePath != bridgePath {
		if _, statErr := os.Stat(homePath); statErr == nil {
			return []adapterprotocol.DiagnoseIssue{{
				Slug:     "amp:bridge-plugin-misplaced",
				Severity: "warning",
				Title:    "Amp ox-bridge plugin is installed where Amp does not load it",
				Detail: fmt.Sprintf("%s exists, but XDG_CONFIG_HOME is set, so Amp loads system plugins from %s. "+
					"No Amp sessions are recorded until the plugin is installed there. "+
					"The old copy is harmless and can stay.",
					homePath, filepath.Dir(bridgePath)),
				Fix:     installFix,
				FixArgv: fixArgv,
				FixSafe: true,
			}}
		}
	}

	return []adapterprotocol.DiagnoseIssue{{
		Slug:     "amp:bridge-plugin-missing",
		Severity: "warning",
		Title:    "Amp ox-bridge plugin not installed",
		Detail:   bridgePath + " is missing; the adapter has no session transcripts to read until install-hooks runs.",
		Fix:      installFix,
		FixArgv:  fixArgv,
		FixSafe:  true,
	}}
}
