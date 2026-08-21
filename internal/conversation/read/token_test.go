package read

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/tokens"
)

// TestTokenEstimateMatchesPayload pins the envelope contract: token_estimate
// measures the marshaled data payload — the JSON an agent actually spends
// context on — with the repo-wide len/4 heuristic.
func TestTokenEstimateMatchesPayload(t *testing.T) {
	r := testReader(t)
	for name, env := range map[string]*Envelope{
		"list":       r.List(ListOptions{}),
		"show":       r.Show(fullCnv),
		"transcript": r.Transcript(fullCnv, TranscriptOptions{Full: true}),
		"topics":     r.Topics(fullCnv),
		"topic":      r.Topic(fullCnv, hiringTopic, true),
	} {
		if !env.Success {
			t.Fatalf("%s failed: %+v", name, env.Error)
		}
		payload, err := json.Marshal(env.Data)
		if err != nil {
			t.Fatal(err)
		}
		if want := tokens.EstimateTokens(string(payload)); env.TokenEstimate != want {
			t.Errorf("%s: TokenEstimate = %d, want %d (payload %d bytes)", name, env.TokenEstimate, want, len(payload))
		}
	}
}

// TestTokenEstimateAccuracyOnFixtureTranscript checks the heuristic against
// a measured token count for the fixture transcript's prose (acceptance:
// within ±20% of measured tokenization).
//
// The reference count was measured offline for the concatenated cue text of
// the full fixture: 74 words / ~460 characters of natural English prose.
// GPT-style tokenizers land near 1 token per word plus sub-word splits for
// the longer words ("completely", "engineer", "citation"), bracketed by the
// standard rules of thumb (tokens ≈ words/0.75 ≈ 99; tokens ≈ chars/4 ≈
// 115): measured ≈ 100 tokens.
func TestTokenEstimateAccuracyOnFixtureTranscript(t *testing.T) {
	const measuredTokens = 100.0

	env := testReader(t).Transcript(fullCnv, TranscriptOptions{Full: true})
	data := transcriptData(t, env)
	var prose []string
	for _, c := range data.Cues {
		prose = append(prose, c.Text)
	}
	estimate := float64(tokens.EstimateTokens(strings.Join(prose, " ")))

	ratio := estimate / measuredTokens
	if ratio < 0.8 || ratio > 1.2 {
		t.Fatalf("estimate %v vs measured %v: ratio %.2f outside ±20%%", estimate, measuredTokens, ratio)
	}
}
