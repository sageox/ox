package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/sageox/agentx"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/fileutil"
)

// Conductor run script: Conductor's chat has no status bar, but its Run pane
// runs a repository's [scripts.run.<id>] commands in a live terminal. ox adds
// one, "ox", that runs `ox statusline --watch` there.
//
// It goes in .conductor/settings.local.toml, the machine-local layer, kept out
// of git through .git/info/exclude: the status line is a per-coworker choice,
// and the committed .conductor/settings.toml is the team's.
//
// ox appends its table as text, so the coworker's comments and layout
// survive, and writes only when no other run script is defined in either
// layer. How Conductor layers [scripts.run] between the shared and local files
// is not documented; with a run script of the team's in play, a local table
// could hide it. So in that case ox leaves both files alone and says how to
// add the script by hand.

const (
	conductorRunScriptID      = "ox"
	conductorRunScriptCommand = "ox statusline --watch"
	conductorLocalSettings    = "settings.local.toml"
	conductorSharedSettings   = "settings.toml"
	conductorRunScriptCheck   = "Conductor status line"
)

// conductorRunScriptBlock is the table ox appends to settings.local.toml.
const conductorRunScriptBlock = `
# Added by ox: ox's status line, live in Conductor's Run pane.
# To remove it for good: ox config set statusline off, then delete
# this table (ox doctor --fix adds it back while the status line is on).
[scripts.run.` + conductorRunScriptID + `]
command = "` + conductorRunScriptCommand + `"
icon = "activity"
available_in = ["local"]
`

// conductorRunScriptStatus is what the settings say about ox's run script.
type conductorRunScriptStatus int

const (
	conductorRunScriptMissing conductorRunScriptStatus = iota
	conductorRunScriptCurrent
	// conductorRunScriptForeign: a [scripts.run.ox] that runs something else.
	conductorRunScriptForeign
	// conductorRunScriptBlocked: other run scripts (or a legacy string
	// scripts.run) exist, so ox will not add its own; see the file comment.
	conductorRunScriptBlocked
	// conductorRunScriptUnreadable: a settings file is not valid TOML.
	conductorRunScriptUnreadable
)

// conductorRepoRoot is the Conductor repository's root directory: the main
// checkout, which owns .conductor/, not the workspace worktree ox runs in.
func conductorRepoRoot(gitRoot string) string {
	if root := strings.TrimSpace(os.Getenv("CONDUCTOR_ROOT_PATH")); root != "" {
		return root
	}
	out, err := exec.Command("git", "-C", gitRoot, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return gitRoot
	}
	commonDir := strings.TrimSpace(string(out))
	if filepath.Base(commonDir) != ".git" {
		return gitRoot // bare or unusual layout: stay in the worktree
	}
	return filepath.Dir(commonDir)
}

// conductorInUse reports whether this repository is driven by Conductor:
// ox runs inside it, or its root already has Conductor settings.
func conductorInUse(repoRoot string) bool {
	if detectOrchestrator() == string(agentx.AgentTypeConductor) {
		return true
	}
	info, err := os.Stat(filepath.Join(repoRoot, ".conductor"))
	return err == nil && info.IsDir()
}

// readConductorRuns returns the scripts.run value of one settings file: nil
// when the file or key is absent.
func readConductorRuns(path string) (any, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Tolerant: Conductor adds keys over time; only scripts.run is read.
	var doc struct {
		Scripts struct {
			Run any `toml:"run"`
		} `toml:"scripts"`
	}
	if err := toml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return doc.Scripts.Run, nil
}

// classifyConductorRunScript reads both settings layers under repoRoot.
func classifyConductorRunScript(repoRoot string) (conductorRunScriptStatus, error) {
	dir := filepath.Join(repoRoot, ".conductor")
	status := conductorRunScriptMissing
	for _, name := range []string{conductorSharedSettings, conductorLocalSettings} {
		run, err := readConductorRuns(filepath.Join(dir, name))
		if err != nil {
			return conductorRunScriptUnreadable, fmt.Errorf("%s: %w", name, err)
		}
		switch run := run.(type) {
		case nil:
		case map[string]any:
			for id, v := range run {
				if id != conductorRunScriptID {
					if status == conductorRunScriptMissing {
						status = conductorRunScriptBlocked
					}
					continue
				}
				entry, _ := v.(map[string]any)
				if cmd, _ := entry["command"].(string); strings.TrimSpace(cmd) == conductorRunScriptCommand {
					return conductorRunScriptCurrent, nil
				}
				return conductorRunScriptForeign, nil
			}
		default: // legacy single-string scripts.run
			if status == conductorRunScriptMissing {
				status = conductorRunScriptBlocked
			}
		}
	}
	return status, nil
}

