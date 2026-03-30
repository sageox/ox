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

	// Workspaces section — populated from daemon status when available.
	nodes = append(nodes, domain.NavNode{
		ID:         "section-workspaces",
		Kind:       domain.NavNodeSection,
		Label:      "Workspaces",
		Depth:      0,
		Expandable: true,
		Expanded:   true,
	})
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
			}
		}
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

	return nodes
}

// ActivityEntries derives the timeline entry list from raw store data.
// Entries are ordered newest first.
func ActivityEntries(s *Store) []domain.TimelineEntry {
	var entries []domain.TimelineEntry

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
		entries = append(entries, domain.TimelineEntry{
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
				entries = append(entries, domain.TimelineEntry{
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

	// Murmur entries.
	for i := range s.Murmurs {
		m := s.Murmurs[i]
		entries = append(entries, domain.TimelineEntry{
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

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Timestamp.After(entries[j].Timestamp)
	})
	return entries
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
