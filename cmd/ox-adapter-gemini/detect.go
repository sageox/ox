// detect.go handles adapter detection and diagnostics.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

func handleDetect() (*adapterprotocol.DetectResponse, error) {
	if os.Getenv("AGENT_ENV") == "gemini" {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "AGENT_ENV=gemini"}, nil
	}
	if os.Getenv("GEMINI_API_KEY") != "" || os.Getenv("GOOGLE_API_KEY") != "" {
		return &adapterprotocol.DetectResponse{Detected: true, Reason: "Gemini API key found"}, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return &adapterprotocol.DetectResponse{Detected: false, Reason: "cannot determine home directory"}, nil
	}

	tmpDir := filepath.Join(home, ".gemini", "tmp")
	if _, err := os.Stat(tmpDir); os.IsNotExist(err) {
		return &adapterprotocol.DetectResponse{Detected: false, Reason: "~/.gemini/tmp not found"}, nil
	}

	entries, err := os.ReadDir(tmpDir)
	if err != nil || len(entries) == 0 {
		return &adapterprotocol.DetectResponse{Detected: false, Reason: "~/.gemini/tmp is empty"}, nil
	}

	return &adapterprotocol.DetectResponse{Detected: true, Reason: "found ~/.gemini/tmp with session data"}, nil
}

func handleDiagnose(p adapterprotocol.DiagnoseParams) (*adapterprotocol.DiagnoseResult, error) {
	var issues []adapterprotocol.DiagnoseIssue

	home, _ := os.UserHomeDir()
	geminiDir := filepath.Join(home, ".gemini")
	installed := true
	if _, err := os.Stat(geminiDir); os.IsNotExist(err) {
		installed = false
		issues = append(issues, adapterprotocol.DiagnoseIssue{
			Slug:     "gemini:not-installed",
			Severity: "warning",
			Title:    "Gemini CLI not detected",
			Detail:   "~/.gemini directory not found.",
		})
	}

	if installed {
		issues = append(issues, diagnoseReader(filepath.Join(geminiDir, "tmp"))...)
	}

	if p.RepoRoot != "" {
		issues = append(issues, diagnoseHooks(p.RepoRoot)...)
	}

	return &adapterprotocol.DiagnoseResult{OK: len(issues) == 0, Issues: issues}, nil
}

// diagnoseReader parses the newest real gemini transcript on this machine.
//
// This check exists because the previous parser expected a format gemini never
// wrote and returned zero entries with no error against every real session —
// and diagnose reported ok:true the whole time, because it only looked at
// whether files and hooks existed. A reader that cannot read is now the loudest
// thing this adapter reports.
func diagnoseReader(tmpDir string) []adapterprotocol.DiagnoseIssue {
	files, err := listGeminiSessionFiles(tmpDir)
	if err != nil || len(files) == 0 {
		// nothing recorded yet is not a fault; it is just no evidence
		return nil
	}

	sort.Slice(files, func(i, j int) bool { return files[i].modTime.After(files[j].modTime) })
	newest := files[0].path

	data, err := os.ReadFile(newest)
	if err != nil {
		return []adapterprotocol.DiagnoseIssue{{
			Slug:     "gemini:session-unreadable",
			Severity: "warning",
			Title:    "Cannot read the most recent Gemini session",
			Detail:   fmt.Sprintf("%s: %v", newest, err),
		}}
	}

	entries, _, err := parseGeminiSession(data)
	if err != nil {
		return []adapterprotocol.DiagnoseIssue{{
			Slug:     "gemini:session-parse-failed",
			Severity: "error",
			Title:    "Gemini session parser does not match the installed CLI's format",
			Detail: fmt.Sprintf(
				"Parsing the most recent transcript (%s) failed: %v. Session recording for Gemini CLI will capture nothing until the parser is updated.",
				newest, err),
		}}
	}

	if len(entries) == 0 {
		return []adapterprotocol.DiagnoseIssue{{
			Slug:     "gemini:session-empty",
			Severity: "error",
			Title:    "Gemini session parser extracted no entries",
			Detail: fmt.Sprintf(
				"The most recent transcript (%s) parsed cleanly but produced zero entries. Session recording for Gemini CLI is silently capturing nothing.",
				newest),
		}}
	}

	return nil
}

// diagnoseHooks reports both a missing install and a settings file in a shape
// that stops the gemini CLI from starting.
func diagnoseHooks(repoRoot string) []adapterprotocol.DiagnoseIssue {
	var issues []adapterprotocol.DiagnoseIssue

	settingsPath, err := resolveSettingsPath(repoRoot, "project")
	if err != nil {
		return nil
	}

	fixCmd := "ox-adapter-gemini install-hooks --repo-root " + repoRoot + " --scope project"

	// gemini validates settings before it does anything else: a non-array
	// value under hooks.<Event> makes the CLI refuse to start
	if settings, err := loadSettings(settingsPath); err == nil {
		if hooks, ok := settings["hooks"].(map[string]any); ok {
			var bad []string
			for event, v := range hooks {
				if _, isArray := v.([]any); !isArray {
					bad = append(bad, event)
				}
			}
			sort.Strings(bad)
			for _, event := range bad {
				issues = append(issues, adapterprotocol.DiagnoseIssue{
					Slug:     "gemini:hooks-invalid-shape",
					Severity: "error",
					Title:    "Gemini CLI cannot start: invalid hooks configuration",
					Detail: fmt.Sprintf(
						"%s sets hooks.%s to a non-array value. Gemini rejects the whole settings file with "+
							"\"Expected array, received string\" and exits before starting. Gemini's schema requires "+
							"hooks.<Event> to be an array of hook definitions.",
						settingsPath, event),
					Fix:     fixCmd,
					FixArgv: []string{"ox-adapter-gemini", "install-hooks", "--repo-root", repoRoot, "--scope", "project"},
					FixSafe: true,
				})
			}
		}
	}

	check, err := handleCheckHooks(adapterprotocol.HookParams{RepoRoot: repoRoot, Scope: "project"})
	if err == nil && !check.Installed {
		issues = append(issues, adapterprotocol.DiagnoseIssue{
			Slug:     "gemini:hooks-missing",
			Severity: "warning",
			Title:    "ox hooks not installed for Gemini CLI",
			Detail: "Gemini CLI hooks are not configured for this project (expected " +
				"SessionStart, BeforeAgent, AfterTool and SessionEnd). Session recording is disabled.",
			Fix:     fixCmd,
			FixArgv: []string{"ox-adapter-gemini", "install-hooks", "--repo-root", repoRoot, "--scope", "project"},
			FixSafe: true,
		})
	}

	return issues
}
