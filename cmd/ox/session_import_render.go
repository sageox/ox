package main

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/session/nativeimport"
)

type importJSONSession struct {
	Agent        string `json:"agent"`
	NativeID     string `json:"native_id"`
	StartedAt    string `json:"started_at"`
	LastActivity string `json:"last_activity"`
	Messages     int    `json:"messages"`
	SizeBytes    int64  `json:"size_bytes"`
	Branch       string `json:"branch,omitempty"`
	State        string `json:"state"`
	Reason       string `json:"reason,omitempty"`
	CoveredBy    string `json:"covered_by,omitempty"`
	SessionName  string `json:"session_name"`
	SessionID    string `json:"session_id"`
	Selected     bool   `json:"selected"`
	Outcome      string `json:"outcome,omitempty"`
	Detail       string `json:"detail,omitempty"`
	URL          string `json:"url,omitempty"`
	Retry        string `json:"retry,omitempty"`
}

type importJSONOutput struct {
	Status      string              `json:"status"`
	Destination importDestination   `json:"destination"`
	Sessions    []importJSONSession `json:"sessions"`
	Counts      map[string]int      `json:"counts"`
	Ignored     importIgnored       `json:"ignored"`
	NextCommand string              `json:"next_command,omitempty"`
	Guidance    string              `json:"guidance,omitempty"`
}

func importJSON(status string, dest importDestination, cands []*importCandidate, ignored importIgnored) importJSONOutput {
	out := importJSONOutput{Status: status, Destination: dest, Counts: map[string]int{}, Ignored: ignored}
	for _, c := range cands {
		s := c.Session
		out.Sessions = append(out.Sessions, importJSONSession{
			Agent: string(s.Agent), NativeID: s.NativeID,
			StartedAt: s.StartedAt.Format("2006-01-02T15:04:05Z"), LastActivity: s.LastActivity.Format("2006-01-02T15:04:05Z"),
			Messages: s.Messages(), SizeBytes: s.Size, Branch: s.Branch,
			State: string(c.State), Reason: c.Reason, CoveredBy: c.Covered,
			SessionName: c.Name, SessionID: c.SessionID, Selected: c.Selected,
			Outcome: c.Outcome, Detail: c.Detail, URL: c.URL, Retry: c.Retry,
		})
		out.Counts[string(c.State)]++
		if c.Outcome != "" {
			out.Counts["outcome_"+c.Outcome]++
		}
	}
	return out
}

// renderImportPreview shows what an import would do. previewOnly means the
// run stops here, so it ends with the command that uploads.
func renderImportPreview(w io.Writer, opts importOptions, dest importDestination, cands []*importCandidate, ignored importIgnored, previewOnly bool) error {
	selected := selectedCandidates(cands)
	if opts.jsonOut {
		if !previewOnly {
			return nil // the JSON report is printed once, at the end
		}
		out := importJSON("preview", dest, cands, ignored)
		if len(selected) > 0 {
			out.NextCommand = importUploadCommand(opts, selected)
		}
		if opts.agentCtx {
			out.Guidance = importAgentGuidance
			if len(selected) == 0 {
				out.Guidance = importNothingGuidance
			}
		}
		return cli.PrintJSONTo(w, out)
	}

	visibility := dest.Visibility
	if visibility == "public" {
		visibility = "PUBLIC: anyone can read it"
	}
	team := dest.Team
	if team == "" {
		team = "your team"
	}
	fmt.Fprintf(w, "Destination  %s · %s · %s\n", team, dest.RepoID, visibility)
	fmt.Fprintf(w, "Summaries    %s\n", summarizerLine(cands))
	fmt.Fprintln(w)

	fmt.Fprintf(w, "Ready to upload (%d)\n", len(selected))
	for _, c := range selected {
		s := c.Session
		branch := s.Branch
		if len(branch) > 14 {
			branch = branch[:13] + "…"
		}
		fmt.Fprintf(w, "  %s  %-6s %4d msgs  %8s  %-14s %s\n",
			s.StartedAt.Local().Format("2006-01-02"), s.Agent, s.Messages(), formatBytes(s.Size), branch, nativeShortID(s.NativeID))
	}

	skipped := map[string]int{}
	var hints []string
	for _, c := range cands {
		if c.Selected || c.State == stateReady {
			continue
		}
		label := skipLabel(c)
		skipped[label]++
		if c.State == stateInProgress && !importHasString(hints, "in progress → finish it, then rerun") {
			hints = append(hints, "in progress → finish it, then rerun")
		}
		if c.State == stateNeedsSummarizer && !importHasString(hints, "needs summarizer → --summarizer claude|codex") {
			hints = append(hints, "needs summarizer → --summarizer claude|codex")
		}
	}
	if n := countValues(skipped); n > 0 {
		fmt.Fprintf(w, "Skipped (%d)\n", n)
		labels := make([]string, 0, len(skipped))
		for label := range skipped {
			labels = append(labels, label)
		}
		sort.Strings(labels)
		for _, label := range labels {
			fmt.Fprintf(w, "  %s (%d)\n", label, skipped[label])
		}
		for _, h := range hints {
			fmt.Fprintf(w, "  %s\n", h)
		}
	}
	if line := ignoredLine(ignored); line != "" {
		fmt.Fprintf(w, "Ignored      %s\n", line)
	}
	if previewOnly {
		fmt.Fprintln(w)
		if len(selected) == 0 {
			fmt.Fprintln(w, "Nothing to upload.")
		} else {
			fmt.Fprintf(w, "Nothing was uploaded. To upload these %d session%s:\n  %s\n", len(selected), plural(len(selected)), importUploadCommand(opts, selected))
		}
	}
	return nil
}

