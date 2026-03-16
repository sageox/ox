package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/store"
	"github.com/sageox/ox/internal/repotools"
	"github.com/spf13/cobra"
)

// insight result types — JSON tags define agent output contract

type insightHotspot struct {
	Path          string   `json:"path"`
	Changes       int      `json:"changes"`
	RecentCommits []string `json:"recent_commits"`
}

type insightContention struct {
	Path           string   `json:"path"`
	WorkspaceCount int      `json:"workspace_count"`
	Workspaces     []string `json:"workspaces"`
}

type insightCommit struct {
	Hash      string   `json:"hash"`
	Author    string   `json:"author"`
	Message   string   `json:"message"`
	Files     []string `json:"files"`
	Age       string   `json:"age"`
	Timestamp int64    `json:"-"`
}

type insightPR struct {
	Number int      `json:"number"`
	Title  string   `json:"title"`
	Author string   `json:"author"`
	Labels []string `json:"labels,omitempty"`
}

type insightIssue struct {
	Number int      `json:"number"`
	Title  string   `json:"title"`
	Author string   `json:"author"`
	Labels []string `json:"labels,omitempty"`
}

type insightsOutput struct {
	Hotspots      []insightHotspot    `json:"hotspots,omitempty"`
	Contention    []insightContention `json:"contention,omitempty"`
	RecentCommits []insightCommit     `json:"recent_commits,omitempty"`
	OpenPRs       []insightPR         `json:"open_prs,omitempty"`
	OpenIssues    []insightIssue      `json:"open_issues,omitempty"`
	Guidance      string              `json:"guidance,omitempty"`
	Hints         *insightHints       `json:"hints,omitempty"`
}

// insightHints provides progressive disclosure — tells agents how to access
// deeper data beyond the summary layer.
type insightHints struct {
	PRDetails    string `json:"pr_details,omitempty"`
	IssueDetails string `json:"issue_details,omitempty"`
}

var codeInsightsCmd = &cobra.Command{
	Use:   "insights",
	Short: "Show planning-relevant code insights (hotspots, contention, recent activity)",
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := repotools.FindRepoRoot(repotools.VCSGit)
		if err != nil {
			return fmt.Errorf("not in a git repository")
		}

		dataDir := resolveCodeDBDir(root)
		if _, err := os.Stat(dataDir); os.IsNotExist(err) {
			return fmt.Errorf("no code index found — run 'ox code index' first")
		}

		db, err := codedb.Open(dataDir)
		if err != nil {
			return fmt.Errorf("open codedb: %w", err)
		}
		defer db.Close()

		days, _ := cmd.Flags().GetInt("days")
		limit, _ := cmd.Flags().GetInt("limit")
		jsonOut, _ := cmd.Flags().GetBool("json")

		// auto-detect: default to JSON when called by an agent
		agentID, _ := detectAgentContext()
		if agentID != "" && !cmd.Flags().Changed("json") {
			jsonOut = true
		}

		s := db.Store()
		out := insightsOutput{}

		out.Hotspots, _ = queryHotspots(s, days, limit)
		out.Contention, _ = queryContention(s, days, limit)
		out.RecentCommits, _ = queryRecentCommits(s, days, limit)
		out.OpenPRs, _ = queryOpenPRs(s, limit)
		out.OpenIssues, _ = queryOpenIssues(s, limit)
		out.Guidance = generateGuidance(out.Hotspots, out.Contention)
		out.Hints = generateHints(s, out.OpenPRs, out.OpenIssues)

		var outputBytes int
		if jsonOut {
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetIndent("", "  ")
			if err := enc.Encode(out); err != nil {
				return err
			}
			outputBytes = buf.Len()
			if _, err := buf.WriteTo(os.Stdout); err != nil {
				return err
			}
		} else {
			rendered := renderInsightsHuman(out, days)
			outputBytes = len(rendered)
			fmt.Print(rendered)
		}

		if agentID != "" {
			slog.Debug("code insights context cost", "agent_id", agentID, "bytes", outputBytes)
			trackContextBytes(int64(outputBytes))
		}
		return nil
	},
}

