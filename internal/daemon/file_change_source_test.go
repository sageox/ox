package daemon

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockMurmurPublisher captures PublishMurmur calls for testing.
type mockMurmurPublisher struct {
	mu       sync.Mutex
	payloads []MurmurPayload
}

func (m *mockMurmurPublisher) PublishMurmur(payload MurmurPayload) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.payloads = append(m.payloads, payload)
}

func (m *mockMurmurPublisher) Payloads() []MurmurPayload {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]MurmurPayload, len(m.payloads))
	copy(result, m.payloads)
	return result
}

// --- A. Drain + batch lifecycle ---

// TestPublisher_DrainAccumulatesIntoPending verifies that drain() pulls
// settled changes into the pending buffer without publishing.
func TestPublisher_DrainAccumulatesIntoPending(t *testing.T) {
	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()
	pub := &mockMurmurPublisher{}

	p := NewFileChangeMurmurPublisher(acc, pub, "/tmp/ledger", "/tmp/project", slogDiscard())

	acc.AddEvent("src/a.go", fsnotify.Write, false)
	acc.AddEvent("src/b.go", fsnotify.Create, false)
	time.Sleep(100 * time.Millisecond) // settle

	p.drain()

	assert.Equal(t, 2, p.PendingCount(), "drain should buffer changes")
	assert.Empty(t, pub.Payloads(), "drain alone should not publish")
}

// TestPublisher_PublishClearsPending verifies that publish() emits a murmur
// and clears the pending buffer.
func TestPublisher_PublishClearsPending(t *testing.T) {
	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()
	pub := &mockMurmurPublisher{}

	p := NewFileChangeMurmurPublisher(acc, pub, "/tmp/ledger", "/tmp/project", slogDiscard())

	acc.AddEvent("src/main.go", fsnotify.Write, false)
	time.Sleep(100 * time.Millisecond)

	p.drain()
	p.publish()

	assert.Equal(t, 0, p.PendingCount(), "publish should clear pending")
	require.Len(t, pub.Payloads(), 1)
	assert.Contains(t, pub.Payloads()[0].Content, "src/main.go")
}

// TestPublisher_NoPendingNoPublish verifies no murmur when nothing changed.
func TestPublisher_NoPendingNoPublish(t *testing.T) {
	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()
	pub := &mockMurmurPublisher{}

	p := NewFileChangeMurmurPublisher(acc, pub, "/tmp/ledger", "/tmp/project", slogDiscard())
	p.publish()

	assert.Empty(t, pub.Payloads())
}

// TestPublisher_CollapsesDuplicatePaths verifies that multiple changes to the
// same file collapse in the pending buffer.
func TestPublisher_CollapsesDuplicatePaths(t *testing.T) {
	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()
	pub := &mockMurmurPublisher{}

	p := NewFileChangeMurmurPublisher(acc, pub, "/tmp/ledger", "/tmp/project", slogDiscard())

	// first batch
	acc.AddEvent("src/main.go", fsnotify.Write, false)
	time.Sleep(100 * time.Millisecond)
	p.drain()

	// second batch — same file
	acc.AddEvent("src/main.go", fsnotify.Write, false)
	time.Sleep(100 * time.Millisecond)
	p.drain()

	assert.Equal(t, 1, p.PendingCount(), "same path should collapse")

	p.publish()
	require.Len(t, pub.Payloads(), 1)
}

// TestPublisher_CreateThenDeleteSuppressed verifies temp file pattern.
func TestPublisher_CreateThenDeleteSuppressed(t *testing.T) {
	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()
	pub := &mockMurmurPublisher{}

	p := NewFileChangeMurmurPublisher(acc, pub, "/tmp/ledger", "/tmp/project", slogDiscard())

	acc.AddEvent("tmp.go", fsnotify.Create, false)
	time.Sleep(100 * time.Millisecond)
	p.drain()

	acc.AddEvent("tmp.go", fsnotify.Remove, false)
	time.Sleep(100 * time.Millisecond)
	p.drain()

	assert.Equal(t, 0, p.PendingCount(), "create+delete should suppress")

	p.publish()
	assert.Empty(t, pub.Payloads(), "suppressed file should not produce murmur")
}

