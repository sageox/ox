package glance

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDetectClusterBridge(t *testing.T) {
	now := time.Now()

	sessions := []SessionRecord{
		// Auth cluster: alice and bob
		{User: "alice", Time: now, Files: []string{"internal/auth/handler.go", "internal/auth/token.go"}},
		{User: "bob", Time: now, Files: []string{"internal/auth/session.go"}},
		// CLI cluster: dave and eve
		{User: "dave", Time: now, Files: []string{"internal/cli/output.go", "internal/cli/format.go"}},
		{User: "eve", Time: now, Files: []string{"internal/cli/output.go"}},
		// Bridge: carol touches both + a third package
		{User: "carol", Time: now, Files: []string{"internal/auth/token.go", "internal/cli/output.go", "internal/daemon/bridge.go"}},
	}

	patterns := DetectPatterns(sessions)

	var bridges []Pattern
	for _, p := range patterns {
		if p.Type == "cluster_bridge" {
			bridges = append(bridges, p)
		}
	}

	found := false
	for _, b := range bridges {
		if b.Authors[0] == "carol" {
			found = true
			assert.Contains(t, b.Detail, "only developer bridging")
		}
	}
	assert.True(t, found, "carol should be detected as a cluster bridge")
}

func TestDetectHotFiles(t *testing.T) {
	now := time.Now()

	sessions := []SessionRecord{
		{User: "alice", Time: now, Files: []string{"internal/sync/pull.go", "internal/auth/token.go"}},
		{User: "bob", Time: now, Files: []string{"internal/sync/pull.go"}},
		{User: "carol", Time: now, Files: []string{"internal/sync/pull.go", "internal/daemon/watcher.go"}},
		{User: "dave", Time: now, Files: []string{"internal/cli/output.go"}},
	}

	patterns := DetectPatterns(sessions)

	var hotFiles []Pattern
	for _, p := range patterns {
		if p.Type == "hot_file" {
			hotFiles = append(hotFiles, p)
		}
	}

	assert.Len(t, hotFiles, 1, "only pull.go has 3+ authors")
	assert.Equal(t, "internal/sync/pull.go", hotFiles[0].Files[0])
	assert.Len(t, hotFiles[0].Authors, 3)
	assert.Equal(t, "high", hotFiles[0].Risk)
}

func TestDetectHotFiles_TwoAuthorsNotHot(t *testing.T) {
	now := time.Now()

	sessions := []SessionRecord{
		{User: "alice", Time: now, Files: []string{"internal/auth/token.go"}},
		{User: "bob", Time: now, Files: []string{"internal/auth/token.go"}},
	}

	patterns := DetectPatterns(sessions)

	for _, p := range patterns {
		assert.NotEqual(t, "hot_file", p.Type, "2 authors should not trigger hot_file")
	}
}

func TestDetectSoloSilos(t *testing.T) {
	now := time.Now()

	sessions := []SessionRecord{
		// alice and bob share a file
		{User: "alice", Time: now, Files: []string{"internal/auth/token.go"}},
		{User: "bob", Time: now, Files: []string{"internal/auth/token.go"}},
		// eve works completely alone
		{User: "eve", Time: now, Files: []string{"tests/unit/foo_test.go", "tests/unit/bar_test.go"}},
	}

	patterns := DetectPatterns(sessions)

	var silos []Pattern
	for _, p := range patterns {
		if p.Type == "solo_silo" {
			silos = append(silos, p)
		}
	}

	assert.Len(t, silos, 1)
	assert.Equal(t, "eve", silos[0].Authors[0])
	assert.Equal(t, "low", silos[0].Risk)
}

func TestDetectSoloSilos_NoSiloWhenOverlapping(t *testing.T) {
	now := time.Now()

	sessions := []SessionRecord{
		{User: "alice", Time: now, Files: []string{"internal/auth/token.go"}},
		{User: "bob", Time: now, Files: []string{"internal/auth/token.go"}},
	}

	patterns := DetectPatterns(sessions)

	for _, p := range patterns {
		assert.NotEqual(t, "solo_silo", p.Type, "overlapping authors should not be silos")
	}
}
