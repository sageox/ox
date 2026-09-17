package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/session"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/spf13/cobra"
)

// isObservationalDoctor bypasses CLI startup telemetry and heartbeat side effects,
// as well as the repair-oriented doctor's auth refresh and auto-fix pipeline.
func isObservationalDoctor(cmd *cobra.Command) bool {
	if cmd.Name() != "doctor" {
		return false
	}
	check, _ := cmd.Flags().GetBool("check")
	return check
}

func runObservationalDoctor(cmd *cobra.Command) error {
	for _, name := range []string{"fix", "gc", "force-session-uploads"} {
		enabled, _ := cmd.Flags().GetBool(name)
		if enabled {
			return fmt.Errorf("--check cannot be combined with --%s", name)
		}
	}
	slugs, _ := cmd.Flags().GetStringSlice("fix-slug")
	if len(slugs) > 0 {
		return fmt.Errorf("--check cannot be combined with --fix-slug")
	}
	root := findGitRoot()
	checks := []JSONCheckResult{}
	add := func(name, status, message string) {
		checks = append(checks, JSONCheckResult{Name: name, Status: status, Message: message})
	}
	if root == "" {
		add("repository", "failed", "not inside a git repository")
	} else {
		if config.IsInitialized(root) {
			add("project initialized", "passed", "initialized")
		} else {
			add("project initialized", "failed", "run ox init to initialize this repository")
		}
		add("endpoint", "passed", endpoint.GetForProject(root))
		recording := config.ResolveSessionRecording(root)
		add("recording configuration", "passed", recording.Mode)
		states, readErr := session.LoadAllRecordingStates(root)
		if readErr != nil {
			add("Codex capture evidence", "failed", readErr.Error())
		} else {
			var latest *session.RecordingState
			for _, state := range states {
				if state.AdapterName == "codex" && (latest == nil || state.StartedAt.After(latest.StartedAt)) {
					latest = state
				}
			}
			if latest == nil || latest.LastHookAt == nil || latest.HookInvocations == 0 {
				add("Codex hook execution", "warning", "no executed hook observed; open Codex, approve its normal hook trust prompt, and send a prompt before checking again")
			} else {
				add("Codex hook execution", "passed", fmt.Sprintf("observed %d hook calls for native session %s", latest.HookInvocations, latest.AgentSessionID))
				if latest.EntryCount > 0 && session.HasSubstantiveEntries(filepath.Join(latest.SessionPath, "raw.jsonl")) {
					add("Codex captured entries", "passed", fmt.Sprintf("%d entries captured", latest.EntryCount))
				} else {
					add("Codex captured entries", "failed", "capture is header-only; verify the host daemon and native session path")
				}
			}
		}
		// git status is observational with optional index refresh disabled.
		// Bounded like the Ledger status check below: cmd.Context() normally
		// has no deadline, and a blocked fsmonitor hook or slow filesystem
		// must not hang `ox doctor --check` indefinitely.
		statusCtx, statusCancel := context.WithTimeout(cmd.Context(), 10*time.Second)
		git := exec.CommandContext(statusCtx, "git", "--no-optional-locks", "status", "--porcelain")
		git.Dir = root
		out, err := git.Output()
		statusCancel()
		if err != nil {
			add("repository state", "failed", err.Error())
		} else if strings.TrimSpace(string(out)) != "" {
			add("repository state", "warning", "uncommitted changes present; no repairs performed")
		} else {
			add("repository state", "passed", "clean")
		}
		if ledger := getLedgerPath(); ledger != "" {
			// A damaged Ledger can have tens of thousands of staged deletions.
			// Inspect only; neither index refresh nor recovery belongs in --check.
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			git := exec.CommandContext(ctx, "git", "--no-optional-locks", "-C", ledger, "status", "--porcelain", "--untracked-files=no")
			out, err := git.Output()
			cancel()
			conflicts, deletions := 0, 0
			for _, line := range strings.Split(string(out), "\n") {
				if len(line) < 3 {
					continue
				}
				if strings.Contains(line[:2], "U") || line[:2] == "AA" || line[:2] == "DD" {
					conflicts++
				}
				if line[0] == 'D' {
					deletions++
				}
			}
			switch {
			case err != nil:
				add("Ledger state", "failed", err.Error())
			case conflicts > 0 || deletions > 0:
				add("Ledger state", "failed", fmt.Sprintf("%d conflicts and %d staged deletions; preserve the Ledger before repair; no changes performed", conflicts, deletions))
			case strings.TrimSpace(string(out)) != "":
				add("Ledger state", "warning", "uncommitted changes present; no changes performed")
			default:
				add("Ledger state", "passed", "clean")
			}
		} else {
			add("Ledger state", "warning", "no local Ledger found; --check does not clone one")
		}
	}
	if daemon.IsResponsiveObservational() {
		add("capture daemon", "passed", "running")
	} else {
		add("capture daemon", "warning", "not running; --check does not start it")
	}
	add("authentication and remote delivery", "skipped", "not verified: observational checks do not refresh credentials or contact repair endpoints")
	output := JSONDoctorOutput{Categories: []JSONCategory{{Name: "Observational checks", Checks: checks}}}
	for _, c := range checks {
		switch c.Status {
		case "failed":
			output.Summary.Failed++
			output.Summary.HasFailed = true
		case "passed":
			output.Summary.Passed++
		case "warning":
			output.Summary.Warnings++
		case "skipped":
			output.Summary.Skipped++
		}
	}
	jsonMode, _ := cmd.Flags().GetBool("json")
	if jsonMode {
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(output); err != nil {
			return err
		}
		if output.Summary.HasFailed {
			return fmt.Errorf("some observational checks failed")
		}
		return nil
	}
	for _, c := range checks {
		fmt.Fprintf(cmd.OutOrStdout(), "%s: %s — %s\n", c.Status, c.Name, c.Message)
	}
	if output.Summary.HasFailed {
		return fmt.Errorf("some observational checks failed")
	}
	return nil
}

// Session import owns its explicit mutation boundary; preview must not inherit
// CLI telemetry, heartbeat, or cached-settings refresh writes.
func isSessionHistoryImport(cmd *cobra.Command) bool {
	return cmd.Name() == "import" && cmd.Parent() != nil && cmd.Parent().Name() == "session"
}