func skipLabel(c *importCandidate) string {
	switch c.State {
	case stateAlreadyImported:
		if c.Reason != "" {
			return "already imported, continued since"
		}
		return "already imported"
	case stateNotShared:
		verdict, _, _ := strings.Cut(c.Reason, ":")
		return verdict
	case stateRecordedLive:
		if c.Reason != "" {
			return "recorded live by ox, not from its start"
		}
		return "recorded live by ox"
	case stateInProgress:
		return "in progress"
	case stateNeedsSummarizer:
		return "needs summarizer"
	case stateIneligible:
		return c.Reason
	case stateReady:
		return "not selected"
	}
	return string(c.State)
}

func summarizerLine(cands []*importCandidate) string {
	seen := map[nativeimport.Agent]bool{}
	var parts []string
	for _, c := range cands {
		if !c.Selected || seen[c.Summarizer] {
			continue
		}
		seen[c.Summarizer] = true
		parts = append(parts, fmt.Sprintf("%s · %s · your login", c.Summarizer, summarizerModelLabel(c.Summarizer)))
	}
	if len(parts) == 0 {
		return "none needed"
	}
	return strings.Join(parts, "; ")
}

func ignoredLine(ig importIgnored) string {
	var parts []string
	add := func(n int, label string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, label))
		}
	}
	add(ig.OtherFolders, "other folders")
	add(ig.OxRuns, "ox runs")
	add(ig.SubagentThreads, "subagent threads")
	add(ig.InternalThreads, "Codex internal threads")
	add(ig.Unreadable, "unreadable")
	return strings.Join(parts, " · ")
}

// printImportLine finishes a session's progress line.
func printImportLine(w io.Writer, c *importCandidate) {
	switch c.Outcome {
	case "committed":
		fmt.Fprintln(w, "ready")
	case "skipped":
		fmt.Fprintf(w, "skipped: %s\n", c.Detail)
	case "failed":
		fmt.Fprintf(w, "failed\n      %s\n      retry: %s\n", c.Detail, c.Retry)
	default:
		fmt.Fprintln(w)
	}
}

// renderImportResult prints the final report: each uploaded session's link and
// an honest count of what happened.
func renderImportResult(w io.Writer, opts importOptions, dest importDestination, cands []*importCandidate, ignored importIgnored) error {
	if opts.jsonOut {
		return cli.PrintJSONTo(w, importJSON("done", dest, cands, ignored))
	}
	counts := map[string]int{}
	selected := 0
	for _, c := range cands {
		if c.Selected {
			selected++
			counts[c.Outcome]++
		}
		if c.Outcome == "uploaded" && c.URL != "" {
			fmt.Fprintf(w, "  %s → %s\n", nativeShortID(c.Session.NativeID), c.URL)
		}
		if c.Outcome == "committed" {
			fmt.Fprintf(w, "  %s %s\n", nativeShortID(c.Session.NativeID), c.Detail)
		}
	}
	fmt.Fprintf(w, "\n%d selected · %d uploaded", selected, counts["uploaded"])
	for _, k := range []string{"committed", "failed", "skipped"} {
		if counts[k] > 0 {
			fmt.Fprintf(w, " · %d %s", counts[k], k)
		}
	}
	fmt.Fprintln(w)
	return nil
}

func importHasString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func countValues(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}
