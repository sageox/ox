package lfs

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sageox/ox/internal/trace/model"
	"github.com/stretchr/testify/require"
)

// Adding trace metadata must preserve legacy manifests and unknown observations.
func TestSessionMetaTraceRoundTrip(t *testing.T) {
	t.Parallel()
	for _, trace := range []*model.Metadata{nil, {
		NativeSessions: []model.NativeRanges{{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", SpansBytes: []model.ByteRange{{0, 400}, {500, 800}}, EventsBytes: []model.ByteRange{}}},
		StoppedAt:      time.Date(2026, 9, 22, 1, 0, 0, 0, time.UTC), SpansLines: 2, Spans: 9, PausedBytesSkipped: 100, TrailingBytesSkipped: 3, LateBytes: 42,
		Scrubbed: map[string]int64{"user.email": 9},
	}} {
		before := SessionMeta{Version: "1", Trace: trace}
		encoded, err := json.Marshal(before)
		require.NoError(t, err)
		var after SessionMeta
		require.NoError(t, json.Unmarshal(encoded, &after))
		require.Equal(t, before, after)
		if trace == nil {
			require.NotContains(t, string(encoded), `"trace"`)
		} else {
			require.Contains(t, string(encoded), `"receiver_up_before_first_prompt":null`)
			require.Contains(t, string(encoded), `"receiver_version":null`)
			require.Contains(t, string(encoded), `"spans_bytes":[[0,400],[500,800]]`)
		}
	}
}
