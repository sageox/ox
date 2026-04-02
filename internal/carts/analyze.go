package carts

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/pkg/sessionsummary"
)

// AnalysisOutput is the structured dump for AI consumption.
// Answers: where are we, what's blocking, what we learned, what's next.
type AnalysisOutput struct {
	TimeRange     TimeRange                `json:"time_range"`
	Headline      string                   `json:"headline"`
	Guidance      string                   `json:"guidance"`
	Pyramid       PyramidSummary           `json:"pyramid"`
	Progress      ProgressSummary          `json:"progress"`
	CartsByStatus map[string][]CartSummary `json:"carts_by_status"`
	WorkItems     []WorkItem               `json:"work_items"`
	Blockers      []BlockerInfo            `json:"blockers"`
	Learnings     []Learning               `json:"learnings"`
	NextSteps     []NextStep               `json:"next_steps"`
}

// CartSummary is a compact view of a cart for status-grouped listings.
type CartSummary struct {
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	Assignee string  `json:"assignee"`
	Type     string  `json:"type"`
	Priority int     `json:"priority"`
	Pyramid  Pyramid `json:"pyramid"`
}

type TimeRange struct {
	Since string `json:"since"`
	Until string `json:"until"`
}

type ProgressSummary struct {
	TotalCarts     int            `json:"total_carts"`
	ByStatus       map[string]int `json:"by_status"`
	ByType         map[string]int `json:"by_type"`
	CompletionRate float64        `json:"completion_rate"`
	SessionCount   int            `json:"session_count"`
	MurmurCount    int            `json:"murmur_count"`
}

// WorkItem joins a cart with its session and murmur evidence.
type WorkItem struct {
	Cart    *Issue           `json:"cart"`
	Session *SessionEvidence `json:"session,omitempty"`
	Murmurs []MurmurEvidence `json:"murmurs,omitempty"`
}

// SessionEvidence is the relevant subset of summary.json for analysis.
type SessionEvidence struct {
	DirName       string                        `json:"dir_name"`
	Outcome       string                        `json:"outcome"`
	QualityScore  float64                       `json:"quality_score"`
	Summary       string                        `json:"summary"`
	KeyActions    []string                      `json:"key_actions,omitempty"`
	Decisions     []sessionsummary.Decision     `json:"decisions,omitempty"`
	ActionItems   []sessionsummary.ActionItem   `json:"action_items,omitempty"`
	OpenQuestions []sessionsummary.OpenQuestion `json:"open_questions,omitempty"`
	FilesChanged  []sessionsummary.FileSummary  `json:"files_changed,omitempty"`
	AhaMoments    []sessionsummary.AhaMoment    `json:"aha_moments,omitempty"`
}

type MurmurEvidence struct {
	ID         string `json:"id"`
	Content    string `json:"content"`
	Topic      string `json:"topic"`
	Importance string `json:"importance"`
	Timestamp  string `json:"timestamp"`
}

type BlockerInfo struct {
	CartID    string   `json:"cart_id"`
	CartTitle string   `json:"cart_title"`
	BlockedBy []string `json:"blocked_by_ids"`
}

type Learning struct {
	Highlight string `json:"highlight"`
	Why       string `json:"why"`
	Source    string `json:"source"`
	Who       string `json:"who"`
}

type NextStep struct {
	Task     string `json:"task"`
	Assignee string `json:"assignee,omitempty"`
	Priority string `json:"priority,omitempty"`
	FromCart string `json:"from_cart"`
}

// AnalyzeInput holds everything Analyze needs. The caller decides where carts
// and dependencies come from — DoltDB in production, JSON files in tests.
type AnalyzeInput struct {
	Carts        []*Issue
	Dependencies map[string][]*Dependency // cart ID → deps
	LedgerPath   string
	Since        time.Time
	Until        time.Time
}