// --- B. Startup age cap ---

// TestPublisher_StartupCapDropsOldChanges verifies that on first publish,
// changes older than startupMaxAge are dropped.
func TestPublisher_StartupCapDropsOldChanges(t *testing.T) {
	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()
	pub := &mockMurmurPublisher{}

	p := NewFileChangeMurmurPublisher(acc, pub, "/tmp/ledger", "/tmp/project", slogDiscard())

	// manually inject an old change
	p.mu.Lock()
	p.pending["old.go"] = &FileChange{
		Path: "old.go", ChangeType: ChangeModified,
		Timestamp: time.Now().Add(-1 * time.Hour), // 1 hour ago
	}
	p.pending["new.go"] = &FileChange{
		Path: "new.go", ChangeType: ChangeModified,
		Timestamp: time.Now(), // just now
	}
	p.mu.Unlock()

	p.publish()

	require.Len(t, pub.Payloads(), 1)
	assert.Contains(t, pub.Payloads()[0].Content, "new.go")
	assert.NotContains(t, pub.Payloads()[0].Content, "old.go")
}

// --- C. Format tests ---

func TestFormatSmallChanges(t *testing.T) {
	changes := []FileChange{
		{Path: "src/main.go", ChangeType: ChangeModified},
		{Path: "src/config.go", ChangeType: ChangeCreated},
	}
	content := formatSmallChanges(changes)
	assert.Equal(t, "created src/config.go, modified src/main.go", content)
}

func TestFormatMediumChanges(t *testing.T) {
	changes := []FileChange{
		{Path: "src/a.go", ChangeType: ChangeModified},
		{Path: "src/b.go", ChangeType: ChangeModified},
		{Path: "src/c.go", ChangeType: ChangeCreated},
		{Path: "tests/d.go", ChangeType: ChangeDeleted},
		{Path: "tests/e.go", ChangeType: ChangeModified},
		{Path: "tests/f.go", ChangeType: ChangeModified},
	}
	content := formatMediumChanges(changes)
	assert.Contains(t, content, "6 files")
	assert.Contains(t, content, "src/(2M,1A)")
	assert.Contains(t, content, "tests/(2M,1D)")
}

func TestFormatLargeChanges(t *testing.T) {
	var changes []FileChange
	for i := range 30 {
		changes = append(changes, FileChange{
			Path:       fmt.Sprintf("pkg%d/file%d.go", i%3, i),
			ChangeType: ChangeModified,
		})
	}
	content := formatLargeChanges(changes)
	assert.Contains(t, content, "30 files")
	assert.Contains(t, content, "pkg0/(10)")
}

func TestFormatHugeChanges(t *testing.T) {
	var changes []FileChange
	for i := range 200 {
		changes = append(changes, FileChange{
			Path:       fmt.Sprintf("pkg%d/file%d.go", i%10, i),
			ChangeType: ChangeModified,
		})
	}
	content := formatHugeChanges(changes)
	assert.Equal(t, "200 files across 10 dirs", content)
}

func TestFormatFileChangeMurmur_IncludesBranchAndWorktree(t *testing.T) {
	changes := []FileChange{
		{Path: "src/main.go", ChangeType: ChangeModified},
	}
	content := formatFileChangeMurmur(changes, "ajit/feature-branch", "/Users/ajit/src/myproject")
	assert.Contains(t, content, "[ajit/feature-branch@myproject]")
	assert.Contains(t, content, "modified src/main.go")
}

func TestFormatFileChangeMurmur_NoBranch(t *testing.T) {
	changes := []FileChange{
		{Path: "README.md", ChangeType: ChangeModified},
	}
	content := formatFileChangeMurmur(changes, "", "/Users/ajit/src/myproject")
	assert.Contains(t, content, "[@myproject]")
	assert.Contains(t, content, "modified README.md")
}

