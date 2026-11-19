package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: the hook catch-up acts only on recordings a local hook
// reported, and a hook's only report is its heartbeat. A daemon that stops
// handing agent heartbeats to the session watcher silently ends catch-up for
// every recording, and Claude Code answers written after the last Stop hook
// stop reaching raw.jsonl and the live stream.
func TestAgentHeartbeatArmsTheHookCatchUpForThatAgentsRecording(t *testing.T) {
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
	require.NoError(t, os.WriteFile(filepath.Join(root, ".sageox", "config.json"), []byte(`{"config_version":"2","repo_id":"repo_arming_test"}`), 0o644))
	start := func(agentID string) *session.RecordingState {
		st, err := session.StartRecording(root, session.StartRecordingOptions{
			AgentID: agentID, AdapterName: "claude-code", Username: "person-a", WatchMode: "hook", WorkspacePath: root,
		})
		require.NoError(t, err)
		return st
	}
	reported, silent := start("OxArmA"), start("OxArmB")

	cfg := DefaultConfig()
	cfg.ProjectRoot = root
	d := New(cfg, discardLogger())
	d.ctx, d.cancel = context.WithCancel(context.Background())
	t.Cleanup(func() {
		d.cancel()
		d.releaseGlobalSyncLease()
	})
	d.initComponents()
	require.False(t, d.sessionWatcher.HookNoted(reported), "armed before any hook reported it")

	// the IPC handler for a heartbeat message hands it to this service method
	(&daemonServiceImpl{d: d}).Heartbeat("", mustMarshal(HeartbeatPayload{RepoPath: root, AgentID: "OxArmA", Timestamp: time.Now().UTC()}))

	assert.True(t, d.sessionWatcher.HookNoted(reported), "an agent heartbeat did not arm that agent's recording")
	assert.False(t, d.sessionWatcher.HookNoted(silent), "a heartbeat armed another agent's recording")
}