// Analyze takes carts + dependencies and enriches them with ledger session/murmur data.
// Caller is responsible for loading carts from whatever source (DoltDB, JSON files, etc).
func Analyze(input AnalyzeInput) (*AnalysisOutput, error) {
	var workItems []WorkItem
	var allLearnings []Learning
	var allNextSteps []NextStep
	sessionCount := 0
	murmurCount := 0

	sessionsDir := filepath.Join(input.LedgerPath, "sessions")

	for _, c := range input.Carts {
		wi := WorkItem{Cart: c}

		// Extract correlation key from cart ID: "cart-Ox1001" → "Ox1001"
		corrKey := strings.TrimPrefix(c.ID, "cart-")

		// Find matching session
		if se := findSessionEvidence(sessionsDir, corrKey); se != nil {
			wi.Session = se
			sessionCount++

			for _, aha := range se.AhaMoments {
				if aha.Type == "insight" || aha.Type == "breakthrough" || aha.Type == "synthesis" {
					allLearnings = append(allLearnings, Learning{
						Highlight: aha.Highlight,
						Why:       aha.Why,
						Source:    c.ID,
						Who:       c.Assignee,
					})
				}
			}

			if c.Status != StatusClosed && se.ActionItems != nil {
				for _, ai := range se.ActionItems {
					allNextSteps = append(allNextSteps, NextStep{
						Task:     ai.Task,
						Assignee: ai.Assignee,
						Priority: ai.Priority,
						FromCart: c.ID,
					})
				}
			}
		}

		// Find matching murmurs by correlation key: "Ox1001" → "Mx1001"
		murmurKey := "Mx" + strings.TrimPrefix(corrKey, "Ox")
		if murmurs := findMurmurEvidence(input.LedgerPath, murmurKey, input.Since, input.Until); len(murmurs) > 0 {
			wi.Murmurs = murmurs
			murmurCount += len(murmurs)
		}

		workItems = append(workItems, wi)
	}

	// Compute progress stats
	byStatus := make(map[string]int)
	byType := make(map[string]int)
	closedCount := 0
	for _, c := range input.Carts {
		byStatus[string(c.Status)]++
		byType[string(c.IssueType)]++
		if c.Status == StatusClosed {
			closedCount++
		}
	}
	completionRate := 0.0
	if len(input.Carts) > 0 {
		completionRate = float64(closedCount) / float64(len(input.Carts))
	}

	// Find blockers from provided dependencies
	var blockers []BlockerInfo
	for _, c := range input.Carts {
		if c.Status == StatusClosed {
			continue
		}
		deps := input.Dependencies[c.ID]
		var blockingIDs []string
		for _, d := range deps {
			if d.Type == DepBlocks {
				blockingIDs = append(blockingIDs, d.DependsOnID)
			}
		}
		if len(blockingIDs) > 0 {
			blockers = append(blockers, BlockerInfo{
				CartID:    c.ID,
				CartTitle: c.Title,
				BlockedBy: blockingIDs,
			})
		}
	}

	// Build grouped cart listing by status with per-cart pyramids
	sessionByCart := make(map[string]*SessionEvidence, len(workItems))
	for _, wi := range workItems {
		if wi.Session != nil {
			sessionByCart[wi.Cart.ID] = wi.Session
		}
	}
	cartsByStatus := buildCartsByStatusWithPyramids(input.Carts, sessionByCart)

	output := &AnalysisOutput{
		TimeRange: TimeRange{
			Since: input.Since.Format(time.RFC3339),
			Until: input.Until.Format(time.RFC3339),
		},
		Progress: ProgressSummary{
			TotalCarts:     len(input.Carts),
			ByStatus:       byStatus,
			ByType:         byType,
			CompletionRate: completionRate,
			SessionCount:   sessionCount,
			MurmurCount:    murmurCount,
		},
		CartsByStatus: cartsByStatus,
		WorkItems:     workItems,
		Blockers:      blockers,
		Learnings:     allLearnings,
		NextSteps:     allNextSteps,
	}

	output.Headline = buildAnalysisHeadline(output)
	output.Guidance = buildAnalysisGuidance(output)
	output.Pyramid = PyramidSummary{
		Overall:  buildOverallPyramid(output),
		ByStatus: buildStatusPyramids(output),
	}

	return output, nil
}