func TestShortenRemoteURL(t *testing.T) {
	tests := []struct{ input, want string }{
		{"https://github.com/sageox/ox.git", "sageox/ox"},
		{"https://github.com/sageox/ox", "sageox/ox"},
		{"git@github.com:sageox/ox.git", "sageox/ox"},
		{"https://gitlab.com/team/project.git", "team/project"},
		{"git@bitbucket.org:org/repo.git", "org/repo"},
		{"https://custom.host/foo/bar.git", "custom.host/foo/bar"},
		{"ssh://git@github.com/sageox/ox.git", "sageox/ox"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, shortenRemoteURL(tt.input), "input: %s", tt.input)
	}
}

func TestBuildFileChangeMetadata(t *testing.T) {
	changes := []FileChange{
		{Path: "src/b.go", ChangeType: ChangeModified},
		{Path: "src/a.go", ChangeType: ChangeCreated},
	}
	meta := buildFileChangeMetadata(changes, "main", "/tmp/project")
	assert.Equal(t, "main", meta["branch"])
	assert.Equal(t, "/tmp/project", meta["worktree"])
	assert.Equal(t, "2", meta["file_count"])
	assert.Equal(t, "src/a.go,src/b.go", meta["files"])
}

// --- D. Agent ID resolution ---

type mockAgentResolver struct{ ids []string }

func (m *mockAgentResolver) ActiveAgentIDs() []string { return m.ids }

func TestResolveAgentID_NilResolver(t *testing.T) {
	p := NewFileChangeMurmurPublisher(
		NewChangeAccumulator(50*time.Millisecond), &mockMurmurPublisher{},
		"/tmp/ledger", "/tmp/project", slogDiscard(),
	)
	assert.Equal(t, "daemon", p.resolveAgentID())
}

func TestResolveAgentID_NoActiveAgents(t *testing.T) {
	p := NewFileChangeMurmurPublisher(
		NewChangeAccumulator(50*time.Millisecond), &mockMurmurPublisher{},
		"/tmp/ledger", "/tmp/project", slogDiscard(),
	)
	p.SetAgentResolver(&mockAgentResolver{ids: nil})
	assert.Equal(t, "daemon", p.resolveAgentID())
}

func TestResolveAgentID_SingleAgent(t *testing.T) {
	p := NewFileChangeMurmurPublisher(
		NewChangeAccumulator(50*time.Millisecond), &mockMurmurPublisher{},
		"/tmp/ledger", "/tmp/project", slogDiscard(),
	)
	p.SetAgentResolver(&mockAgentResolver{ids: []string{"Ox76PV"}})
	assert.Equal(t, "Ox76PV", p.resolveAgentID())
}

func TestResolveAgentID_TwoAgents(t *testing.T) {
	p := NewFileChangeMurmurPublisher(
		NewChangeAccumulator(50*time.Millisecond), &mockMurmurPublisher{},
		"/tmp/ledger", "/tmp/project", slogDiscard(),
	)
	p.SetAgentResolver(&mockAgentResolver{ids: []string{"Ox76PV", "OxAB12"}})
	assert.Equal(t, "Ox76PV,OxAB12", p.resolveAgentID())
}

func TestResolveAgentID_ThreeOrMoreAgents(t *testing.T) {
	p := NewFileChangeMurmurPublisher(
		NewChangeAccumulator(50*time.Millisecond), &mockMurmurPublisher{},
		"/tmp/ledger", "/tmp/project", slogDiscard(),
	)
	p.SetAgentResolver(&mockAgentResolver{ids: []string{"Ox76PV", "OxAB12", "OxCD34"}})
	assert.Equal(t, "Ox76PV,OxAB12,...", p.resolveAgentID())
}

func TestCollapseType(t *testing.T) {
	assert.Equal(t, ChangeType(""), collapseType(ChangeCreated, ChangeDeleted))
	assert.Equal(t, ChangeCreated, collapseType(ChangeCreated, ChangeModified))
	assert.Equal(t, ChangeModified, collapseType(ChangeDeleted, ChangeCreated))
	assert.Equal(t, ChangeDeleted, collapseType(ChangeModified, ChangeDeleted))
}