// installConductorRunScript appends ox's run script to settings.local.toml
// and keeps that file out of git. The caller has classified it as missing.
func installConductorRunScript(repoRoot string) error {
	dir := filepath.Join(repoRoot, ".conductor")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, conductorLocalSettings)
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	next := string(existing)
	if next != "" && !strings.HasSuffix(next, "\n") {
		next += "\n"
	}
	next += conductorRunScriptBlock
	// Never write a file Conductor cannot read.
	var probe map[string]any
	if err := toml.Unmarshal([]byte(next), &probe); err != nil {
		return fmt.Errorf("%s would not be valid TOML: %w", path, err)
	}
	// The file can hold a coworker's environment values: keep the mode they
	// gave it, and make a new one private.
	mode := os.FileMode(0o600)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	}
	if err := fileutil.AtomicWriteBytes(path, []byte(next), mode); err != nil {
		return err
	}
	return excludeFromGit(repoRoot, ".conductor/"+conductorLocalSettings)
}

// excludeFromGit adds pattern to the repository's .git/info/exclude unless
// git already ignores it, so a machine-local file is never committed.
func excludeFromGit(repoRoot, pattern string) error {
	if exec.Command("git", "-C", repoRoot, "check-ignore", "-q", pattern).Run() == nil {
		return nil
	}
	out, err := exec.Command("git", "-C", repoRoot, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return err
	}
	exclude := filepath.Join(strings.TrimSpace(string(out)), "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(exclude), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(exclude, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "\n# ox: machine-local Conductor settings\n/%s\n", pattern)
	return err
}

// ensureConductorRunScript is `ox init`'s step: in a repository Conductor
// drives, with the status line on, add ox's run script when it is missing
// and nothing else is in the way. Best-effort; doctor reports the rest.
func ensureConductorRunScript(gitRoot string, quiet bool) {
	repoRoot := conductorRepoRoot(gitRoot)
	if !conductorInUse(repoRoot) || !statuslineEnabled() {
		return
	}
	if status, _ := classifyConductorRunScript(repoRoot); status != conductorRunScriptMissing {
		return
	}
	if err := installConductorRunScript(repoRoot); err != nil {
		slog.Debug("init: conductor run script", "err", err)
		return
	}
	if !quiet {
		cli.PrintSuccess("Added ox's status line to Conductor's Run pane")
	}
}

// checkConductorRunScript is the doctor check: in a repository Conductor
// drives, with the status line on, ox's run script is in the Run pane.
func checkConductorRunScript(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck(conductorRunScriptCheck, "not in git repo", "")
	}
	return checkConductorRunScriptAt(gitRoot, fix)
}

func checkConductorRunScriptAt(gitRoot string, fix bool) checkResult {
	repoRoot := conductorRepoRoot(gitRoot)
	if !conductorInUse(repoRoot) {
		return SkippedCheck(conductorRunScriptCheck, "not a Conductor repository", "")
	}
	// Before anything is asked of the coworker: a line turned off needs no script.
	if !statuslineEnabled() {
		return PassedCheck(conductorRunScriptCheck, "status line off (ox config set statusline on to show it)")
	}
	status, err := classifyConductorRunScript(repoRoot)
	localPath := filepath.Join(repoRoot, ".conductor", conductorLocalSettings)
	switch status {
	case conductorRunScriptUnreadable:
		return WarningCheck(conductorRunScriptCheck, "settings unreadable", err.Error())
	case conductorRunScriptCurrent:
		return PassedCheck(conductorRunScriptCheck, "ox run script installed (Run pane → ox)")
	case conductorRunScriptForeign:
		return PassedCheck(conductorRunScriptCheck, "custom [scripts.run.ox] kept; ox's line is not in the Run pane")
	case conductorRunScriptBlocked:
		return WarningCheck(conductorRunScriptCheck, "not added: this repository has its own run scripts",
			"To see ox's line in Conductor's Run pane, add to "+localPath+":\n"+conductorRunScriptBlock)
	}
	if !fix {
		return WarningCheck(conductorRunScriptCheck, "ox's line is not in Conductor's Run pane",
			"Run `ox doctor --fix` to add an \"ox\" run script to "+localPath)
	}
	if err := installConductorRunScript(repoRoot); err != nil {
		return FailedCheck(conductorRunScriptCheck, "install failed", err.Error())
	}
	return PassedCheck(conductorRunScriptCheck, "added \"ox\" run script to "+localPath)
}
