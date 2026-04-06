package state

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sageox/ox/internal/daemon"
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
		Label:      fmt.Sprintf("Sessions (%d)", len(s.Sessions)),
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
		hint := "start a session to see recordings here"
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
	// Count first so the section header can display the total.
	wsCount := 0
	if s.DaemonStatus != nil {
		for _, wsList := range s.DaemonStatus.Workspaces {
			wsCount += len(wsList)
		}
	}
	// Sort workspace map keys for stable iteration order across renders.
	var wsRepoIDs []string
	if s.DaemonStatus != nil {
		wsRepoIDs = make([]string, 0, len(s.DaemonStatus.Workspaces))
		for id := range s.DaemonStatus.Workspaces {
			wsRepoIDs = append(wsRepoIDs, id)
		}
		sort.Strings(wsRepoIDs)
	}
	nodes = append(nodes, domain.NavNode{
		ID:         "section-workspaces",
		Kind:       domain.NavNodeSection,
		Label:      fmt.Sprintf("Workspaces (%d)", wsCount),
		Depth:      0,
		Expandable: true,
		Expanded:   true,
	})
	if s.DaemonStatus != nil {
		for _, repoID := range wsRepoIDs {
			wsList := s.DaemonStatus.Workspaces[repoID]
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
	if wsCount == 0 {
		nodes = append(nodes, domain.NavNode{
			ID:    "hint-workspaces",
			Kind:  domain.NavNodeHint,
			Label: "run ox init in a repo to sync ledgers",
			Depth: 1,
		})
	}

	// Murmurs section.
	nodes = append(nodes, domain.NavNode{
		ID:         "section-murmurs",
		Kind:       domain.NavNodeSection,
		Label:      fmt.Sprintf("Murmurs (%d)", len(s.Murmurs)),
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
			Label: "murmurs appear here when AI coworkers share WIP",
			Depth: 1,
		})
	}

	// Team Discussions section — memory/*.md files from team context paths.
	nodes = append(nodes, domain.NavNode{
		ID:         "section-discussions",
		Kind:       domain.NavNodeSection,
		Label:      fmt.Sprintf("Team Discussions (%d)", len(s.Discussions)),
		Depth:      0,
		Expandable: true,
		Expanded:   true,
	})
	for i := range s.Discussions {
		d := s.Discussions[i]
		nodes = append(nodes, domain.NavNode{
			ID:    "discussion-" + d.Path,
			Kind:  domain.NavNodeDiscussion,
			Label: d.Title,
			Depth: 1,
			Target: &domain.InspectorTarget{
				Kind:       domain.TargetTeamDiscussion,
				Discussion: &s.Discussions[i],
			},
		})
	}
	if len(s.Discussions) == 0 {
		nodes = append(nodes, domain.NavNode{
			ID:    "hint-discussions",
			Kind:  domain.NavNodeHint,
			Label: "no discussions — record a team meeting",
			Depth: 1,
		})
	}

	// AI Coworkers section — active AI coworker instances from the daemon.
	nodes = append(nodes, domain.NavNode{
		ID:         "section-ai-coworkers",
		Kind:       domain.NavNodeSection,
		Label:      fmt.Sprintf("AI Coworkers (%d)", len(s.Instances)),
		Depth:      0,
		Expandable: true,
		Expanded:   true,
	})
	// Build parent → children map for tree rendering.
	parentMap := make(map[string][]int)
	for i, inst := range s.Instances {
		parentMap[inst.ParentAgentID] = append(parentMap[inst.ParentAgentID], i)
	}
	for _, idx := range parentMap[""] {
		inst := s.Instances[idx]
		instCopy := inst
		label := inst.AgentID
		if inst.AgentType != "" {
			label = inst.AgentType + " · " + inst.AgentID
		}
		icon := "●"
		if inst.Status == "idle" {
			icon = "◌"
		}
		nodes = append(nodes, domain.NavNode{
			ID:    "ai-coworker-" + inst.AgentID,
			Kind:  domain.NavNodeAICoworker,
			Label: icon + " " + label,
			Depth: 1,
			Target: &domain.InspectorTarget{
				Kind:     domain.TargetInstance,
				Instance: &instCopy,
			},
		})
		for _, childIdx := range parentMap[inst.AgentID] {
			child := s.Instances[childIdx]
			childCopy := child
			childLabel := child.AgentID
			if child.AgentType != "" {
				childLabel = child.AgentType + " · " + child.AgentID
			}
			childIcon := "●"
			if child.Status == "idle" {
				childIcon = "◌"
			}
			nodes = append(nodes, domain.NavNode{
				ID:    "ai-coworker-" + child.AgentID,
				Kind:  domain.NavNodeAICoworker,
				Label: childIcon + " " + childLabel,
				Depth: 2,
				Target: &domain.InspectorTarget{
					Kind:     domain.TargetInstance,
					Instance: &childCopy,
				},
			})
		}
	}
	if len(s.Instances) == 0 {
		hint := "no active coworkers right now"
		if daemonOffline {
			hint = "daemon offline — coworkers unavailable"
		}
		nodes = append(nodes, domain.NavNode{
			ID:    "hint-ai-coworkers",
			Kind:  domain.NavNodeHint,
			Label: hint,
			Depth: 1,
		})
	}

	// Issues section — populated from daemon status when available.
	// Count first so the section header can display the total.
	issueCount := 0
	if s.DaemonStatus != nil {
		issueCount = len(s.DaemonStatus.Issues)
	}
	nodes = append(nodes, domain.NavNode{
		ID:         "section-issues",
		Kind:       domain.NavNodeSection,
		Label:      fmt.Sprintf("Issues (%d)", issueCount),
		Depth:      0,
		Expandable: true,
		Expanded:   true,
	})
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
		}
	}
	if issueCount == 0 {
		hint := "✓ no issues detected"
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
		for _, repoID := range wsRepoIDs {
			wsList := s.DaemonStatus.Workspaces[repoID]
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

	// Daemon section — operational visibility: agent work queue, callers, errors.
	if s.DaemonStatus != nil && s.DaemonStatus.Running {
		nodes = append(nodes, domain.NavNode{
			ID:         "section-daemon",
			Kind:       domain.NavNodeSection,
			Label:      "Daemon",
			Depth:      0,
			Expandable: true,
			Expanded:   true,
		})

		// Agent Work sub-node.
		if s.DaemonStatus.AgentWork != nil {
			aw := s.DaemonStatus.AgentWork
			awLabel := fmt.Sprintf("Agent Work · queue:%d active:%d", aw.QueueDepth, len(aw.Active))
			nodes = append(nodes, domain.NavNode{
				ID:    "daemon-agentwork",
				Kind:  domain.NavNodeAgentWork,
				Label: awLabel,
				Depth: 1,
				Target: &domain.InspectorTarget{
					Kind:      domain.TargetAgentWork,
					AgentWork: s.DaemonStatus.AgentWork,
				},
			})
		}

		// Callers sub-node.
		if len(s.DaemonStatus.Callers) > 0 {
			callersCopy := make([]daemon.CallerInfo, len(s.DaemonStatus.Callers))
			copy(callersCopy, s.DaemonStatus.Callers)
			nodes = append(nodes, domain.NavNode{
				ID:    "daemon-callers",
				Kind:  domain.NavNodeCallers,
				Label: fmt.Sprintf("Callers · %d connected", len(s.DaemonStatus.Callers)),
				Depth: 1,
				Target: &domain.InspectorTarget{
					Kind:    domain.TargetCallers,
					Callers: callersCopy,
				},
			})
		}

		// Stored Errors sub-node.
		errorLabel := "Errors"
		if len(s.StoredErrors) > 0 {
			errorLabel = fmt.Sprintf("Errors · %d unviewed", len(s.StoredErrors))
		} else if s.DaemonStatus.UnviewedErrorCount > 0 {
			errorLabel = fmt.Sprintf("Errors · %d unviewed", s.DaemonStatus.UnviewedErrorCount)
		}
		nodes = append(nodes, domain.NavNode{
			ID:    "daemon-errors",
			Kind:  domain.NavNodeDaemonErrors,
			Label: errorLabel,
			Depth: 1,
			Target: &domain.InspectorTarget{
				Kind:         domain.TargetDaemonErrors,
				StoredErrors: s.StoredErrors,
			},
		})
	}

	// Code Index section — single node showing codedb stats.
	// Shows daemon-reported stats when available; falls back to disk-loaded stats.
	if cdb := codeIndexStats(s); cdb != nil {
		indexLabel := "Code Index"
		if cdb.IndexingNow {
			indexLabel = "⟳ Code Index"
		} else if cdb.LastError != "" {
			indexLabel = "✗ Code Index"
		} else if !cdb.IndexExists {
			indexLabel = "○ Code Index"
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

	// SOUL.md nodes — one per team-context that has pre-loaded SOULPreview.
	// Uses data already fetched by ListTeamContexts to avoid file I/O in render.
	for _, tc := range s.TeamContexts {
		if tc.SOULPreview == "" {
			continue
		}
		teamLabel := tc.TeamName
		if teamLabel == "" {
			teamLabel = tc.TeamSlug
		}
		if teamLabel == "" {
			teamLabel = tc.Path
		}
		soulDoc := &domain.SOULDocument{
			TeamName: teamLabel,
			TeamSlug: tc.TeamSlug,
			Path:     tc.Path + "/SOUL.md",
			Content:  tc.SOULPreview,
		}
		nodes = append(nodes, domain.NavNode{
			ID:    "soul-" + tc.TeamSlug,
			Kind:  domain.NavNodeSOUL,
			Label: "SOUL · " + teamLabel,
			Depth: 0,
			Target: &domain.InspectorTarget{
				Kind: domain.TargetSOUL,
				SOUL: soulDoc,
			},
		})
	}

	// Team Feed section — combined murmurs + discussions sorted newest-first.
	// Provides a single scannable view of live WIP signals and shared knowledge.
	{
		type feedItem struct {
			label     string
			timestamp time.Time
			target    domain.InspectorTarget
		}
		var items []feedItem
		for i := range s.Murmurs {
			m := s.Murmurs[i]
			items = append(items, feedItem{
				label:     fmt.Sprintf("[%s] %s", m.Topic, truncate(m.Content, 40)),
				timestamp: m.Timestamp,
				target:    domain.InspectorTarget{Kind: domain.TargetMurmur, Murmur: &s.Murmurs[i]},
			})
		}
		for i := range s.Discussions {
			d := s.Discussions[i]
			items = append(items, feedItem{
				label:     d.Title,
				timestamp: d.ModTime,
				target:    domain.InspectorTarget{Kind: domain.TargetTeamDiscussion, Discussion: &s.Discussions[i]},
			})
		}
		sort.Slice(items, func(i, j int) bool {
			return items[i].timestamp.After(items[j].timestamp)
		})
		if len(items) > 20 {
			items = items[:20]
		}

		if len(items) > 0 {
			nodes = append(nodes, domain.NavNode{
				ID:         "section-team-feed",
				Kind:       domain.NavNodeSection,
				Label:      "Team Feed",
				Depth:      0,
				Expandable: true,
				Expanded:   true,
			})
			for i := range items {
				item := items[i]
				itemCopy := item.target
				nodes = append(nodes, domain.NavNode{
					ID:    fmt.Sprintf("feed-%d-%d", i, item.timestamp.Unix()),
					Kind:  domain.NavNodeTeamFeedSection,
					Label: item.label,
					Depth: 1,
					Target: &itemCopy,
				})
			}
		}
	}

	// Whisper history section — recent whispers delivered to AI coworkers.
	if len(s.WhisperHistory) > 0 {
		nodes = append(nodes, domain.NavNode{
			ID:         "section-whispers",
			Kind:       domain.NavNodeSection,
			Label:      "Whispers",
			Depth:      0,
			Expandable: true,
			Expanded:   true,
		})
		// Show at most 10 most recent whispers in the nav.
		shown := s.WhisperHistory
		if len(shown) > 10 {
			shown = shown[:10]
		}
		for i := range shown {
			w := shown[i]
			icon := "◦"
			if w.Delivered {
				icon = "✓"
			}
			label := fmt.Sprintf("%s %s", icon, truncate(w.Content, 45))
			wCopy := []domain.WhisperHistoryEntry{w}
			nodes = append(nodes, domain.NavNode{
				ID:    fmt.Sprintf("whisper-%d", i),
				Kind:  domain.NavNodeWhisper,
				Label: label,
				Depth: 1,
				Target: &domain.InspectorTarget{
					Kind:           domain.TargetWhisperHistory,
					WhisperHistory: wCopy,
				},
			})
		}
	}

	// Team Knowledge section — browse team contexts (SOUL, memory, docs).
	if len(s.TeamContexts) > 0 {
		nodes = append(nodes, domain.NavNode{
			ID:         "section-team-knowledge",
			Kind:       domain.NavNodeSection,
			Label:      "Team Knowledge",
			Depth:      0,
			Expandable: true,
			Expanded:   true,
		})
		for i := range s.TeamContexts {
			tc := s.TeamContexts[i]
			label := tc.TeamName
			if tc.TeamName == "" {
				label = tc.TeamSlug
			}
			if tc.MemoryCount > 0 || tc.DocsCount > 0 {
				label = fmt.Sprintf("%s  mem:%d docs:%d", label, tc.MemoryCount, tc.DocsCount)
			}
			nodes = append(nodes, domain.NavNode{
				ID:    "team-ctx-" + tc.TeamSlug,
				Kind:  domain.NavNodeTeamContext,
				Label: label,
				Depth: 1,
				Target: &domain.InspectorTarget{
					Kind:        domain.TargetTeamContext,
					TeamContext: &s.TeamContexts[i],
				},
			})
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

// codeIndexStats returns the best available code index stats.
// Prefers daemon-reported stats; falls back to disk-loaded stats when daemon offline.
func codeIndexStats(s *Store) *daemon.CodeDBStats {
	if s.DaemonStatus != nil && s.DaemonStatus.CodeDB != nil {
		return s.DaemonStatus.CodeDB
	}
	return s.CodeIndexStats
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
		activityRepoIDs := make([]string, 0, len(s.DaemonStatus.Workspaces))
		for id := range s.DaemonStatus.Workspaces {
			activityRepoIDs = append(activityRepoIDs, id)
		}
		sort.Strings(activityRepoIDs)
		for _, wsType := range activityRepoIDs {
			wsList := s.DaemonStatus.Workspaces[wsType]
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

	// Whisper history entries.
	for i := range s.WhisperHistory {
		w := s.WhisperHistory[i]
		icon := "◦"
		if w.Delivered {
			icon = "✓"
		}
		wCopy := []domain.WhisperHistoryEntry{w}
		otherEntries = append(otherEntries, domain.TimelineEntry{
			ID:      fmt.Sprintf("whisper-%d", i),
			Kind:    domain.TimelineAgent,
			Actor:   w.AgentID,
			Summary: fmt.Sprintf("%s [whisper] %s", icon, truncate(w.Content, 60)),
			Timestamp: w.CreatedAt,
			Target: &domain.InspectorTarget{
				Kind:           domain.TargetWhisperHistory,
				WhisperHistory: wCopy,
			},
		})
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

// truncate clips s to at most n runes, appending "…" when trimmed.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n-1]) + "…"
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
