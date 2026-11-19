package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: any ox command heartbeats with its agent ID, not only a
// hook. A marker a Ledger pull planted beside a live recording, under that
// recording's agent ID, must not be armed by the agent's next heartbeat,
// while the recording ox started is armed exactly as before.
func TestAgentHeartbeatNeverArmsAPlantedMarkerReusingItsAgentID(t *testing.T) {
	onNewMachine(t)
	isolateCredentialsWithDir(t)
	t.Setenv(endpoint.EnvVar, "")
	t.Setenv("SAGEOX_TOKEN", "")
	home := t.TempDir()
	for name, dir := range map[string]string{"XDG_CONFIG_HOME": "config", "XDG_DATA_HOME": "data", "XDG_CACHE_HOME": "cache"} {
		t.Setenv(name, filepath.Join(home, dir))
	}
	t.Setenv("OX_USER_CONFIG", filepath.Join(home, "user-config.yaml"))
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".sageox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".sageox", "config.json"), []byte(`{"config_version":"2","repo_id":"repo_provenance_test"}`), 0o644))
	live, err := session.StartRecording(root, session.StartRecordingOptions{
		AgentID: "OxLive", AdapterName: "claude-code", Username: "person-a", WatchMode: "hook", WorkspacePath: root,
	})
	require.NoError(t, err)
	// sorts ahead of the live recording, so a first-match lookup meets it first
	plantedDir := filepath.Join(filepath.Dir(live.SessionPath), "0000-planted")
	require.NoError(t, os.MkdirAll(plantedDir, 0o755))
	marker, err := json.Marshal(map[string]any{
		"agent_id": "OxLive", "session_id": "ses_planted", "adapter_name": "claude-code", "watch_mode": "hook",
		"session_path": plantedDir, "workspace_path": root, "session_file": "/planted.jsonl",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(plantedDir, ".recording.json"), marker, 0o600))
	planted, err := session.ReadRecordingStateFile(plantedDir)
	require.NoError(t, err)

	cfg := DefaultConfig()
	cfg.ProjectRoot = root
	d := New(cfg, discardLogger())
	d.ctx, d.cancel = context.WithCancel(context.Background())
	t.Cleanup(func() {
		d.cancel()
		d.releaseGlobalSyncLease()
	})
	d.initComponents()

	// an ordinary heartbeat, from any ox command the agent runs
	(&daemonServiceImpl{d: d}).Heartbeat("", mustMarshal(HeartbeatPayload{RepoPath: root, AgentID: "OxLive", Timestamp: time.Now().UTC()}))

	assert.False(t, d.sessionWatcher.HookNoted(planted), "a heartbeat armed a marker ox never started")
	assert.True(t, d.sessionWatcher.HookNoted(live), "the recording ox started was not armed")
}
