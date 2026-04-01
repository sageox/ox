package daemon

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMessage_MarshalJSON(t *testing.T) {
	msg := Message{
		Type:    MsgTypePing,
		Payload: json.RawMessage(`{"key":"value"}`),
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"type":"ping"`)
	assert.Contains(t, string(data), `"payload"`)
}

func TestMessage_WithWorkspaceID(t *testing.T) {
	msg := Message{
		Type:        MsgTypeStatus,
		WorkspaceID: "a1b2c3d4",
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"type":"status"`)
	assert.Contains(t, string(data), `"workspace_id":"a1b2c3d4"`)

	// verify round-trip
	var decoded Message
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)
	assert.Equal(t, "a1b2c3d4", decoded.WorkspaceID)
}

func TestMessage_WorkspaceID_OmittedWhenEmpty(t *testing.T) {
	msg := Message{
		Type: MsgTypePing,
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "workspace_id")
}

func TestMessage_CallerID(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		msg := Message{
			Type:        MsgTypeStatus,
			WorkspaceID: "a1b2c3d4",
			CallerID:    "e5f6g7h8",
		}

		data, err := json.Marshal(msg)
		require.NoError(t, err)
		assert.Contains(t, string(data), `"caller_id":"e5f6g7h8"`)

		var decoded Message
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)
		assert.Equal(t, "e5f6g7h8", decoded.CallerID)
	})

	t.Run("omitted_when_empty", func(t *testing.T) {
		msg := Message{
			Type: MsgTypePing,
		}

		data, err := json.Marshal(msg)
		require.NoError(t, err)
		assert.NotContains(t, string(data), "caller_id")
	})

	t.Run("backwards_compat_missing_field", func(t *testing.T) {
		// JSON from an older client that doesn't send caller_id
		oldJSON := `{"type":"status","workspace_id":"a1b2c3d4"}`

		var decoded Message
		err := json.Unmarshal([]byte(oldJSON), &decoded)
		require.NoError(t, err)
		assert.Equal(t, "status", decoded.Type)
		assert.Equal(t, "a1b2c3d4", decoded.WorkspaceID)
		assert.Empty(t, decoded.CallerID, "missing caller_id should default to empty string")
	})

	t.Run("backwards_compat_old_worktree_id", func(t *testing.T) {
		// JSON from an older client that sends worktree_id (old name)
		// should be silently ignored — CallerID stays empty
		oldJSON := `{"type":"status","workspace_id":"a1b2c3d4","worktree_id":"e5f6g7h8"}`

		var decoded Message
		err := json.Unmarshal([]byte(oldJSON), &decoded)
		require.NoError(t, err)
		assert.Empty(t, decoded.CallerID, "old worktree_id field should not populate CallerID")
	})
}

func TestResponse_MarshalJSON(t *testing.T) {
	resp := Response{
		Success: true,
		Data:    json.RawMessage(`"pong"`),
	}

	data, err := json.Marshal(resp)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"success":true`)
}

func TestResponse_WithError(t *testing.T) {
	resp := Response{
		Success: false,
		Error:   "something went wrong",
	}

	data, err := json.Marshal(resp)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"success":false`)
	assert.Contains(t, string(data), `"error":"something went wrong"`)
}
