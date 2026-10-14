// detect.go handles adapter detection and diagnostics.
package main

import (
	"errors"
	"fmt"
	"io/fs"
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

	// ox doctor drops every finding from an adapter that reports
	// not-installed, so this must agree with detect: an Amp found only on
	// PATH has no ~/.amp/ and still needs its plugin checked.
	if detected, _ := handleDetect(); !detected.Detected {
		issues = append(issues, adapterprotocol.DiagnoseIssue{
			Slug:     "amp:not-installed",
			Severity: "warning",
			Title:    "Amp CLI not detected",
			Detail:   detected.Reason + ".",
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
// A path Amp cannot load from (a file where Amp expects the plugins
// directory, a directory where the plugin file belongs, a permission error) is
// reported as such, never as healthy and never as "missing": install-hooks
// fails on it the same way, so there is no automatic repair to offer. An unresolvable home directory reports nothing;
// with no path to name, there is nothing to tell the coworker to fix.
func diagnoseBridgePlugin(repoRoot string) []adapterprotocol.DiagnoseIssue {
	bridgePath, err := userBridgePluginPath()
	if err != nil {
		return nil
	}
	info, statErr := os.Stat(bridgePath)
	if statErr == nil && info.IsDir() {
		statErr = errors.New("it is a directory, not the plugin file")
	}
	if statErr == nil {
		return nil
	}
	// checked before trusting a "not found": on Windows a file in the way
	// reports exactly that
	if blocker := fileBlockingPluginDir(bridgePath); blocker != "" {
		statErr = fmt.Errorf("%s is a file, so the plugin directory cannot exist", blocker)
	}
	if !errors.Is(statErr, fs.ErrNotExist) {
		return []adapterprotocol.DiagnoseIssue{{
			Slug:     "amp:bridge-plugin-unreadable",
			Severity: "warning",
			Title:    "Amp cannot read the ox-bridge plugin",
			Detail: fmt.Sprintf("Cannot use %s: %v. Amp loads system plugins from %s; "+
				"clear that path by hand, then run `%s`. Until then, Amp sessions are not recorded.",
				bridgePath, statErr, filepath.Dir(bridgePath), installFix),
		}}
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

// fileBlockingPluginDir returns the nearest existing ancestor of path when it
// is a file rather than a directory: install-hooks cannot create the plugin
// directory through it, and Amp loads nothing below it. It returns "" when the
// nearest existing ancestor is a directory.
//
// The walk is needed because stat's own answer differs by platform: through a
// file component, unix reports ENOTDIR but Windows reports "not found", which
// would otherwise read as a plugin that is merely missing.
func fileBlockingPluginDir(path string) string {
	for dir := filepath.Dir(path); ; {
		if info, err := os.Stat(dir); err == nil {
			if info.IsDir() {
				return ""
			}
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
