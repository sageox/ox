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

	require.Eventually(t, func() bool {
		p.drain()
		return p.PendingCount() == 2
	}, 2*time.Second, 10*time.Millisecond)
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

	require.Eventually(t, func() bool {
		p.drain()
		return p.PendingCount() == 1
	}, 2*time.Second, 10*time.Millisecond)

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
	require.Eventually(t, func() bool {
		p.drain()
		return p.PendingCount() == 1
	}, 2*time.Second, 10*time.Millisecond)

	// second batch — same file
	acc.AddEvent("src/main.go", fsnotify.Write, false)
	require.Eventually(t, func() bool {
		p.drain()
		return p.PendingCount() == 1
	}, 2*time.Second, 10*time.Millisecond)

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
	require.Eventually(t, func() bool {
		p.drain()
		return p.PendingCount() == 1
	}, 2*time.Second, 10*time.Millisecond)

	acc.AddEvent("tmp.go", fsnotify.Remove, false)
	require.Eventually(t, func() bool {
		p.drain()
		return p.PendingCount() == 0
	}, 2*time.Second, 10*time.Millisecond)

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
	assert.Equal(t, "new src/config.go, mod src/main.go", content)
}

// TestFormatSmallChanges_GroupsSameType verifies consecutive same-type files
// share one prefix: "mod a.go, b.go" not "mod a.go, mod b.go".
// Failure prevented: redundant type prefixes waste tokens in murmur output.
func TestFormatSmallChanges_GroupsSameType(t *testing.T) {
	changes := []FileChange{
		{Path: "lib/audio/recorder.cpp", ChangeType: ChangeModified},
		{Path: "lib/sys/app_flow.cpp", ChangeType: ChangeModified},
	}
	content := formatSmallChanges(changes)
	assert.Equal(t, "mod lib/audio/recorder.cpp, lib/sys/app_flow.cpp", content)
}

// TestFormatSmallChanges_MixedTypes verifies type prefix reappears when type changes.
func TestFormatSmallChanges_MixedTypes(t *testing.T) {
	changes := []FileChange{
		{Path: "a.go", ChangeType: ChangeCreated},
		{Path: "b.go", ChangeType: ChangeModified},
		{Path: "c.go", ChangeType: ChangeModified},
		{Path: "d.go", ChangeType: ChangeDeleted},
	}
	content := formatSmallChanges(changes)
	assert.Equal(t, "new a.go, del d.go, mod b.go, c.go", content)
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
	assert.Contains(t, content, "pkg0/(10M)")
}

// TestFormatLargeChanges_MixedTypes verifies change types are shown per directory.
// Failure prevented: large refactors show only counts, losing signal about adds vs deletes.
func TestFormatLargeChanges_MixedTypes(t *testing.T) {
	changes := []FileChange{
		{Path: "cmd/ox/new.go", ChangeType: ChangeCreated},
		{Path: "cmd/ox/old.go", ChangeType: ChangeDeleted},
		{Path: "cmd/ox/main.go", ChangeType: ChangeModified},
		{Path: "cmd/ox/root.go", ChangeType: ChangeModified},
	}
	// pad to >20 with other dirs
	for i := range 20 {
		changes = append(changes, FileChange{
			Path:       fmt.Sprintf("internal/pkg%d/file.go", i),
			ChangeType: ChangeModified,
		})
	}
	content := formatLargeChanges(changes)
	assert.Contains(t, content, "24 files")
	assert.Contains(t, content, "cmd/ox/(2M,1A,1D)")
}

// TestFormatLargeChanges_HugeCount routes 200+ files through the same formatter.
// Failure prevented: huge change sets still produce actionable dir-level summaries.
func TestFormatLargeChanges_HugeCount(t *testing.T) {
	var changes []FileChange
	for i := range 200 {
		changes = append(changes, FileChange{
			Path:       fmt.Sprintf("pkg%d/file%d.go", i%10, i),
			ChangeType: ChangeModified,
		})
	}
	content := formatLargeChanges(changes)
	assert.Contains(t, content, "200 files")
	assert.Contains(t, content, "20M")     // each dir has 20 modified
	assert.Contains(t, content, "+5 dirs") // top 5 shown, 5 remaining
}

func TestFormatFileChangeMurmur_IncludesBranchAndWorktree(t *testing.T) {
	changes := []FileChange{
		{Path: "src/main.go", ChangeType: ChangeModified},
	}
	content := formatFileChangeMurmur(changes, "ajit/feature-branch")
	assert.Contains(t, content, "[ajit/feature-branch]")
	assert.Contains(t, content, "mod src/main.go")
}

func TestFormatFileChangeMurmur_NoBranch(t *testing.T) {
	changes := []FileChange{
		{Path: "README.md", ChangeType: ChangeModified},
	}
	content := formatFileChangeMurmur(changes, "")
	assert.Equal(t, "mod README.md", content)
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

// --- D. File-change noise filtering ---

// TestIsFileChangeNoise verifies infrastructure paths are filtered from murmurs.
// Failure prevented: ledger writes and temp files pollute file-change murmurs.
func TestIsFileChangeNoise(t *testing.T) {
	tests := []struct {
		path  string
		noise bool
	}{
		// noise: paths outside project root (ledger/team-context)
		{"../../../.local/share/sageox/sageox.ai/ledgers/repo_abc/.gitignore", true},
		{"../../../.local/share/sageox/sageox.ai/ledgers/repo_abc/AGENTS.md", true},
		{"../foo/bar.go", true},

		// noise: atomic-write temp files
		{"internal/carts/queries.go.tmp.22532.1775148899215", true},
		{"cmd/ox/main.go.tmp.1234.5678", true},

		// noise: local tool state directories
		{".sageox/config.local.toml", true},
		{".sageox/config.json", true},
		{".beads/.local_version", true},
		{".beads/dolt/noms/manifest", true},
		{".claude/settings.json", true},
		{".codegraph/index.db", true},
		{".cursor/settings.json", true},

		// not noise: normal project files
		{"cmd/ox/main.go", false},
		{"internal/carts/queries.go", false},
		{"docs/guide.md", false},
		{"README.md", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := isFileChangeNoise(tt.path)
			assert.Equal(t, tt.noise, got, "isFileChangeNoise(%q)", tt.path)
		})
	}
}

// TestFilterFileChangeNoise verifies the batch filter removes noise and keeps real changes.
// Failure prevented: empty murmurs published when all changes are noise.
func TestFilterFileChangeNoise(t *testing.T) {
	changes := []FileChange{
		{Path: "cmd/ox/main.go", ChangeType: ChangeModified},
		{Path: "../../../.local/share/sageox/ledgers/repo/.gitignore", ChangeType: ChangeCreated},
		{Path: "internal/foo.go.tmp.123.456", ChangeType: ChangeCreated},
		{Path: "internal/foo.go", ChangeType: ChangeModified},
	}

	filtered := filterFileChangeNoise(changes)
	assert.Equal(t, 2, len(filtered))
	assert.Equal(t, "cmd/ox/main.go", filtered[0].Path)
	assert.Equal(t, "internal/foo.go", filtered[1].Path)
}
