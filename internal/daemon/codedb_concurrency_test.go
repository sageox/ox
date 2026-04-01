package daemon

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestHeartbeatUpdatesCodeDBProjectRoot_MultipleWorkspaces(t *testing.T) {
	t.Parallel()

	logger := codedbTestLogger()
	handler := NewHeartbeatHandler(logger)
	mgr := NewCodeDBManager("/workspace/edinburgh-v1", logger, nil)

	handler.SetCallerPathCallback(func(path string) {
		mgr.UpdateProjectRoot(path)
	})

	// simulate the exact Conductor pattern:
	// edinburgh-v1 → khartoum-v1 → da-nang-v1
	workspaces := []struct {
		callerID string
		path     string
	}{
		{"abc123", "/workspace/edinburgh-v1"},
		{"def456", "/workspace/khartoum-v1"},
		{"ghi789", "/workspace/da-nang-v1"},
	}

	for _, ws := range workspaces {
		payload := HeartbeatPayload{
			CallerPath: ws.path,
			Timestamp:  time.Now(),
		}
		data, _ := json.Marshal(payload)
		handler.Handle(ws.callerID, data)
	}

	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.Equal(t, "/workspace/da-nang-v1", got)
}
