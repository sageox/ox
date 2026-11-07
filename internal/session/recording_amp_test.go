package session

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestAmpStartsAtPrompt: only an Amp recording whose prime found its sidecar
// is bounded by its start offset. Every other recording, and an Amp recording
// from before prime set that boundary, keeps the StartedAt filter.
func TestAmpStartsAtPrompt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state RecordingState
		want  bool
	}{
		{"amp with a known start", RecordingState{AdapterName: "amp", SessionFile: "/s.jsonl", StartOffsetKnown: true, StartOffset: 42}, true},
		{"amp at byte zero", RecordingState{AdapterName: "amp", SessionFile: "/s.jsonl", StartOffsetKnown: true}, true},
		{"amp started before the boundary existed", RecordingState{AdapterName: "amp", SessionFile: "/s.jsonl"}, false},
		{"amp whose sidecar was not found at prime", RecordingState{AdapterName: "amp", StartOffsetKnown: true}, false},
		{"claude-code", RecordingState{AdapterName: "claude-code", SessionFile: "/s.jsonl", StartOffsetKnown: true, StartOffset: 10}, false},
		{"codex", RecordingState{AdapterName: "codex", SessionFile: "/s.jsonl", StartOffsetKnown: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.state.AmpStartsAtPrompt())
		})
	}
}