func queryHotspots(s *store.Store, days, limit int) ([]insightHotspot, error) {
	// two-step: get hotspot paths+counts, then fetch recent messages per path
	rows, err := s.Query(`
		SELECT d.path, COUNT(*) as changes
		FROM diffs d
		JOIN commits c ON c.id = d.commit_id
		WHERE c.timestamp > unixepoch('now', '-' || ? || ' days')
		GROUP BY d.path ORDER BY changes DESC LIMIT ?`,
		days, limit)
	if err != nil {
		slog.Warn("hotspots query failed", "error", err)
		return nil, err
	}
	defer rows.Close()

	var results []insightHotspot
	for rows.Next() {
		var h insightHotspot
		if err := rows.Scan(&h.Path, &h.Changes); err != nil {
			continue
		}
		results = append(results, h)
	}

	// fetch recent commit messages for each hotspot
	for i := range results {
		msgRows, err := s.Query(`
			SELECT DISTINCT
			    CASE WHEN instr(c.message, char(10)) > 0
			    THEN substr(c.message, 1, instr(c.message, char(10)) - 1)
			    ELSE substr(c.message, 1, 72) END
			FROM diffs d
			JOIN commits c ON c.id = d.commit_id
			WHERE d.path = ? AND c.timestamp > unixepoch('now', '-' || ? || ' days')
			ORDER BY c.timestamp DESC LIMIT 5`,
			results[i].Path, days)
		if err != nil {
			continue
		}
		for msgRows.Next() {
			var msg string
			if msgRows.Scan(&msg) == nil && msg != "" {
				results[i].RecentCommits = append(results[i].RecentCommits, msg)
			}
		}
		msgRows.Close()
	}

	return results, nil
}

func queryContention(s *store.Store, days, limit int) ([]insightContention, error) {
	rows, err := s.Query(`
		SELECT d.path, COUNT(DISTINCT r.name) as workspace_count,
		       GROUP_CONCAT(DISTINCT r.name) as workspaces
		FROM diffs d
		JOIN commits c ON c.id = d.commit_id
		JOIN repos r ON r.id = c.repo_id
		WHERE c.timestamp > unixepoch('now', '-' || ? || ' days')
		GROUP BY d.path HAVING workspace_count > 1
		ORDER BY workspace_count DESC LIMIT ?`,
		days, limit)
	if err != nil {
		slog.Warn("contention query failed", "error", err)
		return nil, err
	}
	defer rows.Close()

	var results []insightContention
	for rows.Next() {
		var c insightContention
		var ws string
		if err := rows.Scan(&c.Path, &c.WorkspaceCount, &ws); err != nil {
			continue
		}
		c.Workspaces = splitAndTrim(ws, ",")
		results = append(results, c)
	}
	return results, nil
}

func queryRecentCommits(s *store.Store, days, limit int) ([]insightCommit, error) {
	rows, err := s.Query(`
		SELECT c.hash, c.author, c.message, c.timestamp,
		       GROUP_CONCAT(d.path) as files
		FROM commits c
		JOIN diffs d ON d.commit_id = c.id
		WHERE c.timestamp > unixepoch('now', '-' || ? || ' days')
		GROUP BY c.id ORDER BY c.timestamp DESC LIMIT ?`,
		days, limit)
	if err != nil {
		slog.Warn("recent commits query failed", "error", err)
		return nil, err
	}
	defer rows.Close()

	var results []insightCommit
	for rows.Next() {
		var c insightCommit
		var files string
		var ts int64
		if err := rows.Scan(&c.Hash, &c.Author, &c.Message, &ts, &files); err != nil {
			continue
		}
		c.Timestamp = ts
		c.Age = formatTimeAgo(time.Unix(ts, 0))
		c.Files = splitAndTrim(files, ",")
		// short hash for display
		if len(c.Hash) > 7 {
			c.Hash = c.Hash[:7]
		}
		// first line only for message
		if idx := strings.IndexByte(c.Message, '\n'); idx > 0 {
			c.Message = c.Message[:idx]
		}
		results = append(results, c)
	}
	return results, nil
}

