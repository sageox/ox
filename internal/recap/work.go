package recap

import (
	"context"
	"strings"
	"time"

	"github.com/sageox/ox/internal/gitutil"
)

// gatherWork builds the "what you did" list: each of the user's in-window
// sessions (newest first), with any commits that carry a SageOx-Session trailer
// resolving back to that session attached as receipts. Git mining is
// best-effort — a non-repo or git error yields sessions without commits, never
// an error.
func gatherWork(ctx context.Context, projectRoot string, since time.Time, sessions []SessionFacts) []WorkItem {
	mine := mineOnly(sessions)
	sortByCreatedDesc(mine)

	items := make([]WorkItem, 0, len(mine))
	byID := map[string]int{}   // session id  -> items index
	byName := map[string]int{} // session name -> items index
	for _, s := range mine {
		if len(items) >= maxWorkItems {
			break
		}
		items = append(items, WorkItem{Session: s.Name, Title: s.Title, When: s.CreatedAt})
		idx := len(items) - 1
		if s.SessionID != "" {
			byID[s.SessionID] = idx
		}
		byName[s.Name] = idx
	}

	for _, c := range mineTrailerCommits(ctx, projectRoot, since) {
		idx, ok := -1, false
		if c.sessionID != "" {
			idx, ok = byID[c.sessionID]
		}
		if !ok && c.sessionName != "" {
			idx, ok = byName[c.sessionName]
		}
		if ok {
			items[idx].Commits = append(items[idx].Commits, c.short()+" "+c.subject)
		}
	}
	return items
}

// trailerCommit is a commit carrying a SageOx-Session trailer, with the session
// reference already extracted.
type trailerCommit struct {
	sha         string
	subject     string
	sessionID   string
	sessionName string
}

func (c trailerCommit) short() string {
	if len(c.sha) > 7 {
		return c.sha[:7]
	}
	return c.sha
}

// mineTrailerCommits runs one git log over the window for commits carrying a
// SageOx-Session trailer and parses each into a trailerCommit.
func mineTrailerCommits(ctx context.Context, projectRoot string, since time.Time) []trailerCommit {
	if projectRoot == "" {
		return nil
	}
	out, err := gitutil.RunGit(ctx, projectRoot,
		"log",
		"--since="+since.Format(time.RFC3339),
		"--grep=SageOx-Session:",
		"--format=%H%x1f%s%x1f%(trailers:key=SageOx-Session,valueonly,separator=%x1e)",
	)
	if err != nil {
		return nil
	}

	var commits []trailerCommit
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\x1f")
		if len(fields) < 3 {
			continue
		}
		id, name := parseSessionTrailer(fields[2])
		if id == "" && name == "" {
			continue
		}
		commits = append(commits, trailerCommit{
			sha:         fields[0],
			subject:     fields[1],
			sessionID:   id,
			sessionName: name,
		})
	}
	return commits
}

// parseSessionTrailer extracts the session id or session name from a
// SageOx-Session trailer value, tolerating both URL forms:
//
//	<endpoint>/c/<ses_id>                                   → session id
//	<endpoint>/repo/<repo_id>/sessions/<name>/view          → session name
//
// Multiple trailer values may be joined by the record separator; the first
// resolvable one wins.
func parseSessionTrailer(value string) (sessionID, sessionName string) {
	for _, v := range strings.Split(value, "\x1e") {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if i := strings.Index(v, "/c/"); i >= 0 {
			id := strings.Trim(v[i+len("/c/"):], "/")
			if id != "" {
				return id, ""
			}
		}
		if i := strings.Index(v, "/sessions/"); i >= 0 {
			rest := v[i+len("/sessions/"):]
			rest = strings.TrimSuffix(strings.Trim(rest, "/"), "/view")
			rest = strings.TrimSuffix(rest, "/")
			if rest != "" && !strings.Contains(rest, "/") {
				return "", rest
			}
		}
	}
	return "", ""
}
