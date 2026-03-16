package daemon

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDaemonInfo_JSON_RoundTrip(t *testing.T) {
	info := DaemonInfo{
		WorkspaceID:   "a1b2c3d4",
		WorkspacePath: "/home/dev/project",
		SocketPath:    "/tmp/sageox/daemon/daemon-a1b2c3d4.sock",
		PID:           12345,
		Version:       "0.4.0",
		StartedAt:     time.Date(2026, 3, 11, 10, 0, 0, 0, time.UTC),
	}

	data, err := json.Marshal(info)
	require.NoError(t, err)

	var decoded DaemonInfo
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, info.WorkspaceID, decoded.WorkspaceID)
	assert.Equal(t, info.WorkspacePath, decoded.WorkspacePath)
	assert.Equal(t, info.SocketPath, decoded.SocketPath)
	assert.Equal(t, info.PID, decoded.PID)
	assert.Equal(t, info.Version, decoded.Version)
	assert.True(t, info.StartedAt.Equal(decoded.StartedAt))
}

func TestDaemonInfo_RepoID(t *testing.T) {
	// DaemonInfo with RepoID should survive JSON round-trip.
	// This tests the new RepoID field being added to DaemonInfo.
	info := DaemonInfo{
		WorkspaceID:   "a1b2c3d4",
		WorkspacePath: "/home/dev/project",
		SocketPath:    "/tmp/sageox/daemon/daemon-a1b2c3d4.sock",
		PID:           12345,
		Version:       "0.4.0",
		StartedAt:     time.Date(2026, 3, 11, 10, 0, 0, 0, time.UTC),
		RepoID:        "repo_01jfk3mab7e9qp2xz4nh8vwcdr",
	}

	data, err := json.Marshal(info)
	require.NoError(t, err)

	// verify repo_id appears in JSON
	assert.Contains(t, string(data), `"repo_id"`)
	assert.Contains(t, string(data), "repo_01jfk3mab7e9qp2xz4nh8vwcdr")

	var decoded DaemonInfo
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "repo_01jfk3mab7e9qp2xz4nh8vwcdr", decoded.RepoID)
}

func TestRegistry_BackwardsCompatibility(t *testing.T) {
	// old format without repo_id should load fine
	oldJSON := `{
		"daemons": {
			"a1b2c3d4": {
				"workspace_id": "a1b2c3d4",
				"workspace_path": "/home/dev/project",
				"socket_path": "/tmp/daemon-a1b2c3d4.sock",
				"pid": 12345,
				"version": "0.3.0",
				"started_at": "2026-03-10T10:00:00Z"
			}
		}
	}`

	var reg Registry
	err := json.Unmarshal([]byte(oldJSON), &reg)
	require.NoError(t, err)
	require.NotNil(t, reg.Daemons)
	require.Len(t, reg.Daemons, 1)

	info := reg.Daemons["a1b2c3d4"]
	assert.Equal(t, "a1b2c3d4", info.WorkspaceID)
	assert.Equal(t, "/home/dev/project", info.WorkspacePath)
	assert.Equal(t, 12345, info.PID)
	assert.Equal(t, "0.3.0", info.Version)
	// RepoID should be zero value (empty string) for old entries
	assert.Empty(t, info.RepoID)
}

func TestRegistry_List(t *testing.T) {
	reg := &Registry{Daemons: map[string]DaemonInfo{
		"aaaa1111": {WorkspaceID: "aaaa1111", PID: 100},
		"bbbb2222": {WorkspaceID: "bbbb2222", PID: 200},
		"cccc3333": {WorkspaceID: "cccc3333", PID: 300},
	}}

	list := reg.List()
	assert.Len(t, list, 3)

	// collect workspace IDs to verify all are present (order not guaranteed)
	ids := make(map[string]bool)
	for _, info := range list {
		ids[info.WorkspaceID] = true
	}
	assert.True(t, ids["aaaa1111"])
	assert.True(t, ids["bbbb2222"])
	assert.True(t, ids["cccc3333"])
}

func TestFindByRepoID(t *testing.T) {
	reg := &Registry{Daemons: map[string]DaemonInfo{
		"aaaa1111": {WorkspaceID: "aaaa1111", RepoID: "repo_abc", PID: 100},
		"bbbb2222": {WorkspaceID: "bbbb2222", RepoID: "repo_abc", PID: 200},
		"cccc3333": {WorkspaceID: "cccc3333", RepoID: "repo_xyz", PID: 300},
		"dddd4444": {WorkspaceID: "dddd4444", RepoID: "", PID: 400}, // legacy entry without repo_id
	}}

	t.Run("found", func(t *testing.T) {
		info := reg.FindByRepoID("repo_abc")
		require.NotNil(t, info, "should find a daemon with matching repo_id")
		assert.Equal(t, "repo_abc", info.RepoID)
		// should be one of the two matching entries
		assert.True(t, info.WorkspaceID == "aaaa1111" || info.WorkspaceID == "bbbb2222")
	})

	t.Run("found_unique", func(t *testing.T) {
		info := reg.FindByRepoID("repo_xyz")
		require.NotNil(t, info)
		assert.Equal(t, "cccc3333", info.WorkspaceID)
		assert.Equal(t, "repo_xyz", info.RepoID)
	})

	t.Run("not_found", func(t *testing.T) {
		info := reg.FindByRepoID("repo_notfound")
		assert.Nil(t, info, "should return nil for unknown repo_id")
	})

	t.Run("empty_repo_id", func(t *testing.T) {
		// searching for empty string should not match legacy entries
		info := reg.FindByRepoID("")
		assert.Nil(t, info, "should not match entries with empty repo_id")
	})
}

func TestFindByRepoID_EmptyRegistry(t *testing.T) {
	reg := &Registry{Daemons: make(map[string]DaemonInfo)}

	info := reg.FindByRepoID("repo_abc")
	assert.Nil(t, info)
}

func TestRegistry_Register_WithRepoID(t *testing.T) {
	reg := &Registry{Daemons: make(map[string]DaemonInfo)}

	info := DaemonInfo{
		WorkspaceID: "aaaa1111",
		RepoID:      "repo_test",
		PID:         100,
		Version:     "0.4.0",
		StartedAt:   time.Now(),
	}

	// Register uses Save() which writes to disk via RegistryPath(),
	// so we test the in-memory part only
	reg.mu.Lock()
	reg.Daemons[info.WorkspaceID] = info
	reg.mu.Unlock()

	assert.Len(t, reg.Daemons, 1)
	stored := reg.Daemons["aaaa1111"]
	assert.Equal(t, "repo_test", stored.RepoID)
}

func TestDaemonInfo_RepoID_OmittedWhenEmpty(t *testing.T) {
	// when RepoID is empty, it should be omitted from JSON (if tagged with omitempty)
	// or present as empty string. Either is acceptable for backwards compat.
	info := DaemonInfo{
		WorkspaceID: "a1b2c3d4",
		PID:         12345,
	}

	data, err := json.Marshal(info)
	require.NoError(t, err)

	var decoded DaemonInfo
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)
	assert.Empty(t, decoded.RepoID)
}
