package main

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/session/cursorpaths"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

const cursorDiagnosticProbeConversation = "00000000-0000-0000-0000-000000000000"

type cursorDiagnoseDeps struct {
	homeDir           func() (string, error)
	currentExecutable func() (string, error)
	findOx            func() (string, error)
	checkHooks        func(string, string) (string, bool, error)
	sessionPath       func(string, string, string) (string, error)
	stat              func(string) (os.FileInfo, error)
	lstat             func(string) (os.FileInfo, error)
	openDirectory     func(string) error
}

func defaultCursorDiagnoseDeps() cursorDiagnoseDeps {
	return cursorDiagnoseDeps{
		homeDir:           os.UserHomeDir,
		currentExecutable: currentCursorExecutable,
		findOx:            findCursorOx,
		checkHooks:        checkCursorHooks,
		sessionPath: func(home, repo, conversationID string) (string, error) {
			return cursorpaths.ValidateSource(home, repo, conversationID, "")
		},
		stat:  os.Stat,
		lstat: os.Lstat,
		openDirectory: func(path string) error {
			directory, err := os.Open(path)
			if err != nil {
				return err
			}
			return directory.Close()
		},
	}
}

func handleDiagnose(p adapterprotocol.DiagnoseParams) (*adapterprotocol.DiagnoseResult, error) {
	return diagnoseCursor(p, defaultCursorDiagnoseDeps()), nil
}

func diagnoseCursor(p adapterprotocol.DiagnoseParams, deps cursorDiagnoseDeps) *adapterprotocol.DiagnoseResult {
	issues := make([]adapterprotocol.DiagnoseIssue, 0, 4)
	if p.Scope != "project" {
		issues = append(issues, adapterprotocol.DiagnoseIssue{
			Slug:     "unsupported-scope",
			Severity: "error",
			Title:    "Cursor integration scope is unsupported",
			Detail:   "Cursor Agents Window hooks currently support project scope only.",
		})
		return cursorDiagnoseResult(issues)
	}

	if p.RepoRoot == "" || !filepath.IsAbs(p.RepoRoot) {
		issues = append(issues, cursorWorkspaceIssue())
		return cursorDiagnoseResult(issues)
	}
	canonicalRepo, err := filepath.EvalSymlinks(filepath.Clean(p.RepoRoot))
	if err != nil {
		issues = append(issues, cursorWorkspaceIssue())
		return cursorDiagnoseResult(issues)
	}
	if info, statErr := deps.stat(canonicalRepo); statErr != nil || !info.IsDir() {
		issues = append(issues, cursorWorkspaceIssue())
		return cursorDiagnoseResult(issues)
	}

	executable, executableErr := deps.currentExecutable()
	if executableErr != nil {
		issues = append(issues, adapterprotocol.DiagnoseIssue{
			Slug:     "adapter-missing",
			Severity: "error",
			Title:    "Cursor adapter executable is unavailable",
			Detail:   "Reinstall ox so the bundled Cursor adapter is a regular executable.",
		})
	} else {
		issues = append(issues, diagnoseCursorHooks(canonicalRepo, executable, deps)...)
		if _, err := deps.findOx(); err != nil {
			issues = append(issues, adapterprotocol.DiagnoseIssue{
				Slug:     "adapter-missing",
				Severity: "error",
				Title:    "ox executable is unavailable to Cursor hooks",
				Detail:   "Reinstall ox or make it executable beside the Cursor adapter or on PATH.",
			})
		}
	}

	issues = append(issues, diagnoseCursorNativeRoot(canonicalRepo, deps)...)
	return cursorDiagnoseResult(issues)
}

func cursorWorkspaceIssue() adapterprotocol.DiagnoseIssue {
	return adapterprotocol.DiagnoseIssue{
		Slug:     "workspace-mismatch",
		Severity: "error",
		Title:    "Cursor project workspace is unavailable",
		Detail:   "Run ox doctor from the initialized project you want Cursor Agents Window to record.",
	}
}

