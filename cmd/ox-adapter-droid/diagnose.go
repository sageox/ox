package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

func handleDiagnose(p adapterprotocol.DiagnoseParams) (*adapterprotocol.DiagnoseResult, error) {
	var issues []adapterprotocol.DiagnoseIssue

	home, _ := os.UserHomeDir()
	factoryDir := filepath.Join(home, ".factory")
	if _, err := os.Stat(factoryDir); os.IsNotExist(err) {
		issues = append(issues, adapterprotocol.DiagnoseIssue{
			Slug:     "droid:not-installed",
			Severity: "warning",
			Title:    "Factory Droid not detected",
			Detail:   "~/.factory directory not found. Factory Droid may not be installed.",
		})
	} else if detail := checkSessionStore(); detail != "" {
		// the droid binary is on PATH but the session store this adapter reads
		// is missing — either the user has never run droid, or Factory moved
		// the store again (it moved at least once: projects/ -> sessions/).
		// Either way, silent zero-entry recording looks identical to an idle
		// session, so this must be loud.
		issues = append(issues, adapterprotocol.DiagnoseIssue{
			Slug:     "droid:store-missing",
			Severity: "error",
			Title:    "Factory Droid session store not found",
			Detail:   detail,
		})
	}

	if p.RepoRoot != "" {
		settingsPath := filepath.Join(p.RepoRoot, ".factory", "settings.json")
		hooksInstalled := false

		// check for ox hook marker in the structured hooks format
		if data, err := os.ReadFile(settingsPath); err == nil {
			if strings.Contains(string(data), oxHookMarker) {
				hooksInstalled = true
			}
		}

		if !hooksInstalled {
			issues = append(issues, adapterprotocol.DiagnoseIssue{
				Slug:     "droid:hooks-missing",
				Severity: "warning",
				Title:    "ox hooks not installed for Factory Droid",
				Detail:   "Factory Droid hooks are not configured for this project. Session recording is disabled.",
				Fix:      "ox-adapter-droid install-hooks --repo-root " + p.RepoRoot + " --scope project",
				FixSafe:  true,
			})
		}

		// check rules
		checkResp, err := handleCheckRules(adapterprotocol.RulesParams{
			RepoRoot: p.RepoRoot,
			Version:  p.Version,
		})
		if err == nil && !checkResp.Installed {
			if len(checkResp.Missing) > 0 {
				issues = append(issues, adapterprotocol.DiagnoseIssue{
					Slug:     "droid:rules-missing",
					Severity: "warning",
					Title:    "ox rules not installed for Factory Droid",
					Detail:   "Missing rule files: " + strings.Join(checkResp.Missing, ", "),
					Fix:      "ox integrate install",
					FixSafe:  true,
				})
			}
			if len(checkResp.Stale) > 0 {
				issues = append(issues, adapterprotocol.DiagnoseIssue{
					Slug:     "droid:rules-stale",
					Severity: "info",
					Title:    "ox rules are outdated for Factory Droid",
					Detail:   "Stale rule files: " + strings.Join(checkResp.Stale, ", "),
					Fix:      "ox integrate install",
					FixSafe:  true,
				})
			}
		}
	}

	return &adapterprotocol.DiagnoseResult{
		OK:     len(issues) == 0,
		Issues: issues,
	}, nil
}

// checkSessionStore reports a detail message when ~/.factory exists (droid
// has been used before) but the session store this adapter reads from is
// missing while the droid binary is still on PATH. This is exactly the
// failure mode that shipped broken once already: the store moved from
// ~/.factory/projects/ to ~/.factory/sessions/ and every discovery path
// silently produced nothing. If Factory relocates it again, this makes the
// next relocation loud instead of a silent zero-entry recording that looks
// identical to an idle session.
func checkSessionStore() string {
	sessionsDir, err := droidSessionsDir()
	if err != nil {
		return ""
	}
	if _, err := os.Stat(sessionsDir); err == nil {
		return ""
	}
	if _, err := exec.LookPath("droid"); err != nil {
		return ""
	}
	return "droid is installed but ~/.factory/sessions/ does not exist. " +
		"Factory may have relocated the session store; session recording " +
		"will silently produce nothing until this adapter is updated."
}