func queryOpenPRs(s *store.Store, limit int) ([]insightPR, error) {
	rows, err := s.Query(`
		SELECT number, title, COALESCE(author, ''), COALESCE(labels, '')
		FROM pull_requests WHERE state = 'open'
		ORDER BY number DESC LIMIT ?`, limit)
	if err != nil {
		slog.Warn("open PRs query failed", "error", err)
		return nil, err
	}
	defer rows.Close()

	var results []insightPR
	for rows.Next() {
		var p insightPR
		var labels string
		if err := rows.Scan(&p.Number, &p.Title, &p.Author, &labels); err != nil {
			continue
		}
		if labels != "" {
			p.Labels = splitAndTrim(labels, ",")
		}
		results = append(results, p)
	}
	return results, nil
}

func queryOpenIssues(s *store.Store, limit int) ([]insightIssue, error) {
	rows, err := s.Query(`
		SELECT number, title, COALESCE(author, ''), COALESCE(labels, '')
		FROM issues WHERE state = 'open'
		ORDER BY number DESC LIMIT ?`, limit)
	if err != nil {
		slog.Warn("open issues query failed", "error", err)
		return nil, err
	}
	defer rows.Close()

	var results []insightIssue
	for rows.Next() {
		var i insightIssue
		var labels string
		if err := rows.Scan(&i.Number, &i.Title, &i.Author, &labels); err != nil {
			continue
		}
		if labels != "" {
			i.Labels = splitAndTrim(labels, ",")
		}
		results = append(results, i)
	}
	return results, nil
}

// generateHints returns progressive disclosure hints when GitHub data is present.
// Checks total counts (not just open) since agents most often need merged PR content.
func generateHints(s *store.Store, openPRs []insightPR, openIssues []insightIssue) *insightHints {
	hasPRs := len(openPRs) > 0
	hasIssues := len(openIssues) > 0

	// check for any indexed PRs/issues if none are open
	if !hasPRs {
		var count int
		row := s.QueryRow("SELECT COUNT(*) FROM pull_requests")
		if row.Scan(&count) == nil && count > 0 {
			hasPRs = true
		}
	}
	if !hasIssues {
		var count int
		row := s.QueryRow("SELECT COUNT(*) FROM issues")
		if row.Scan(&count) == nil && count > 0 {
			hasIssues = true
		}
	}

	if !hasPRs && !hasIssues {
		return nil
	}

	h := &insightHints{}
	if hasPRs {
		h.PRDetails = `Use 'ox code search "<query>" type:pr' for full PR descriptions, review comments, and discussion`
	}
	if hasIssues {
		h.IssueDetails = `Use 'ox code search "<query>" type:issue' for issue descriptions and comments`
	}
	return h
}

func generateGuidance(hotspots []insightHotspot, contention []insightContention) string {
	if len(hotspots) == 0 && len(contention) == 0 {
		return ""
	}

	var parts []string

	// find files that are both hot AND contended — highest risk
	contentionPaths := make(map[string]int)
	for _, c := range contention {
		contentionPaths[c.Path] = c.WorkspaceCount
	}

	for _, h := range hotspots {
		if ws, ok := contentionPaths[h.Path]; ok {
			parts = append(parts, fmt.Sprintf("%s is a hotspot (%d changes, %d workspaces) — plan changes carefully",
				h.Path, h.Changes, ws))
			break
		}
	}

	if len(parts) == 0 {
		if len(hotspots) > 0 {
			parts = append(parts, fmt.Sprintf("%s is the most active file (%d changes in window)",
				hotspots[0].Path, hotspots[0].Changes))
		}
		if len(contention) > 0 {
			high := 0
			for _, c := range contention {
				if c.WorkspaceCount >= 3 {
					high++
				}
			}
			if high > 0 {
				parts = append(parts, fmt.Sprintf("%d files touched by 3+ workspaces — review before merging", high))
			}
		}
	}

	return strings.Join(parts, ". ") + "."
}

