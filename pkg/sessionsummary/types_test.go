package sessionsummary

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSummarizeResponse_JSONRoundTrip(t *testing.T) {
	t.Run("nil AgentSummary omitted from JSON", func(t *testing.T) {
		resp := SummarizeResponse{
			Title:   "Test Session",
			Summary: "Did things",
			Outcome: "success",
		}
		data, err := json.Marshal(resp)
		require.NoError(t, err)
		assert.NotContains(t, string(data), "agent_summary")
	})

	t.Run("empty AgentSummary included with empty fields omitted", func(t *testing.T) {
		resp := SummarizeResponse{
			Title:        "Test",
			Summary:      "Did things",
			AgentSummary: &AgentSummary{},
		}
		data, err := json.Marshal(resp)
		require.NoError(t, err)
		assert.Contains(t, string(data), "agent_summary")
		assert.NotContains(t, string(data), "decisions")
	})

	t.Run("full round-trip preserves all fields", func(t *testing.T) {
		resp := SummarizeResponse{
			Title:   "Auth Implementation",
			Summary: "Added JWT auth",
			Outcome: "success",
			AgentSummary: &AgentSummary{
				Decisions:   []Decision{{What: "Use JWT", Why: "Standard"}},
				ActionItems: []ActionItem{{Task: "Add tests", Priority: "high"}},
				Constraints: []string{"Must be backward compatible"},
			},
			AhaMoments:   []AhaMoment{{Seq: 5, Role: "user", Type: "question", Highlight: "Why not OAuth?"}},
			QualityScore: 0.85,
		}

		data, err := json.Marshal(resp)
		require.NoError(t, err)

		var decoded SummarizeResponse
		require.NoError(t, json.Unmarshal(data, &decoded))

		assert.Equal(t, resp.Title, decoded.Title)
		assert.Equal(t, resp.QualityScore, decoded.QualityScore)
		require.NotNil(t, decoded.AgentSummary)
		assert.Len(t, decoded.AgentSummary.Decisions, 1)
		assert.Equal(t, "Use JWT", decoded.AgentSummary.Decisions[0].What)
		assert.Len(t, decoded.AhaMoments, 1)
	})

	t.Run("backward compat: old JSON without agent_summary deserializes", func(t *testing.T) {
		// simulates deserializing an existing summary.json that predates AgentSummary
		oldJSON := `{"title":"Old Session","summary":"Did stuff","outcome":"success","quality_score":0.5}`
		var resp SummarizeResponse
		require.NoError(t, json.Unmarshal([]byte(oldJSON), &resp))
		assert.Equal(t, "Old Session", resp.Title)
		assert.Nil(t, resp.AgentSummary)
		assert.Equal(t, 0.5, resp.QualityScore)
	})
}
