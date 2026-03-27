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

	// enrich summary.json with computed chapter/file data so other tools
	// (web UI, API, CLI) can consume structured chapters without
	// re-implementing the grouping algorithm
	if summary != nil {
		gen.EnrichSummary(t, summary)
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