func renderInsightsHuman(out insightsOutput, days int) string {
	var b strings.Builder

	b.WriteString(statusHeaderStyle.Render(fmt.Sprintf("Code Insights (%dd)", days)))
	b.WriteString("\n")
	b.WriteString(statusMutedStyle.Render(strings.Repeat("─", 19+len(fmt.Sprintf("%d", days)))))
	b.WriteString("\n")

	empty := true

	if len(out.Hotspots) > 0 {
		empty = false
		b.WriteString("\n")
		b.WriteString(statusHeaderStyle.Render("Hotspots"))
		b.WriteString("\n")
		for _, h := range out.Hotspots {
			b.WriteString(fmt.Sprintf("  %s  %s\n",
				statusWarningStyle.Render(fmt.Sprintf("%d changes", h.Changes)),
				statusValueStyle.Render(h.Path)))
			if len(h.RecentCommits) > 0 {
				msgs := h.RecentCommits
				if len(msgs) > 3 {
					msgs = msgs[:3]
				}
				// truncate each message and join
				var short []string
				for _, m := range msgs {
					if len(m) > 50 {
						m = m[:47] + "..."
					}
					short = append(short, m)
				}
				b.WriteString(fmt.Sprintf("              %s\n",
					statusMutedStyle.Render(strings.Join(short, ", "))))
			}
		}
	}

	if len(out.Contention) > 0 {
		empty = false
		b.WriteString("\n")
		b.WriteString(statusHeaderStyle.Render("Contention Risk"))
		b.WriteString("\n")
		for _, c := range out.Contention {
			wsLabel := fmt.Sprintf("%d workspaces", c.WorkspaceCount)
			style := statusValueStyle
			if c.WorkspaceCount >= 3 {
				style = statusWarningStyle
			}
			b.WriteString(fmt.Sprintf("  %s  %s  %s\n",
				style.Render(wsLabel),
				statusValueStyle.Render(c.Path),
				statusMutedStyle.Render("("+strings.Join(c.Workspaces, ", ")+")")))
		}
	}

	if len(out.RecentCommits) > 0 {
		empty = false
		b.WriteString("\n")
		b.WriteString(statusHeaderStyle.Render("Recent Commits"))
		b.WriteString("\n")
		for _, c := range out.RecentCommits {
			msg := c.Message
			if len(msg) > 60 {
				msg = msg[:57] + "..."
			}
			b.WriteString(fmt.Sprintf("  %s  %s  %s  %s\n",
				statusMutedStyle.Render(c.Hash),
				statusHighlightStyle.Render(c.Author),
				statusValueStyle.Render(msg),
				statusMutedStyle.Render("("+c.Age+")")))
		}
	}

	if len(out.OpenPRs) > 0 {
		empty = false
		b.WriteString("\n")
		b.WriteString(statusHeaderStyle.Render("Open PRs"))
		b.WriteString("\n")
		for _, p := range out.OpenPRs {
			title := p.Title
			if len(title) > 60 {
				title = title[:57] + "..."
			}
			b.WriteString(fmt.Sprintf("  %s  %s  %s\n",
				statusHighlightStyle.Render(fmt.Sprintf("#%d", p.Number)),
				statusValueStyle.Render(title),
				statusMutedStyle.Render("("+p.Author+")")))
		}
	}

	if len(out.OpenIssues) > 0 {
		empty = false
		b.WriteString("\n")
		b.WriteString(statusHeaderStyle.Render("Open Issues"))
		b.WriteString("\n")
		for _, i := range out.OpenIssues {
			title := i.Title
			if len(title) > 60 {
				title = title[:57] + "..."
			}
			labelStr := ""
			if len(i.Labels) > 0 {
				labelStr = "  " + statusMutedStyle.Render("["+strings.Join(i.Labels, ", ")+"]")
			}
			b.WriteString(fmt.Sprintf("  %s  %s  %s%s\n",
				statusHighlightStyle.Render(fmt.Sprintf("#%d", i.Number)),
				statusValueStyle.Render(title),
				statusMutedStyle.Render("("+i.Author+")"),
				labelStr))
		}
	}

	if empty {
		b.WriteString("\n")
		b.WriteString(statusMutedStyle.Render("  No data — run 'ox code index' to populate"))
		b.WriteString("\n")
	}

	return b.String()
}

func splitAndTrim(s, sep string) []string {
	parts := strings.Split(s, sep)
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

func init() {
	codeInsightsCmd.Flags().Bool("json", false, "structured JSON output for agents")
	codeInsightsCmd.Flags().Int("days", 14, "time window in days")
	codeInsightsCmd.Flags().Int("limit", 10, "max rows per section")
}