// LoadCartsFromDir reads cart JSON files from a directory (e.g. digital twin output).
func LoadCartsFromDir(cartsDir string) ([]*Issue, error) {
	entries, err := os.ReadDir(cartsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read carts dir: %w", err)
	}

	var issues []*Issue
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(cartsDir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read cart %s: %w", e.Name(), err)
		}

		var raw struct {
			ID          string  `json:"id"`
			Title       string  `json:"title"`
			Description string  `json:"description"`
			Status      string  `json:"status"`
			Priority    int     `json:"priority"`
			IssueType   string  `json:"issue_type"`
			Assignee    string  `json:"assignee"`
			Creator     string  `json:"creator"`
			Source      string  `json:"source"`
			CreatedAt   string  `json:"created_at"`
			ClosedAt    *string `json:"closed_at,omitempty"`
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("parse cart %s: %w", e.Name(), err)
		}

		createdAt, _ := time.Parse(time.RFC3339, raw.CreatedAt)
		issue := &Issue{
			ID:          raw.ID,
			Title:       raw.Title,
			Description: raw.Description,
			Status:      Status(raw.Status),
			Priority:    raw.Priority,
			IssueType:   IssueType(raw.IssueType),
			Assignee:    raw.Assignee,
			Creator:     raw.Creator,
			Source:      raw.Source,
			CreatedAt:   createdAt,
			UpdatedAt:   createdAt,
		}
		if raw.ClosedAt != nil {
			t, _ := time.Parse(time.RFC3339, *raw.ClosedAt)
			issue.ClosedAt = &t
		}
		issues = append(issues, issue)
	}
	return issues, nil
}

// findSessionEvidence searches for a session matching the correlation key.
func findSessionEvidence(sessionsDir, corrKey string) *SessionEvidence {
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return nil
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), "-"+corrKey) {
			continue
		}

		summaryPath := filepath.Join(sessionsDir, e.Name(), "summary.json")
		data, err := os.ReadFile(summaryPath)
		if err != nil {
			return nil
		}

		var summary sessionsummary.SummarizeResponse
		if err := json.Unmarshal(data, &summary); err != nil {
			return nil
		}

		se := &SessionEvidence{
			DirName:      e.Name(),
			Outcome:      summary.Outcome,
			QualityScore: summary.QualityScore,
			Summary:      summary.Summary,
			KeyActions:   summary.KeyActions,
			FilesChanged: summary.FilesChanged,
			AhaMoments:   summary.AhaMoments,
		}
		if summary.AgentSummary != nil {
			se.Decisions = summary.AgentSummary.Decisions
			se.ActionItems = summary.AgentSummary.ActionItems
			se.OpenQuestions = summary.AgentSummary.OpenQuestions
		}
		return se
	}
	return nil
}

// findMurmurEvidence scans ledger murmur directories for murmurs matching the key.
func findMurmurEvidence(ledgerPath, murmurKey string, since, until time.Time) []MurmurEvidence {
	var results []MurmurEvidence
	current := since.Truncate(time.Hour)

	for !current.After(until) {
		dir := filepath.Join(ledgerPath, "data", "murmurs",
			current.Format("2006-01-02"), fmt.Sprintf("%02d", current.Hour()))

		entries, err := os.ReadDir(dir)
		if err != nil {
			current = current.Add(time.Hour)
			continue
		}

		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".json")
			if name != murmurKey {
				continue
			}

			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}

			var m struct {
				ID         string `json:"id"`
				Content    string `json:"content"`
				Topic      string `json:"topic"`
				Importance string `json:"importance"`
				Timestamp  string `json:"timestamp"`
			}
			if err := json.Unmarshal(data, &m); err != nil {
				continue
			}

			results = append(results, MurmurEvidence{
				ID:         m.ID,
				Content:    m.Content,
				Topic:      m.Topic,
				Importance: m.Importance,
				Timestamp:  m.Timestamp,
			})
		}
		current = current.Add(time.Hour)
	}
	return results
}

