package state

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/dashboard/domain"
)

// BuildNav derives the flat, display-order navigation node list from raw store
// data. Sections are always present; child rows are added for each available
// data item. This is the single source of truth for nav tree structure so the
// cursor logic in the root model stays in sync.
func BuildNav(s *Store) []domain.NavNode {
	var nodes []domain.NavNode

	daemonOffline := s.DaemonStatus == nil || !s.DaemonStatus.Running

	// Sessions section.
	nodes = append(nodes, domain.NavNode{
		ID:         "section-sessions",
		Kind:       domain.NavNodeSection,
		Label:      "Sessions",
		Depth:      0,
		Expandable: true,
		Expanded:   true,
	})
	for i := range s.Sessions {
		sess := s.Sessions[i]
		label := sess.Username
		if label == "" {
			label = sess.Filename
		}
		nodes = append(nodes, domain.NavNode{
			ID:    "session-" + sess.Filename,
			Kind:  domain.NavNodeSession,
			Label: label,
			Depth: 1,
			Target: &domain.InspectorTarget{
				Kind:    domain.TargetSession,
				Session: &s.Sessions[i],
			},
		})
	}
	if len(s.Sessions) == 0 {
		hint := "no sessions yet"
		if daemonOffline {
			hint = "daemon offline — sessions unavailable"
		}
		nodes = append(nodes, domain.NavNode{
			ID:    "hint-sessions",
			Kind:  domain.NavNodeHint,
			Label: hint,
			Depth: 1,
		})
	}

	// Workspaces section — populated from daemon status when available.
	nodes = append(nodes, domain.NavNode{
		ID:         "section-workspaces",
		Kind:       domain.NavNodeSection,
		Label:      "Workspaces",
		Depth:      0,
		Expandable: true,
		Expanded:   true,
	})
	wsCount := 0
	if s.DaemonStatus != nil {
		for _, wsList := range s.DaemonStatus.Workspaces {
			for i := range wsList {
				ws := wsList[i]
				label := ws.TeamName
				if label == "" {
					label = ws.ID
				}
				wsCopy := ws
				nodes = append(nodes, domain.NavNode{
					ID:    "workspace-" + ws.ID,
					Kind:  domain.NavNodeWorkspace,
					Label: label,
					Depth: 1,
					Target: &domain.InspectorTarget{
						Kind:      domain.TargetWorkspace,
						Workspace: &wsCopy,
					},
				})
				wsCount++
			}
		}
	}
	if wsCount == 0 {
		nodes = append(nodes, domain.NavNode{
			ID:    "hint-workspaces",
			Kind:  domain.NavNodeHint,
			Label: "no workspaces synced",
			Depth: 1,
		})
	}

	// Murmurs section.
	nodes = append(nodes, domain.NavNode{
		ID:         "section-murmurs",
		Kind:       domain.NavNodeSection,
		Label:      "Murmurs",
		Depth:      0,
		Expandable: true,
		Expanded:   true,
	})
	for i := range s.Murmurs {
		m := s.Murmurs[i]
		nodes = append(nodes, domain.NavNode{
			ID:    fmt.Sprintf("murmur-%s-%s", m.AgentID, m.Topic),
			Kind:  domain.NavNodeMurmur,
			Label: m.Topic,
			Depth: 1,
			Target: &domain.InspectorTarget{
				Kind:   domain.TargetMurmur,
				Murmur: &s.Murmurs[i],
			},
		})
	}
	if len(s.Murmurs) == 0 {
		nodes = append(nodes, domain.NavNode{
			ID:    "hint-murmurs",
			Kind:  domain.NavNodeHint,
			Label: "no recent murmurs",
			Depth: 1,
		})
	}

	// Issues section — populated from daemon status when available.
	nodes = append(nodes, domain.NavNode{
		ID:         "section-issues",
		Kind:       domain.NavNodeSection,
		Label:      "Issues",
		Depth:      0,
		Expandable: true,
		Expanded:   true,
	})
	issueCount := 0
	if s.DaemonStatus != nil {
		for i := range s.DaemonStatus.Issues {
			issue := &s.DaemonStatus.Issues[i]
			severity := issue.Severity
			label := fmt.Sprintf("[%s] %s", severity, issue.Type)
			if issue.Repo != "" {
				label = fmt.Sprintf("[%s] %s (%s)", severity, issue.Type, issue.Repo)
			}
			nodes = append(nodes, domain.NavNode{
				ID:    fmt.Sprintf("issue-%d", i),
				Kind:  domain.NavNodeIssue,
				Label: label,
				Depth: 1,
				Target: &domain.InspectorTarget{
					Kind:  domain.TargetIssue,
					Issue: issue,
				},
			})
			issueCount++
		}
	}
	if issueCount == 0 {
		hint := "no issues"
		if daemonOffline {
			hint = "daemon offline"
		}
		nodes = append(nodes, domain.NavNode{
			ID:    "hint-issues",
			Kind:  domain.NavNodeHint,
			Label: hint,
			Depth: 1,
		})
	}

	// Sync Health section — top-level node that opens sync health inspector.
	if s.DaemonStatus != nil {
		nodes = append(nodes, domain.NavNode{
			ID:         "section-sync",
			Kind:       domain.NavNodeSection,
			Label:      "Sync Health",
			Depth:      0,
			Expandable: false,
			Target: &domain.InspectorTarget{
				Kind:       domain.TargetSyncHealth,
				SyncHealth: s.DaemonStatus,
			},
		})
		// Per-workspace sync health rows.
		for _, wsList := range s.DaemonStatus.Workspaces {
			for i := range wsList {
				ws := wsList[i]
				label := ws.TeamName
				if label == "" {
					label = ws.ID
				}
				age := "—"
				if !ws.LastSync.IsZero() {
					age = syncAgeLabel(time.Since(ws.LastSync))
				}
				statusLabel := label + " · " + age
				if ws.Syncing {
					statusLabel = "⟳ " + statusLabel
				}
				if ws.LastErr != "" {
					statusLabel = "✗ " + label
				}
				wsCopy := ws
				nodes = append(nodes, domain.NavNode{
					ID:    "sync-ws-" + ws.ID,
					Kind:  domain.NavNodeSyncHealth,
					Label: statusLabel,
					Depth: 1,
					Target: &domain.InspectorTarget{
						Kind:      domain.TargetWorkspace,
						Workspace: &wsCopy,
					},
				})
			}
		}
	}

	// Code Index section — single node showing codedb stats.
	if s.DaemonStatus != nil && s.DaemonStatus.CodeDB != nil {
		cdb := s.DaemonStatus.CodeDB
		indexLabel := "Code Index"
		if cdb.IndexingNow {
			indexLabel = "⟳ Code Index"
		} else if cdb.LastError != "" {
			indexLabel = "✗ Code Index"
		}
		nodes = append(nodes, domain.NavNode{
			ID:    "node-codeindex",
			Kind:  domain.NavNodeCodeIndex,
			Label: indexLabel,
			Depth: 0,
			Target: &domain.InspectorTarget{
				Kind:   domain.TargetCodeDB,
				CodeDB: cdb,
			},
		})
	}

	// Auth section — single node showing authentication status.
	authLabel := "Auth"
	if s.DaemonStatus != nil && s.DaemonStatus.AuthenticatedUser != nil {
		authLabel = "Auth · " + s.DaemonStatus.AuthenticatedUser.Email
	}
	if hasAuthIssue(s) {
		authLabel = "⚠ " + authLabel
	}
	authTarget := &domain.InspectorTarget{
		Kind: domain.TargetAuth,
		Auth: s.DaemonStatus,
	}
	nodes = append(nodes, domain.NavNode{
		ID:     "node-auth",
		Kind:   domain.NavNodeAuth,
		Label:  authLabel,
		Depth:  0,
		Target: authTarget,
	})

	// SOUL.md nodes — one per team-context workspace that has SOUL.md.
	if s.DaemonStatus != nil {
		for _, wsList := range s.DaemonStatus.Workspaces {
			for _, ws := range wsList {
				if ws.Type != "team-context" || !ws.Exists || ws.Path == "" {
					continue
				}
				soulPath := ws.Path + "/SOUL.md"
				content, err := readFileSafe(soulPath)
				if err != nil || content == "" {
					continue
				}
				teamLabel := ws.TeamName
				if teamLabel == "" {
					teamLabel = ws.TeamSlug
				}
				if teamLabel == "" {
					teamLabel = ws.ID
				}
				soulDoc := &domain.SOULDocument{
					TeamName: teamLabel,
					TeamSlug: ws.TeamSlug,
					Path:     soulPath,
					Content:  content,
				}
				nodes = append(nodes, domain.NavNode{
					ID:    "soul-" + ws.ID,
					Kind:  domain.NavNodeSOUL,
					Label: "SOUL · " + teamLabel,
					Depth: 0,
					Target: &domain.InspectorTarget{
						Kind: domain.TargetSOUL,
						SOUL: soulDoc,
					},
				})
			}
		}
	}

	return nodes
}

