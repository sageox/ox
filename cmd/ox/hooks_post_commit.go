package main

import (
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
)

// hooksPostCommitCmd appends HEAD SHA to the active recording's
// ProducedCommits index. Called by the post-commit git hook.
//
// Always exits 0 — post-commit hooks MUST never block the user's commit
// flow. Diagnostic output goes to debug-level logs only.
//
// The forward commit→session link is owned by prepare-commit-msg (which
// injects the SageOx-Session: trailer). This handler is the reverse-
// direction index — it lets `ox session view` and friends answer "what
// commits did this session produce?" without grepping git log.
var hooksPostCommitCmd = &cobra.Command{
	Use:          "post-commit",
	Short:        "Append HEAD SHA to active recording's ProducedCommits index",
	Hidden:       true,
	SilenceUsage: true,
	RunE:         runHooksPostCommit,
}

func init() {
	hooksCmd.AddCommand(hooksPostCommitCmd)
}

func runHooksPostCommit(cmd *cobra.Command, args []string) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		slog.Debug("hooks post-commit: project root not found", "error", err)
		return nil
	}

	if !config.IsInitialized(projectRoot) {
		return nil
	}

	state := loadActiveRecording(projectRoot)
	if state == nil {
		return nil
	}

	sha := readHeadSHA(projectRoot)
	if sha == "" {
		slog.Debug("hooks post-commit: empty HEAD sha")
		return nil
	}

	// Append unless already the last entry — guards against a hook firing
	// twice on the same SHA (e.g. via a chained hook script that calls ox
	// hooks post-commit more than once per commit).
	if n := len(state.ProducedCommits); n > 0 && state.ProducedCommits[n-1] == sha {
		return nil
	}

	if err := session.UpdateRecordingStateForAgent(projectRoot, state.AgentID, func(s *session.RecordingState) {
		// re-check under the load to handle a race between two
		// concurrent post-commit invocations in the same repo.
		if n := len(s.ProducedCommits); n > 0 && s.ProducedCommits[n-1] == sha {
			return
		}
		s.ProducedCommits = append(s.ProducedCommits, sha)
	}); err != nil {
		slog.Debug("hooks post-commit: update recording state failed", "error", err, "agent_id", state.AgentID)
	}
	return nil
}

// loadActiveRecording returns the recording state to update for the current
// commit, or nil if none. Resolution mirrors prepare-commit-msg:
//
//   - SAGEOX_AGENT_ID set: agent-specific lookup; subagent prefers parent.
//   - SAGEOX_AGENT_ID unset (human commit): workspace match against project root.
func loadActiveRecording(projectRoot string) *session.RecordingState {
	agentID := os.Getenv("SAGEOX_AGENT_ID")
	if agentID != "" {
		state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
		if err != nil || state == nil {
			return nil
		}
		if state.ParentAgentID != "" {
			if parent, _ := session.LoadRecordingStateForAgent(projectRoot, state.ParentAgentID); parent != nil {
				return parent
			}
		}
		return state
	}

	state, err := session.LoadRecordingStateForWorkspace(projectRoot, projectRoot)
	if err != nil {
		return nil
	}
	return state
}

// readHeadSHA returns the current HEAD commit SHA for the project repo, or
// empty string on any failure. Pure-Go alternative would parse .git/HEAD
// and resolve the ref ourselves, but shelling out to git rev-parse is the
// simplest correct path and post-commit always runs in a context where
// git is available.
func readHeadSHA(projectRoot string) string {
	cmd := exec.Command("git", "-C", projectRoot, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