func diagnoseCursorHooks(repoRoot, executable string, deps cursorDiagnoseDeps) []adapterprotocol.DiagnoseIssue {
	hooksPath, installed, err := deps.checkHooks(repoRoot, executable)
	if err != nil {
		return []adapterprotocol.DiagnoseIssue{{
			Slug:     "hooks-invalid",
			Severity: "error",
			Title:    "Cursor hooks configuration is invalid",
			Detail:   "The project hooks file is unreadable, malformed, unsupported, or unsafe. Correct it before reinstalling the Cursor integration.",
			Fix:      "ox integrate install --cursor",
		}}
	}
	if installed {
		return nil
	}

	_, statErr := deps.lstat(hooksPath)
	if errors.Is(statErr, os.ErrNotExist) {
		return []adapterprotocol.DiagnoseIssue{cursorRepairableHooksIssue(
			"hooks-missing",
			"Cursor hooks are not installed",
			"Install the project hooks so Cursor can start and drain Session recording.",
		)}
	}
	if statErr != nil {
		return []adapterprotocol.DiagnoseIssue{{
			Slug:     "hooks-invalid",
			Severity: "error",
			Title:    "Cursor hooks configuration is unreadable",
			Detail:   "The project hooks file cannot be inspected. Correct its permissions or file type before reinstalling the Cursor integration.",
			Fix:      "ox integrate install --cursor",
		}}
	}
	return []adapterprotocol.DiagnoseIssue{cursorRepairableHooksIssue(
		"hooks-invalid",
		"Cursor hooks are stale or incomplete",
		"Reinstall the project integration to restore the required Cursor lifecycle hooks.",
	)}
}

func cursorRepairableHooksIssue(slug, title, detail string) adapterprotocol.DiagnoseIssue {
	return adapterprotocol.DiagnoseIssue{
		Slug:     slug,
		Severity: "warning",
		Title:    title,
		Detail:   detail,
		Fix:      "ox integrate install --cursor",
		FixArgv:  []string{"ox", "integrate", "install", "--cursor"},
		FixSafe:  true,
	}
}

func diagnoseCursorNativeRoot(repoRoot string, deps cursorDiagnoseDeps) []adapterprotocol.DiagnoseIssue {
	home, err := deps.homeDir()
	if err != nil {
		return []adapterprotocol.DiagnoseIssue{cursorSourceUnreadableIssue()}
	}
	probe, err := deps.sessionPath(home, repoRoot, cursorDiagnosticProbeConversation)
	if err != nil {
		return []adapterprotocol.DiagnoseIssue{{
			Slug:     "invalid-source-path",
			Severity: "error",
			Title:    "Cursor Session source location is invalid",
			Detail:   "The native Cursor project source cannot be resolved safely for this workspace.",
		}}
	}
	transcriptRoot := filepath.Dir(filepath.Dir(probe))
	info, err := deps.stat(transcriptRoot)
	if errors.Is(err, os.ErrNotExist) {
		return []adapterprotocol.DiagnoseIssue{{
			Slug:     "source-not-found",
			Severity: "info",
			Title:    "No Cursor Session export exists for this project yet",
			Detail:   "Start or continue an Agents Window / This Mac chat in this project. A missing hook path hint alone does not mean Cursor export is disabled.",
		}}
	}
	if err != nil {
		return []adapterprotocol.DiagnoseIssue{cursorSourceUnreadableIssue()}
	}
	if !info.IsDir() {
		return []adapterprotocol.DiagnoseIssue{{
			Slug:     "invalid-source-path",
			Severity: "error",
			Title:    "Cursor Session source location is invalid",
			Detail:   "The expected native transcript root is not a directory.",
		}}
	}
	if err := deps.openDirectory(transcriptRoot); err != nil {
		return []adapterprotocol.DiagnoseIssue{cursorSourceUnreadableIssue()}
	}
	return nil
}

func cursorSourceUnreadableIssue() adapterprotocol.DiagnoseIssue {
	return adapterprotocol.DiagnoseIssue{
		Slug:     "source-unreadable",
		Severity: "error",
		Title:    "Cursor Session source is unreadable",
		Detail:   "The native Cursor transcript root for this project cannot be read. Check its permissions and retry.",
	}
}

func cursorDiagnoseResult(issues []adapterprotocol.DiagnoseIssue) *adapterprotocol.DiagnoseResult {
	return &adapterprotocol.DiagnoseResult{OK: len(issues) == 0, Issues: issues}
}
