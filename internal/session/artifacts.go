package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ArtifactPaths holds the output paths for generated session artifacts.
type ArtifactPaths struct {
	SummaryMD   string // summary.md — structured markdown summary
	SummaryJSON string // summary.json — machine-readable summary
	HTML        string // session.html — interactive HTML viewer
	SessionMD   string // session.md — full session transcript in markdown
}

// HTMLGenerator abstracts HTML generation to avoid import cycles
// (internal/session cannot import internal/session/html).
type HTMLGenerator interface {
	GenerateToFile(stored *StoredSession, outputPath string) error
	GenerateToFileWithSummary(stored *StoredSession, summary *SummarizeResponse, outputPath string) error
}

// WriteSessionArtifacts generates the standard set of session artifacts from
// a stored session and summary response. Both the CLI stop path and daemon
// anti-entropy finalization call this to ensure identical output.
//
// The summaryResp may come from LocalSummary (stats-only) or LLM (rich).
// Either way, the same 4 files are produced.
func WriteSessionArtifacts(sessionDir string, stored *StoredSession, summaryResp *SummarizeResponse, htmlGen HTMLGenerator) (*ArtifactPaths, error) {
	paths := &ArtifactPaths{}

	// --- summary.json ---
	if summaryResp != nil {
		summaryJSONPath := filepath.Join(sessionDir, "summary.json")
		summaryJSON, err := json.MarshalIndent(summaryResp, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("marshal summary.json: %w", err)
		}
		if err := os.WriteFile(summaryJSONPath, summaryJSON, 0644); err != nil {
			return nil, fmt.Errorf("write summary.json: %w", err)
		}
		paths.SummaryJSON = summaryJSONPath
	}

	// --- summary.md (structured markdown from SummarizeResponse) ---
	if summaryResp != nil {
		summaryMDPath := filepath.Join(sessionDir, "summary.md")
		summaryView := SummarizeResponseToSummaryView(summaryResp)
		gen := NewSummaryMarkdownGenerator()
		var entries []map[string]any
		if stored != nil {
			entries = stored.Entries
		}
		var meta *StoreMeta
		if stored != nil {
			meta = stored.Meta
		}
		mdBytes, err := gen.Generate(meta, summaryView, entries)
		if err != nil {
			return nil, fmt.Errorf("generate summary.md: %w", err)
		}
		if err := os.WriteFile(summaryMDPath, mdBytes, 0644); err != nil {
			return nil, fmt.Errorf("write summary.md: %w", err)
		}
		paths.SummaryMD = summaryMDPath
	}

	// --- session.html ---
	if htmlGen == nil {
		return nil, fmt.Errorf("generate session.html: html generator is nil")
	}
	htmlPath := filepath.Join(sessionDir, "session.html")
	if summaryResp != nil {
		if err := htmlGen.GenerateToFileWithSummary(stored, summaryResp, htmlPath); err != nil {
			return nil, fmt.Errorf("generate session.html: %w", err)
		}
	} else {
		if err := htmlGen.GenerateToFile(stored, htmlPath); err != nil {
			return nil, fmt.Errorf("generate session.html: %w", err)
		}
	}
	paths.HTML = htmlPath

	// --- session.md ---
	sessionMDPath := filepath.Join(sessionDir, "session.md")
	mdGen := NewMarkdownGenerator()
	if err := mdGen.GenerateToFile(stored, sessionMDPath); err != nil {
		return nil, fmt.Errorf("generate session.md: %w", err)
	}
	paths.SessionMD = sessionMDPath

	return paths, nil
}

// SummarizeResponseToSummaryView converts a SummarizeResponse to the
// SummaryView used by the markdown generator.
func SummarizeResponseToSummaryView(resp *SummarizeResponse) *SummaryView {
	if resp == nil {
		return nil
	}
	return &SummaryView{
		Text:        resp.Summary,
		KeyActions:  resp.KeyActions,
		Outcome:     resp.Outcome,
		TopicsFound: resp.TopicsFound,
		FinalPlan:   resp.FinalPlan,
		Diagrams:    resp.Diagrams,
	}
}