// buildCartsByStatusWithPyramids groups carts into a status-keyed map of compact summaries,
// each with a per-cart pyramid for multi-resolution scanning.
func buildCartsByStatusWithPyramids(issues []*Issue, sessions map[string]*SessionEvidence) map[string][]CartSummary {
	grouped := make(map[string][]CartSummary)
	for _, c := range issues {
		s := string(c.Status)
		grouped[s] = append(grouped[s], CartSummary{
			ID:       c.ID,
			Title:    c.Title,
			Assignee: c.Assignee,
			Type:     string(c.IssueType),
			Priority: c.Priority,
			Pyramid:  buildCartPyramid(c, sessions[c.ID]),
		})
	}
	return grouped
}

// buildAnalysisHeadline produces a one-line summary of progress.
func buildAnalysisHeadline(o *AnalysisOutput) string {
	p := o.Progress
	if p.TotalCarts == 0 {
		return "No carts found in this time window."
	}

	closed := p.ByStatus["closed"]
	inProgress := p.ByStatus["in_progress"]
	open := p.ByStatus["open"]

	// Count unique assignees
	assignees := make(map[string]bool)
	for _, wi := range o.WorkItems {
		if wi.Cart.Assignee != "" {
			assignees[wi.Cart.Assignee] = true
		}
	}

	parts := []string{fmt.Sprintf("%d/%d carts completed", closed, p.TotalCarts)}

	if inProgress > 0 {
		parts = append(parts, fmt.Sprintf("%d in progress", inProgress))
	}
	if open > 0 {
		parts = append(parts, fmt.Sprintf("%d open", open))
	}
	if len(assignees) > 0 {
		parts = append(parts, fmt.Sprintf("%d coworkers", len(assignees)))
	}
	if len(o.Blockers) > 0 {
		parts = append(parts, fmt.Sprintf("%d blocked", len(o.Blockers)))
	}

	return strings.Join(parts, ", ") + "."
}

// buildAnalysisGuidance produces agent-facing instructions for presenting results.
func buildAnalysisGuidance(o *AnalysisOutput) string {
	p := o.Progress

	var lines []string

	// Pyramid scanning instructions
	lines = append(lines, "Use pyramid summaries for progressive disclosure. Start with pyramid.overall.l4 for the one-line status. Use pyramid.by_status for group-level summaries. Use per-cart pyramids (l2) to scan many carts quickly — only expand to full cart details when the user asks to zoom in.")

	lines = append(lines, "Present the cart listing grouped by status. Lead with in-progress and open carts (the active work), then show completed carts as a summary count with the option to expand.")

	if p.CompletionRate == 1 {
		lines = append(lines, "All carts are completed. Summarize what was accomplished and highlight any learnings or suggested follow-ups.")
	} else if p.CompletionRate >= 0.75 {
		lines = append(lines, fmt.Sprintf("%.0f%% complete. Focus on the remaining in-progress and open carts — what's left to finish.", p.CompletionRate*100))
	} else {
		lines = append(lines, fmt.Sprintf("%.0f%% complete. Highlight what's in progress and any blockers preventing progress.", p.CompletionRate*100))
	}

	if len(o.Blockers) > 0 {
		lines = append(lines, fmt.Sprintf("%d carts are blocked. Surface these prominently — blocked work is the most actionable signal.", len(o.Blockers)))
	}

	if len(o.Learnings) > 0 {
		lines = append(lines, "Include learnings — these are insights from AHA moments in sessions.")
	}

	lines = append(lines, "Keep the tone factual and concise. Use cart titles as-is — they were written by the team.")

	return strings.Join(lines, " ")
}
