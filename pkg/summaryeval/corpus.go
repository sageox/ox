package summaryeval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// LoadGoldenSession reads a GoldenSession from a corpus directory layout:
//
//	<corpus>/<session_name>/reference.json
//
// Returns (nil, nil) if the session dir doesn't exist — callers can
// distinguish "not curated" from "exists but broken".
func LoadGoldenSession(corpusDir, sessionName string) (*GoldenSession, error) {
	path := filepath.Join(corpusDir, sessionName, "reference.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var gs GoldenSession
	if err := json.Unmarshal(data, &gs); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if gs.Name == "" {
		gs.Name = sessionName
	}
	return &gs, nil
}

// LoadCorpus walks corpusDir and returns all GoldenSessions found.
// Each subdirectory containing reference.json is a session. Order is
// lexicographic by directory name for reproducible reports.
func LoadCorpus(corpusDir string) ([]GoldenSession, error) {
	entries, err := os.ReadDir(corpusDir)
	if err != nil {
		return nil, fmt.Errorf("read corpus dir %s: %w", corpusDir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	out := make([]GoldenSession, 0, len(names))
	for _, name := range names {
		gs, err := LoadGoldenSession(corpusDir, name)
		if err != nil {
			return nil, err
		}
		if gs == nil {
			continue // dir without reference.json — skip silently
		}
		out = append(out, *gs)
	}
	return out, nil
}

// LoadCandidate reads a candidate Summary from a JSON file. Used when
// comparing a distiller's output against a golden reference.
func LoadCandidate(path string) (*Summary, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var s Summary
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &s, nil
}

// ScoreCorpus evaluates each golden session against its paired candidate
// and returns an aggregated Report. The candidates map is keyed by
// session name. Sessions with no paired candidate are scored as
// all-zeros (missing_candidate reason) and counted in the corpus size.
//
// Gates (optional) are checked after aggregation; any unmet thresholds
// appear in Report.GatesFailed.
func ScoreCorpus(corpus []GoldenSession, candidates map[string]Summary, w Weights, gates *Gates) Report {
	report := Report{
		ScoredAt:       time.Now().UTC(),
		CorpusSize:     len(corpus),
		DimensionMeans: make(map[string]float64),
		Sessions:       make([]SessionScore, 0, len(corpus)),
	}
	if len(corpus) == 0 {
		return report
	}

	var overallSum float64
	dimSums := make(map[string]float64)

	report.OverallMin = 1.0
	report.OverallMax = 0.0

	for _, g := range corpus {
		cand, ok := candidates[g.Name]
		var ss SessionScore
		if !ok {
			ss = SessionScore{
				Name: g.Name,
				Dimensions: []DimensionScore{
					{Dimension: DimTitle, Score: 0, Reason: "no candidate"},
					{Dimension: DimSummary, Score: 0, Reason: "no candidate"},
					{Dimension: DimKeyActions, Score: 0, Reason: "no candidate"},
					{Dimension: DimOutcome, Score: 0, Reason: "no candidate"},
					{Dimension: DimAhaMoments, Score: 0, Reason: "no candidate"},
				},
				Overall: 0,
			}
		} else {
			ss = Score(g.Name, g.Reference, cand, w)
		}
		report.Sessions = append(report.Sessions, ss)
		overallSum += ss.Overall
		if ss.Overall < report.OverallMin {
			report.OverallMin = ss.Overall
		}
		if ss.Overall > report.OverallMax {
			report.OverallMax = ss.Overall
		}
		for _, d := range ss.Dimensions {
			dimSums[d.Dimension] += d.Score
		}
	}

	n := float64(len(corpus))
	report.OverallMean = overallSum / n
	for d, s := range dimSums {
		report.DimensionMeans[d] = s / n
	}

	if gates != nil {
		if gates.MinOverall > 0 && report.OverallMean < gates.MinOverall {
			report.GatesFailed = append(report.GatesFailed,
				fmt.Sprintf("overall_mean %.3f < gate %.3f", report.OverallMean, gates.MinOverall))
		}
		for _, d := range Dimensions() {
			min, ok := gates.MinDimensions[d]
			if !ok {
				continue
			}
			if mean := report.DimensionMeans[d]; mean < min {
				report.GatesFailed = append(report.GatesFailed,
					fmt.Sprintf("%s_mean %.3f < gate %.3f", d, mean, min))
			}
		}
	}
	return report
}