// hasAuthIssue reports whether the daemon has reported an auth-related issue.
func hasAuthIssue(s *Store) bool {
	if s.DaemonStatus == nil {
		return false
	}
	for _, issue := range s.DaemonStatus.Issues {
		if issue.Type == "auth_expiring" || issue.Type == "auth_expired" {
			return true
		}
	}
	return false
}

// syncAgeLabel returns a short human-readable age string colored by freshness.
func syncAgeLabel(age time.Duration) string {
	switch {
	case age < 5*time.Minute:
		return "just now"
	case age < time.Hour:
		return fmt.Sprintf("%dm ago", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(age.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(age.Hours()/24))
	}
}

// readFileSafe reads a file and returns its content, returning empty string on error.
func readFileSafe(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ActivityEntries derives the timeline entry list from raw store data.
// Murmur entries appear first (sorted newest-first among themselves), followed
// by session and workspace sync events (also newest-first). This ordering puts
// the live "team pulse" at the top of the feed. Active topic filter and search
// query are applied to murmur entries.
func ActivityEntries(s *Store) []domain.TimelineEntry {
	var murmurEntries []domain.TimelineEntry
	var otherEntries []domain.TimelineEntry

	// Murmur entries — lead the feed so the live team pulse is always visible.
	// Apply topic filter and inline search query when set.
	filterTopic := s.MurmurTopic
	searchQuery := strings.ToLower(s.MurmurQuery)

	for i := range s.Murmurs {
		m := s.Murmurs[i]
		// Topic filter: skip if a specific topic is selected and doesn't match.
		if filterTopic != domain.MurmurFilterAll &&
			!strings.EqualFold(m.Topic, string(filterTopic)) {
			continue
		}
		// Search filter: skip if query doesn't appear in content, topic, or author.
		if searchQuery != "" {
			haystack := strings.ToLower(m.Content + " " + m.Topic + " " + m.Author)
			if !strings.Contains(haystack, searchQuery) {
				continue
			}
		}
		murmurEntries = append(murmurEntries, domain.TimelineEntry{
			ID:        fmt.Sprintf("murmur-%s-%s", m.AgentID, m.Topic),
			Kind:      domain.TimelineMurmur,
			Actor:     m.Author,
			Summary:   fmt.Sprintf("[%s] %s", m.Topic, m.Content),
			Timestamp: m.Timestamp,
			Target: &domain.InspectorTarget{
				Kind:   domain.TargetMurmur,
				Murmur: &s.Murmurs[i],
			},
		})
	}
	sort.Slice(murmurEntries, func(i, j int) bool {
		return murmurEntries[i].Timestamp.After(murmurEntries[j].Timestamp)
	})

	// Session entries.
	for i := range s.Sessions {
		sess := s.Sessions[i]
		label := sess.Username
		if label == "" {
			label = sess.AgentID
		}
		summary := sess.Summary
		if summary == "" {
			summary = sess.Filename
		}
		otherEntries = append(otherEntries, domain.TimelineEntry{
			ID:      "session-" + sess.Filename,
			Kind:    domain.TimelineSession,
			Actor:   label,
			Summary: summary,
			// Sessions are sorted newest-first by the store already; ModTime
			// is the best proxy for "when this session was active."
			Timestamp: sess.ModTime,
			Target: &domain.InspectorTarget{
				Kind:    domain.TargetSession,
				Session: &s.Sessions[i],
			},
		})
	}

	// Workspace sync entries from daemon status.
	if s.DaemonStatus != nil {
		for wsType, wsList := range s.DaemonStatus.Workspaces {
			for i := range wsList {
				ws := wsList[i]
				if ws.LastSync.IsZero() {
					continue
				}
				label := ws.TeamName
				if label == "" {
					label = ws.ID
				}
				detail := fmt.Sprintf("%s sync", wsType)
				if ws.LastErr != "" {
					detail += " (" + ws.LastErr + ")"
				}
				wsCopy := ws
				otherEntries = append(otherEntries, domain.TimelineEntry{
					ID:        "ws-" + ws.ID,
					Kind:      domain.TimelineSync,
					Actor:     label,
					Summary:   detail,
					Timestamp: ws.LastSync,
					Target: &domain.InspectorTarget{
						Kind:      domain.TargetWorkspace,
						Workspace: &wsCopy,
					},
				})
			}
		}
	}

	sort.Slice(otherEntries, func(i, j int) bool {
		return otherEntries[i].Timestamp.After(otherEntries[j].Timestamp)
	})

	return append(murmurEntries, otherEntries...)
}

// ActiveMurmurCoworkers returns the count of unique AgentIDs that have posted a
// murmur within the last 30 minutes. Used by the Team Pulse header.
func ActiveMurmurCoworkers(s *Store) int {
	cutoff := time.Now().Add(-30 * time.Minute)
	seen := make(map[string]struct{})
	for _, m := range s.Murmurs {
		if m.Timestamp.After(cutoff) {
			seen[m.AgentID] = struct{}{}
		}
	}
	return len(seen)
}

// ActiveMurmurTeams returns the count of unique team slugs represented by
// murmurs in the last 30 minutes. Team slug is derived from the murmur Author
// field (format: "team-slug/agent-id" or just author name). Falls back to
// counting unique authors when no slash is present.
func ActiveMurmurTeams(s *Store) int {
	cutoff := time.Now().Add(-30 * time.Minute)
	seen := make(map[string]struct{})
	for _, m := range s.Murmurs {
		if !m.Timestamp.After(cutoff) {
			continue
		}
		// Use AgentID prefix up to "/" as the team identifier when available;
		// fall back to Author so the count is always non-zero for active murmurs.
		slug := m.Author
		if idx := indexByte(m.AgentID, '/'); idx >= 0 {
			slug = m.AgentID[:idx]
		}
		seen[slug] = struct{}{}
	}
	return len(seen)
}

// indexByte returns the index of the first occurrence of b in s, or -1.
func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// AllActivityTimestamps returns merged activity timestamps from the daemon's
// ActivitySummary. Used by sparkline renderers in the status bar.
func AllActivityTimestamps(s *Store) []time.Time {
	if s.DaemonStatus == nil || s.DaemonStatus.Activity == nil {
		return nil
	}
	activity := s.DaemonStatus.Activity
	var all []time.Time
	for _, e := range activity.Repos {
		all = append(all, e.Timestamps...)
	}
	for _, e := range activity.Teams {
		all = append(all, e.Timestamps...)
	}
	for _, e := range activity.Workspaces {
		all = append(all, e.Timestamps...)
	}
	for _, e := range activity.Agents {
		all = append(all, e.Timestamps...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Before(all[j]) })
	return all
}

// RecentSessions returns timeline entries for the n most recently modified
// sessions. Sessions are already sorted newest-first by the store.
func RecentSessions(s *Store, n int) []domain.TimelineEntry {
	if n > len(s.Sessions) {
		n = len(s.Sessions)
	}
	entries := make([]domain.TimelineEntry, 0, n)
	for i := 0; i < n; i++ {
		sess := s.Sessions[i]
		label := sess.Username
		if label == "" {
			label = sess.AgentID
		}
		summary := sess.Summary
		if summary == "" {
			summary = sess.Filename
		}
		entries = append(entries, domain.TimelineEntry{
			ID:        "session-" + sess.Filename,
			Kind:      domain.TimelineSession,
			Actor:     label,
			Summary:   summary,
			Timestamp: sess.ModTime,
			Target: &domain.InspectorTarget{
				Kind:    domain.TargetSession,
				Session: &s.Sessions[i],
			},
		})
	}
	return entries
}

// DaemonHealthLevel maps daemon status data to the dashboard health level.
// Returns HealthUnknown when no daemon data is available yet.
func DaemonHealthLevel(s *Store) domain.HealthLevel {
	if s.DaemonStatus == nil {
		return domain.HealthUnknown
	}
	if !s.DaemonStatus.Running {
		return domain.HealthError
	}
	if len(s.DaemonStatus.Issues) == 0 {
		return domain.HealthOK
	}
	for _, issue := range s.DaemonStatus.Issues {
		switch issue.Severity {
		case "critical", "error":
			return domain.HealthError
		}
	}
	return domain.HealthWarn
}
