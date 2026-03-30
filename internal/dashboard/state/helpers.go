package state

import (
	"fmt"
	"sort"
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

	return nodes
}

// ActivityEntries derives the timeline entry list from raw store data.
// Murmur entries appear first (sorted newest-first among themselves), followed
// by session and workspace sync events (also newest-first). This ordering puts
// the live "team pulse" at the top of the feed.
func ActivityEntries(s *Store) []domain.TimelineEntry {
	var murmurEntries []domain.TimelineEntry
	var otherEntries []domain.TimelineEntry

	// Murmur entries — lead the feed so the live team pulse is always visible.
	for i := range s.Murmurs {
		m := s.Murmurs[i]
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
