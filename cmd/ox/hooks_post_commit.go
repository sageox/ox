package main

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/consultscan"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/session/contextuse"
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

	recordCommitCitations(projectRoot, state, sha)
	return nil
}

// recordCommitCitations appends a provable `used` event (mechanism citation,
// evidence cited_commit) to the active recording's context-trace for every
// surfaced ref or title the new commit's message names. The surfaced set is
// read from the trace already on disk, so this works on both finalize paths
// (CLI and daemon) without either having to rescan git history. Idempotent
// per (ref, commit); fail-open — any failure is a debug log, never an error,
// because a post-commit hook must never block a commit. Respects the
// recap.trace_consultations opt-out like every other trace writer.
func recordCommitCitations(projectRoot string, state *session.RecordingState, sha string) {
	defer func() {
		if r := recover(); r != nil {
			slog.Debug("hooks post-commit: citation scan recovered from panic", "recover", r)
		}
	}()
	if state == nil || state.SessionPath == "" || sha == "" {
		return
	}
	if !config.TraceConsultationsEnabled() {
		return
	}
	tracePath := filepath.Join(state.SessionPath, contexttrace.FileName)
	events, err := contexttrace.ReadEvents(tracePath)
	if err != nil {
		slog.Debug("hooks post-commit: read context-trace failed", "error", err)
		return
	}
	surfaced := contextuse.Surfaced(events)
	if len(surfaced) == 0 {
		return
	}
	msg := readCommitMessage(projectRoot, sha)
	if msg == "" {
		return
	}
	short := shortSHA(sha)
	already := map[string]bool{}
	for _, ev := range events {
		if ev.Type == contexttrace.EventUsed && ev.Evidence == contexttrace.EvidenceCitedCommit {
			already[ev.Ref+"\x00"+ev.Commit] = true
		}
	}
	var fresh []contexttrace.Event
	for _, s := range surfaced {
		if already[s.Ref+"\x00"+short] || !consultscan.MatchesRef(msg, s.Ref, s.Title) {
			continue
		}
		already[s.Ref+"\x00"+short] = true
		fresh = append(fresh, contexttrace.Event{
			Type:        contexttrace.EventUsed,
			Source:      contexttrace.SourceOnDemand,
			Mechanism:   contexttrace.MechanismCitation,
			Evidence:    contexttrace.EvidenceCitedCommit,
			Seq:         state.EntryCount,
			SurfacedSeq: s.Seq,
			Ref:         s.Ref,
			RefType:     s.RefType,
			Title:       s.Title,
			URL:         s.URL,
			Commit:      short,
		})
	}
	if len(fresh) == 0 {
		return
	}
	if err := contexttrace.NewFileWriter(tracePath).AppendAll(fresh); err != nil {
		slog.Debug("hooks post-commit: append cited_commit events failed", "error", err)
		return
	}
	slog.Debug("hooks post-commit: recorded commit citations", "commit", short, "refs", len(fresh))
	for _, ev := range fresh {
		recordStatuslineUse(projectRoot, state, ev.RefType, ev.Ref)
	}
}

// readCommitMessage returns the full message body of a commit, or "" on any
// failure.
func readCommitMessage(projectRoot, sha string) string {
	out, err := exec.Command("git", "-C", projectRoot, "log", "-1", "--format=%B", sha).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// shortSHA abbreviates a commit sha the way git does by default.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
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
