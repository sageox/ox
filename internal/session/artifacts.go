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

// IsStubSummary reports whether resp is a stats-only LocalSummary stub
// rather than a substantive LLM-generated summary or a deliberate
// daemon-side failure marker. Stubs have no Title, no KeyActions, no
// AhaMoments, no Diagrams, no SageoxInsights, no AgentSummary, AND no
// ScoreReason — they're the silent-placeholder shape the heuristic
// LocalSummary generator produces while the LLM path hasn't run yet.
//
// Detection is structural, not text-pattern-based, so a future change to
// LocalSummary's wording wouldn't bypass this guard.
//
// ScoreReason is the load-bearing distinction from daemon validation-
// failure stubs: those also lack structured fields, but the daemon
// explicitly fills ScoreReason ("content validation failed: …",
// "richness validation failed: …") to mark the stub as a deliberate
// failure artifact that MUST be persisted so teammates see the failure
// in the ledger rather than a missing file.
func IsStubSummary(resp *SummarizeResponse) bool {
	if resp == nil {
		return true
	}
	if resp.ScoreReason != "" {
		return false
	}
	return resp.Title == "" &&
		len(resp.KeyActions) == 0 &&
		len(resp.AhaMoments) == 0 &&
		len(resp.Diagrams) == 0 &&
		len(resp.SageoxInsights) == 0 &&
		resp.AgentSummary == nil
}

// WriteSessionArtifacts generates the standard set of session artifacts from
// a stored session and summary response. Both the CLI stop path and daemon
// anti-entropy finalization call this to ensure identical output.
//
// When summaryResp is a stats-only LocalSummary stub (see IsStubSummary),
// summary.json and summary.md are NOT written — leaving them absent signals
// to push-summary / daemon anti-entropy that a real LLM summary still owes.
// Persisting the stub on disk was the ox-0pxt fingerprint: galexy-account
// sessions shipped with "N user messages, N assistant responses" because the
// stub got committed to the ledger before the LLM path ran. session.md is
// still emitted (it's the raw transcript, always useful).
func WriteSessionArtifacts(sessionDir string, stored *StoredSession, summaryResp *SummarizeResponse) (*ArtifactPaths, error) {
	paths := &ArtifactPaths{}

	writeSummary := summaryResp != nil && !IsStubSummary(summaryResp)

	// --- enrich summary.json with computed fields (files_changed, chapters) ---
	if writeSummary && stored != nil {
		EnrichSummary(stored, summaryResp)
	}

	// --- summary.json ---
	if writeSummary {
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
	if writeSummary {
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
