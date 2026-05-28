package main

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
)

// hooksPrePushCmd records PR + issue linkage for the active recording from
// the commit range being pushed. Called by the pre-push git hook, which
// pipes the ref lines on stdin:
//
//	<local-ref> <local-sha> <remote-ref> <remote-sha>
//
// For each ref with a non-zero remote-sha (branch already exists on the
// remote) the pushed range is <remote-sha>..<local-sha>; for a brand-new
// branch (zero remote-sha) the range is bounded against everything already
// on remotes so we don't walk the entire history.
//
// Always exits 0 — pre-push must never block a push on linkage best-effort.
var hooksPrePushCmd = &cobra.Command{
	Use:          "pre-push",
	Short:        "Record PR/issue linkage for the active recording from the pushed range",
	Hidden:       true,
	SilenceUsage: true,
	RunE:         runHooksPrePush,
}

var (
	hooksPrePushRemote string
	hooksPrePushURL    string
)

func init() {
	hooksPrePushCmd.Flags().StringVar(&hooksPrePushRemote, "remote", "", "remote name (from git hook $1)")
	hooksPrePushCmd.Flags().StringVar(&hooksPrePushURL, "url", "", "remote url (from git hook $2)")
	hooksCmd.AddCommand(hooksPrePushCmd)
}

// zeroSHA is git's all-zeros sentinel for "ref does not exist on the remote".
const zeroSHA = "0000000000000000000000000000000000000000"

// ghTimeout bounds the gh CLI call so a slow GitHub round-trip can't stall
// the user's push. The pre-push hook runs synchronously before the push
// proceeds; we cap the network cost.
const ghTimeout = 3 * time.Second

func runHooksPrePush(cmd *cobra.Command, args []string) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		slog.Debug("hooks pre-push: project root not found", "error", err)
		return nil
	}
	if !config.IsInitialized(projectRoot) {
		return nil
	}

	state := loadActiveRecording(projectRoot)
	if state == nil {
		return nil
	}

	refs := parsePrePushStdin(os.Stdin)
	if len(refs) == 0 {
		return nil
	}

	issues := map[string]struct{}{}
	for _, ref := range refs {
		commits := commitRangeForRef(projectRoot, ref)
		for _, sha := range commits {
			for _, issue := range parseIssueRefs(commitBody(projectRoot, sha)) {
				issues[issue] = struct{}{}
			}
		}
	}

	prs := resolvePRsForBranches(projectRoot, refs)

	if len(prs) == 0 && len(issues) == 0 {
		return nil
	}

	if err := session.UpdateRecordingStateForAgent(projectRoot, state.AgentID, func(s *session.RecordingState) {
		s.LinkedPRs = mergeUnique(s.LinkedPRs, prs)
		s.LinkedIssues = mergeUnique(s.LinkedIssues, mapKeys(issues))
	}); err != nil {
		slog.Debug("hooks pre-push: update recording state failed", "error", err, "agent_id", state.AgentID)
	}
	return nil
}

// prePushRef is one line of the pre-push stdin stream.
type prePushRef struct {
	localRef  string
	localSHA  string
	remoteRef string
	remoteSHA string
}

// parsePrePushStdin reads git's pre-push ref stream. Lines without four
// whitespace-separated fields are skipped. Refs whose local-sha is the zero
// SHA (a branch deletion) are dropped — nothing is being pushed.
func parsePrePushStdin(r io.Reader) []prePushRef {
	var refs []prePushRef
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		fields := strings.Fields(strings.TrimSpace(scanner.Text()))
		if len(fields) < 4 {
			continue
		}
		ref := prePushRef{
			localRef:  fields[0],
			localSHA:  fields[1],
			remoteRef: fields[2],
			remoteSHA: fields[3],
		}
		if ref.localSHA == zeroSHA {
			continue // branch deletion — nothing pushed
		}
		refs = append(refs, ref)
	}
	return refs
}

// commitRangeForRef returns the SHAs being pushed for one ref. For an
// existing remote branch the range is remoteSHA..localSHA; for a new branch
// it's localSHA with --not --remotes so we don't walk shared history.
func commitRangeForRef(projectRoot string, ref prePushRef) []string {
	var args []string
	if ref.remoteSHA == zeroSHA || ref.remoteSHA == "" {
		args = []string{"-C", projectRoot, "rev-list", ref.localSHA, "--not", "--remotes"}
	} else {
		args = []string{"-C", projectRoot, "rev-list", ref.remoteSHA + ".." + ref.localSHA}
	}
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return nil
	}
	var shas []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			shas = append(shas, line)
		}
	}
	return shas
}

// commitBody returns the full commit message body for a SHA, or "".
func commitBody(projectRoot, sha string) string {
	out, err := exec.Command("git", "-C", projectRoot, "log", "-1", "--format=%B", sha).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// issueRefPattern matches GitHub issue-closing keywords plus the issue
// number: "Fixes #12", "Closes: #34", "resolves #5", "GH-7". The number is
// captured in group 1 (for #N forms) or group 2 (for GH-N).
var issueRefPattern = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\b[:\s]+#(\d+)|\bGH-(\d+)\b`)

// parseIssueRefs extracts issue numbers from a commit message body, in the
// "#N" canonical form. Only closing-keyword references and GH-N are matched;
// a bare "#N" mention without a keyword is intentionally NOT captured to
// avoid linking every passing reference (e.g. "see #12 for context").
func parseIssueRefs(body string) []string {
	matches := issueRefPattern.FindAllStringSubmatch(body, -1)
	seen := map[string]struct{}{}
	var out []string
	for _, m := range matches {
		num := m[1]
		if num == "" {
			num = m[2]
		}
		if num == "" {
			continue
		}
		ref := "#" + num
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		out = append(out, ref)
	}
	return out
}

// resolvePRsForBranches returns the PR URLs for the local branches being
// pushed, resolved via the gh CLI. Returns nil when gh is unavailable or
// errors — issue refs (which need no network) are still recorded by the
// caller. Each distinct branch is queried once.
func resolvePRsForBranches(projectRoot string, refs []prePushRef) []string {
	if _, err := exec.LookPath("gh"); err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	var prs []string
	for _, ref := range refs {
		branch := strings.TrimPrefix(ref.localRef, "refs/heads/")
		if branch == "" || branch == ref.localRef {
			continue // not a branch ref (tag, etc.)
		}
		if _, ok := seen[branch]; ok {
			continue
		}
		seen[branch] = struct{}{}
		if url := prURLForBranch(projectRoot, branch); url != "" {
			prs = append(prs, url)
		}
	}
	return prs
}

// prURLForBranch shells out to gh to find an open PR whose head is branch.
// Bounded by ghTimeout. Returns "" on any failure or no match.
func prURLForBranch(projectRoot, branch string) string {
	ctx, cancel := context.WithTimeout(context.Background(), ghTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "list",
		"--head", branch, "--state", "open", "--json", "url", "--jq", ".[0].url")
	cmd.Dir = projectRoot
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// mergeUnique appends entries from add to base, skipping values already
// present in base or earlier in add. Order-preserving.
func mergeUnique(base, add []string) []string {
	seen := map[string]struct{}{}
	for _, v := range base {
		seen[v] = struct{}{}
	}
	out := base
	for _, v := range add {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// mapKeys returns the keys of a set in nondeterministic order. Callers that
// need stable order sort the result.
func mapKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
