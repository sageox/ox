package main

import (
	"bufio"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
)

// hooksPostRewriteCmd consumes git's old→new SHA mapping on stdin and
// rewrites entries in the active recording's ProducedCommits index.
//
// Git invokes post-rewrite after `git commit --amend` and `git rebase`,
// piping lines of "<old-sha> <new-sha>" (with an optional third column on
// rebase mode that records the original head SHA, which we ignore). The
// $1 argument is "amend" or "rebase"; we accept it via --mode for
// observability but the handler logic is the same either way.
//
// Always exits 0 — never blocks the rewrite. No active recording = no-op.
//
// Behavior on closed sessions (D3): this handler ONLY touches the active
// RecordingState. Closed sessions whose ProducedCommits referenced one of
// the rewritten SHAs become stale; `ox doctor` surfaces this as a soft
// signal and an opt-in fix can be added later.
var hooksPostRewriteCmd = &cobra.Command{
	Use:          "post-rewrite",
	Short:        "Rewrite ProducedCommits SHAs in active recording from git's mapping",
	Hidden:       true,
	SilenceUsage: true,
	RunE:         runHooksPostRewrite,
}

var hooksPostRewriteMode string

func init() {
	hooksPostRewriteCmd.Flags().StringVar(&hooksPostRewriteMode, "mode", "", "rewrite mode passed by git: amend or rebase")
	hooksCmd.AddCommand(hooksPostRewriteCmd)
}

func runHooksPostRewrite(cmd *cobra.Command, args []string) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		slog.Debug("hooks post-rewrite: project root not found", "error", err)
		return nil
	}
	if !config.IsInitialized(projectRoot) {
		return nil
	}

	state := loadActiveRecording(projectRoot)
	if state == nil {
		return nil
	}
	if len(state.ProducedCommits) == 0 {
		return nil
	}

	mapping, err := parsePostRewriteStdin(os.Stdin)
	if err != nil {
		slog.Debug("hooks post-rewrite: parse stdin failed", "error", err)
		return nil
	}
	if len(mapping) == 0 {
		return nil
	}

	if err := session.UpdateRecordingStateForAgent(projectRoot, state.AgentID, func(s *session.RecordingState) {
		rewriteSHAs(s, mapping)
	}); err != nil {
		slog.Debug("hooks post-rewrite: update recording state failed",
			"error", err, "agent_id", state.AgentID, "mode", hooksPostRewriteMode)
	}
	return nil
}

// parsePostRewriteStdin reads git's old→new SHA stream. Each line:
//
//	<old-sha> <new-sha> [<rebase-extra-data>]
//
// We accept any whitespace separator and ignore extra columns. Blank
// lines and lines without at least two fields are skipped silently —
// git's stream is well-formed in practice, but defensive parsing keeps
// the handler unbreakable.
func parsePostRewriteStdin(r io.Reader) (map[string]string, error) {
	mapping := make(map[string]string)
	scanner := bufio.NewScanner(r)
	// allow long lines — rebase can pipe extra metadata
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		oldSHA, newSHA := fields[0], fields[1]
		if !looksLikeSHA(oldSHA) || !looksLikeSHA(newSHA) {
			continue
		}
		mapping[oldSHA] = newSHA
	}
	if err := scanner.Err(); err != nil {
		return mapping, err
	}
	return mapping, nil
}

// looksLikeSHA is a cheap shape guard: hex string of plausible length.
// Git can produce either full 40-char (SHA-1) or 64-char (SHA-256)
// digests depending on object format. We accept either, and any
// abbreviated length >= 7 (git's default short-hash threshold).
func looksLikeSHA(s string) bool {
	if len(s) < 7 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9',
			c >= 'a' && c <= 'f',
			c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// rewriteSHAs applies mapping to s.ProducedCommits in place, preserving
// order. Entries not in the mapping are left untouched (they refer to
// commits the rewrite didn't change).
//
// Squash/fixup case: git emits multiple "old new" pairs all pointing to
// the same new SHA (e.g. three squashed commits collapse to one). We
// rewrite each old→new individually, then collapse adjacent duplicates
// in a single pass so the list reflects the resulting history rather
// than the pre-squash history. This matches what `ox session view`
// users expect — "this session produced these commits in the current
// history."
func rewriteSHAs(s *session.RecordingState, mapping map[string]string) {
	if s == nil || len(s.ProducedCommits) == 0 {
		return
	}
	rewritten := make([]string, 0, len(s.ProducedCommits))
	for _, sha := range s.ProducedCommits {
		if newSHA, ok := mapping[sha]; ok {
			rewritten = append(rewritten, newSHA)
			continue
		}
		rewritten = append(rewritten, sha)
	}
	s.ProducedCommits = collapseAdjacentDuplicates(rewritten)
}

func collapseAdjacentDuplicates(in []string) []string {
	if len(in) <= 1 {
		return in
	}
	out := make([]string, 0, len(in))
	out = append(out, in[0])
	for i := 1; i < len(in); i++ {
		if in[i] == in[i-1] {
			continue
		}
		out = append(out, in[i])
	}
	return out
}
