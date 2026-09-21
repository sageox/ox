package adapters

import (
	"testing"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProtocolToInternal_CarriesCallID: the id an external adapter binary
// extracted (Claude Code tool_use.id / tool_result.tool_use_id) must survive
// the protocol→internal hop, or the converter downstream has nothing to copy.
func TestProtocolToInternal_CarriesCallID(t *testing.T) {
	ts := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	entries := protocolToInternal([]adapterprotocol.RawEntry{
		adapterruntime.ToolUseWithID(ts, "Bash", `{"command":"ls"}`, "toolu_01"),
		adapterruntime.ToolResultWithID(ts, "file.go", false, "toolu_01"),
		adapterruntime.ToolUseEntry(ts, "Read", "x"),
	})
	require.Len(t, entries, 3)
	assert.Equal(t, "toolu_01", entries[0].CallID)
	assert.Equal(t, "toolu_01", entries[1].CallID)
	assert.Equal(t, "Bash", entries[0].ToolName)
	assert.Equal(t, "file.go", entries[1].ToolOutput)
	assert.Empty(t, entries[2].CallID)
}
