package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/session"
	sessionhtml "github.com/sageox/ox/internal/session/html"
)

// generateHTML creates an HTML file from a stored session.
func generateHTML(t *session.StoredSession, outputPath string) error {
	// try to load summary.json from the same directory
	var summary *session.SummarizeResponse
	summaryPath := filepath.Join(filepath.Dir(t.Info.FilePath), "summary.json")
	if data, err := os.ReadFile(summaryPath); err == nil {
		var s session.SummarizeResponse
		if json.Unmarshal(data, &s) == nil {
			summary = &s
		}
	}

	gen, err := sessionhtml.NewGenerator()
	if err != nil {
		return fmt.Errorf("create generator: %w", err)
	}

	// build template data via the generator's exported helper so we can
	// enrich summary.json with computed chapter/file data
	data := sessionhtml.BuildTemplateData(t, summary)

	// enrich summary.json with computed chapter/file data so other tools
	// (web UI, API, CLI) can consume structured chapters without
	// re-implementing the grouping algorithm
	if summary != nil {
		enrichSummaryWithChapters(summary, data)
		if enriched, err := json.MarshalIndent(summary, "", "  "); err == nil {
			if writeErr := os.WriteFile(summaryPath, enriched, 0644); writeErr != nil {
				slog.Warn("failed to write enriched summary", "path", summaryPath, "error", writeErr)
			}
		}
	}

	// generate HTML using the generator (which now has full summary data)
	var htmlBytes []byte
	if summary != nil {
		htmlBytes, err = gen.GenerateWithSummary(t, summary)
	} else {
		htmlBytes, err = gen.Generate(t)
	}
	if err != nil {
		return fmt.Errorf("generate html: %w", err)
	}

	if err := os.WriteFile(outputPath, htmlBytes, 0644); err != nil {
		return fmt.Errorf("write output file: %w", err)
	}

	return nil
}

// enrichSummaryWithChapters populates the structured chapters and files_changed
// fields in the summary from the computed template data. This makes the grouping
// data available to any tool reading summary.json.
func enrichSummaryWithChapters(summary *session.SummarizeResponse, data *sessionhtml.TemplateData) {
	// convert chapters
	summary.Chapters = make([]session.ChapterSummary, 0, len(data.Chapters))
	for _, ch := range data.Chapters {
		cs := session.ChapterSummary{
			ID:    ch.ID,
			Title: ch.Title,
		}

		// compute seq range and aggregate tool counts
		toolCounts := make(map[string]int)
		for _, item := range ch.Items {
			if item.Message != nil {
				if cs.StartSeq == 0 {
					cs.StartSeq = item.Message.ID
				}
				cs.EndSeq = item.Message.ID
			}
			if item.WorkBlock != nil {
				cs.TotalTools += item.WorkBlock.TotalTools
				cs.HasEdits = cs.HasEdits || item.WorkBlock.HasEdits
				for name, count := range item.WorkBlock.ToolCounts {
					toolCounts[name] += count
				}
				// update seq range from work block messages
				for _, wbMsg := range item.WorkBlock.Messages {
					if cs.StartSeq == 0 {
						cs.StartSeq = wbMsg.ID
					}
					cs.EndSeq = wbMsg.ID
				}
			}
		}
		if len(toolCounts) > 0 {
			cs.ToolCounts = toolCounts
		}
		summary.Chapters = append(summary.Chapters, cs)
	}

	// convert files changed
	summary.FilesChanged = make([]session.FileSummary, 0, len(data.FilesChanged))
	for _, fc := range data.FilesChanged {
		summary.FilesChanged = append(summary.FilesChanged, session.FileSummary{
			Path:    fc.Path,
			Added:   fc.Added,
			Removed: fc.Removed,
		})
	}
}

// formatAgentType formats agent type for display (e.g., "claude-code" -> "Claude Code").
func formatAgentType(agentType string) string {
	if agentType == "" {
		return "Assistant"
	}
	// capitalize first letter of each word, replace hyphens with spaces
	words := strings.Split(agentType, "-")
	for i, word := range words {
		if len(word) > 0 {
			words[i] = strings.ToUpper(word[:1]) + word[1:]
		}
	}
	return strings.Join(words, " ")
}
