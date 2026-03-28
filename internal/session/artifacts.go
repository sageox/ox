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
	SessionMD   string // session.md — full session transcript in markdown
}

// WriteSessionArtifacts generates the standard set of session artifacts from
// a stored session and summary response. Both the CLI stop path and daemon
// anti-entropy finalization call this to ensure identical output.
//
// The summaryResp may come from LocalSummary (stats-only) or LLM (rich).
// Either way, the same 3 files are produced.
func WriteSessionArtifacts(sessionDir string, stored *StoredSession, summaryResp *SummarizeResponse) (*ArtifactPaths, error) {
	paths := &ArtifactPaths{}

	// --- enrich summary.json with computed fields (files_changed, chapters) ---
	if summaryResp != nil && stored != nil {
		EnrichSummary(stored, summaryResp)
	}

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
