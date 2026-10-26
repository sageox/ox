package usage

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/session/contexttrace"
)

// TestBuild_TraceShapes covers trace shapes older or odder writers produce.
// Failure prevented: a legacy or partial trace is misreported — a whisper
// without a ref vanishes, the same doc is listed twice, an MCP retrieval is
// not counted, or a self-report with no ref is shown as "used".
func TestBuild_TraceShapes(t *testing.T) {
	retrieval := func(cmd, q, refType string) contexttrace.Event {
		ev := invoked("lookup", cmd, 10, 1)
		ev.Query, ev.RefType = q, refType
		return ev
	}
	tests := []struct {
		name   string
		in     Input
		assert func(t *testing.T, r Report)
	}{
		{
			name: "empty trace source reads as none",
			in:   Input{},
			assert: func(t *testing.T, r Report) {
				assert.Equal(t, TraceSourceNone, r.Trace.Source)
				assert.False(t, r.Trace.Available)
			},
		},
		{
			name: "by-command list is capped",
			in: func() Input {
				var evs []contexttrace.Event
				for i := 0; i < maxByCmd+3; i++ {
					evs = append(evs, invoked(fmt.Sprintf("label %d", i), fmt.Sprintf("cmd%d", i), 1, i))
				}
				return Input{TraceSource: TraceSourceSession, Events: evs}
			}(),
			assert: func(t *testing.T, r Report) {
				assert.Len(t, r.Calls.ByCommand, maxByCmd)
				assert.Equal(t, maxByCmd+3, r.Calls.Total)
			},
		},
		{
			name: "prime without a per-source budget counts its inline total",
			in: Input{TraceSource: TraceSourceSession, Events: []contexttrace.Event{
				{Type: contexttrace.EventInvoked, Command: primeCommand, Label: "loaded team context", InlineTokens: 700},
			}},
			assert: func(t *testing.T, r Report) {
				assert.Contains(t, r.Injected.BySource, SourceStat{Source: "prime:total", Tokens: 700})
			},
		},
		{
			name: "surfaced items are deduplicated and whisper ids stand in for refs",
			in: Input{TraceSource: TraceSourceSession, Events: []contexttrace.Event{
				memoryProvided(), memoryProvided(),
				{Type: contexttrace.EventProvided, Source: contexttrace.SourceProjectWhisper, WhisperID: "wsp_1"},
			}},
			assert: func(t *testing.T, r Report) {
				assert.Equal(t, []string{"prime:MEMORY.md", "whisper:wsp_1"}, surfacedRefs(r))
			},
		},
		{
			name: "retrievals: default ref type, mcp tools, and unclassifiable commands",
			in: Input{TraceSource: TraceSourceSession, Events: []contexttrace.Event{
				retrieval("query", "auth", ""),
				retrieval("mcp Search", "billing", "query"),
				retrieval("", "no command", ""),
				retrieval("&&", "garbage", ""),
			}},
			assert: func(t *testing.T, r Report) {
				require.Len(t, r.Surfaced, 2)
				assert.Equal(t, Surfaced{Ref: "auth", RefType: "query", Via: "retrieval"}, r.Surfaced[0])
				assert.Equal(t, "billing", r.Surfaced[1].Ref)
				assert.Len(t, r.Used.Retrieval, 2)
			},
		},
		{
			name: "self-reports fall back to doc and skip ref-less lines",
			in: Input{TraceSource: TraceSourceSession, Events: []contexttrace.Event{
				{Type: contexttrace.EventInfluenced, Doc: "AGENTS.md", Decision: "legacy"},
				{Type: contexttrace.EventInfluenced, Decision: "no ref at all"},
			}},
			assert: func(t *testing.T, r Report) {
				require.Len(t, r.Used.SelfReported, 1)
				assert.Equal(t, "AGENTS.md", r.Used.SelfReported[0].Ref)
				assert.Equal(t, UsedReported, r.Used.Status)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.assert(t, Build(tt.in))
		})
	}
}
