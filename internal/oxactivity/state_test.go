package oxactivity

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testAgentID = "OxA1b2"

// Failure prevented: the hook that bumps a count and the status-line process
// that renders it disagree on where or how state is stored.
func TestState_RoundTrip(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	want := State{Discussions: 3, Plans: 1, Murmurs: 1, SeenRefs: []string{"plan\x00a"}, UpdatedAt: now}
	require.NoError(t, SaveState(root, testAgentID, want))

	path := filepath.Join(root, ".sageox", "cache", "statusline", testAgentID+".json")
	assert.Equal(t, path, StatePath(root, testAgentID))
	assert.FileExists(t, path)

	got := LoadState(root, testAgentID)
	assert.True(t, want.UpdatedAt.Equal(got.UpdatedAt))
	got.UpdatedAt = want.UpdatedAt
	assert.Equal(t, want, got)

	// A second save replaces, not merges.
	require.NoError(t, SaveState(root, testAgentID, State{Docs: 2}))
	assert.Equal(t, State{Docs: 2}, LoadState(root, testAgentID))

	// Agents are isolated from each other.
	assert.Equal(t, State{}, LoadState(root, "OxZz99"))
}

// TestLoadState_OldSchemaDecodesToZero reads a file written by the activity-
// feed status line. Failure prevented: upgrading ox leaves a stale
// "posted murmur" on the bar, or every refresh fails to decode the old file.
func TestLoadState_OldSchemaDecodesToZero(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	key := StateKey(testAgentID, "s1")
	path := StatePath(root, key)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	old := `{"last_action":"posted murmur","last_action_at":"2026-10-23T12:00:00Z",` +
		`"last_whisper_from":"160305533-ryan","last_whisper_topic":"wip",` +
		`"last_whisper_content":"Refactoring auth","last_whisper_at":"2026-10-23T11:59:00Z"}`
	require.NoError(t, os.WriteFile(path, []byte(old), 0o600))

	assert.Equal(t, State{}, LoadState(root, key))

	// And the next bump starts counting from zero on top of it.
	require.NoError(t, RecordInfluence(root, testAgentID, "s1", "plan", "my-plan"))
	got := LoadState(root, key)
	assert.Equal(t, 1, got.Plans)
	assert.Equal(t, Counts{Plans: 1}, got.Counts())
}

// Failure prevented: a half-written, corrupt, or unreadable state file breaks
// the status line or a hook instead of degrading to "nothing to show".
func TestLoadState_DegradesToZero(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{"missing", func(t *testing.T, path string) {}},
		{"corrupt", func(t *testing.T, path string) {
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			require.NoError(t, os.WriteFile(path, []byte(`{"plans": 1`), 0o600))
		}},
		{"wrong shape", func(t *testing.T, path string) {
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			require.NoError(t, os.WriteFile(path, []byte(`{"plans": "many"}`), 0o600))
		}},
		{"directory in place of file", func(t *testing.T, path string) {
			require.NoError(t, os.MkdirAll(path, 0o755))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			tt.setup(t, StatePath(root, testAgentID))
			assert.Equal(t, State{}, LoadState(root, testAgentID))
		})
	}
}

// Failure prevented: an agent ID from hook input names a path outside the
// state directory ("../../etc/x").
func TestState_InvalidAgentID(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	for _, id := range []string{"", "../../x", "Ox/../a", "OxA1b2.", "OxA1b2.a/b", "OxA1b2..x", "abc", "OxA1b2c"} {
		assert.Empty(t, StatePath(root, id), id)
		err := SaveState(root, id, State{Plans: 1})
		require.ErrorIs(t, err, ErrInvalidAgentID, id)
		assert.Equal(t, State{}, LoadState(root, id), id)
	}
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	assert.Empty(t, entries, "invalid IDs must not create anything")
}

// TestStateKey scopes state to a harness session and keeps every key a safe
// file name. Failure prevented: one window's counts shown in another, or a
// session ID escaping the cache directory.
func TestStateKey(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "OxA1b2", StateKey("OxA1b2", ""))
	assert.Equal(t, "OxA1b2", StateKey("OxA1b2", "  "))
	assert.Equal(t, "OxA1b2.6f1c2e3d-9a8b-4c5d-8e7f-000000000001", StateKey("OxA1b2", "6f1c2e3d-9a8b-4c5d-8e7f-000000000001"))

	hashed := StateKey("OxA1b2", "../escape/../../x")
	assert.Regexp(t, `^OxA1b2\.[0-9a-f]{16}$`, hashed)
	assert.Equal(t, hashed, StateKey("OxA1b2", "../escape/../../x"), "deterministic")

	root := t.TempDir()
	for _, key := range []string{StateKey("OxA1b2", "s1"), hashed} {
		p := StatePath(root, key)
		require.NotEmpty(t, p)
		assert.Equal(t, filepath.Join(root, ".sageox", "cache", "statusline"), filepath.Dir(p))
	}
	require.NoError(t, SaveState(root, StateKey("OxA1b2", "s1"), State{Plans: 1}))
	require.NoError(t, SaveState(root, StateKey("OxA1b2", "s2"), State{Plans: 2}))
	assert.Equal(t, 1, LoadState(root, StateKey("OxA1b2", "s1")).Plans)
	assert.Equal(t, 2, LoadState(root, StateKey("OxA1b2", "s2")).Plans)
	assert.Equal(t, State{}, LoadState(root, "OxA1b2"), "agent-wide key does not see session state")
}

// TestSaveState_PrunesStaleSessions removes files idle past StateRetention
// and keeps the rest. Failure prevented: one file per Claude Code session
// accumulating forever in the cache.
func TestSaveState_PrunesStaleSessions(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	old := StateKey("OxA1b2", "old")
	fresh := StateKey("OxA1b2", "fresh")
	require.NoError(t, SaveState(root, old, State{Plans: 1}))
	require.NoError(t, SaveState(root, fresh, State{Plans: 1}))
	require.NoError(t, SaveState(root, "OxA1b2", State{Plans: 1}))
	other := filepath.Join(filepath.Dir(StatePath(root, old)), "murmurs.json")
	require.NoError(t, os.WriteFile(other, []byte("{}"), 0o600))
	stale := time.Now().Add(-StateRetention - time.Hour)
	for _, p := range []string{StatePath(root, old), StatePath(root, "OxA1b2"), other} {
		require.NoError(t, os.Chtimes(p, stale, stale))
	}

	require.NoError(t, SaveState(root, StateKey("OxA1b2", "new"), State{Plans: 1}))
	assert.NoFileExists(t, StatePath(root, old))
	assert.FileExists(t, StatePath(root, "OxA1b2"), "agent-wide state is not a session file")
	assert.FileExists(t, other, "only session state files are pruned")
	assert.FileExists(t, StatePath(root, fresh))
	assert.FileExists(t, StatePath(root, StateKey("OxA1b2", "new")))
}

// Failure prevented: SaveState reports success when it could not write.
func TestSaveState_SurfacesWriteFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// A regular file where the .sageox directory must be: MkdirAll fails everywhere.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".sageox"), []byte("x"), 0o600))
	assert.Error(t, SaveState(root, testAgentID, State{Plans: 1}))
}

// Failure prevented: SaveState reports success when the directory exists but
// the file itself cannot be written. A directory where the state file must go
// defeats the atomic rename on every platform, unlike permissions.
func TestSaveState_SurfacesFileWriteFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(StatePath(root, testAgentID), 0o755))
	assert.Error(t, SaveState(root, testAgentID, State{Plans: 1}))
}

// TestState_BumpDedupsRefs counts each item once per session and routes every
// ref_type to its bucket. Failure prevented: an AI coworker re-reading one
// plan five times shows "5 plans", or a doc-kind ref lands in no bucket.
func TestState_BumpDedupsRefs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		bumps [][2]string
		want  Counts
		seen  int
	}{
		{"discussion", [][2]string{{"discussion", "cnv_1"}}, Counts{Discussions: 1}, 1},
		{"plan", [][2]string{{"plan", "p"}}, Counts{Plans: 1}, 1},
		{"doc kinds share Docs", [][2]string{{"doc", "a"}, {"team-context", "b"}, {"kb", "c"}, {"skill", "d"}}, Counts{Docs: 4}, 4},
		{"session", [][2]string{{"session", "s"}}, Counts{Sessions: 1}, 1},
		{"murmur", [][2]string{{"murmur", "m"}}, Counts{Murmurs: 1}, 1},
		{"same ref twice counts once", [][2]string{{"plan", "p"}, {"plan", "p"}, {"plan", " p "}}, Counts{Plans: 1}, 1},
		{"same ref, different type counts in each", [][2]string{{"plan", "x"}, {"discussion", "x"}}, Counts{Discussions: 1, Plans: 1}, 2},
		{"unknown type is ignored", [][2]string{{"query", "auth"}, {"code", "x"}, {"decision", "d"}, {"", "x"}}, Counts{}, 0},
		{"empty ref is ignored", [][2]string{{"plan", ""}, {"plan", "  "}}, Counts{}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var s State
			for _, b := range tt.bumps {
				s.Bump(b[0], b[1])
			}
			assert.Equal(t, tt.want, s.Counts())
			assert.Len(t, s.SeenRefs, tt.seen)
		})
	}

	t.Run("reports change", func(t *testing.T) {
		t.Parallel()
		var s State
		assert.True(t, s.Bump("plan", "p"))
		assert.False(t, s.Bump("plan", "p"))
		assert.False(t, s.Bump("nope", "p"))
	})

	t.Run("seen refs capped, newest kept", func(t *testing.T) {
		t.Parallel()
		var s State
		for i := range MaxSeenRefs + 10 {
			require.True(t, s.Bump("discussion", fmt.Sprintf("cnv_%d", i)))
		}
		assert.Equal(t, MaxSeenRefs+10, s.Discussions)
		require.Len(t, s.SeenRefs, MaxSeenRefs)
		assert.Equal(t, "discussion\x00cnv_10", s.SeenRefs[0], "oldest dropped first")
		assert.Equal(t, fmt.Sprintf("discussion\x00cnv_%d", MaxSeenRefs+9), s.SeenRefs[MaxSeenRefs-1])
		assert.False(t, s.Bump("discussion", fmt.Sprintf("cnv_%d", MaxSeenRefs+9)), "a kept ref still dedups")
	})
}

// TestRecordInfluence_CreatesFile bumps a session with no state yet. Failure
// prevented: the first use-evidence write of a session errors on the missing
// file and the bar never shows anything.
func TestRecordInfluence_CreatesFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	key := StateKey(testAgentID, "claude-1")
	require.NoFileExists(t, StatePath(root, key))

	require.NoError(t, RecordInfluence(root, testAgentID, "claude-1", "discussion", "cnv_a"))
	require.NoError(t, RecordInfluence(root, testAgentID, "claude-1", "discussion", "cnv_a"))
	require.NoError(t, RecordInfluence(root, testAgentID, "claude-1", "murmur", "m1"))

	got := LoadState(root, key)
	assert.Equal(t, Counts{Discussions: 1, Murmurs: 1}, got.Counts())
	assert.False(t, got.UpdatedAt.IsZero())
}

// TestRecordInfluence_UnknownTypeNoop: a ref_type the line has no bucket for
// (query, code, decision) writes nothing. Failure prevented: an `ox query`
// creating a state file — or worse, a count — for a search that pulled no item.
func TestRecordInfluence_UnknownTypeNoop(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, rt := range []string{"query", "code", "decision", ""} {
		require.NoError(t, RecordInfluence(root, testAgentID, "s", rt, "x"), rt)
	}
	assert.NoDirExists(t, filepath.Join(root, ".sageox"))
}

// TestRecordInfluence_EmptyRefNoop: without an item ID there is nothing to
// dedup, so nothing is counted. Failure prevented: every `ox plan view` with
// no slug inflating "plans".
func TestRecordInfluence_EmptyRefNoop(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, RecordInfluence(root, testAgentID, "s", "plan", ""))
	require.NoError(t, RecordInfluence(root, testAgentID, "s", "plan", "   "))
	assert.NoDirExists(t, filepath.Join(root, ".sageox"))

	// Invalid agent IDs still surface as errors once there is something to write.
	assert.ErrorIs(t, RecordInfluence(root, "../x", "s", "plan", "p"), ErrInvalidAgentID)
}

// TestRecordInfluence_PerSessionKey writes under the harness session's key.
// Failure prevented: a bump from one Claude Code window showing on another's
// bar, or landing in a file no status line reads.
func TestRecordInfluence_PerSessionKey(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, RecordInfluence(root, testAgentID, "win-a", "plan", "p1"))
	require.NoError(t, RecordInfluence(root, testAgentID, "win-b", "plan", "p1"))
	require.NoError(t, RecordInfluence(root, testAgentID, "win-b", "plan", "p2"))

	assert.Equal(t, 1, LoadState(root, StateKey(testAgentID, "win-a")).Plans)
	assert.Equal(t, 2, LoadState(root, StateKey(testAgentID, "win-b")).Plans)
	assert.Equal(t, State{}, LoadState(root, testAgentID), "never agent-wide when a session ID is given")
}

// TestUpdateState_ConcurrentWritersKeepEveryBump: hooks bumping different
// refs at the same time must not lose any. Failure prevented: the bar shows
// fewer items than were used because a later writer restored a stale snapshot.
func TestUpdateState_ConcurrentWritersKeepEveryBump(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			_ = RecordInfluence(root, testAgentID, "", "plan", fmt.Sprintf("p%d", i))
		}(i)
		go func(i int) {
			defer wg.Done()
			_ = RecordInfluence(root, testAgentID, "", "discussion", fmt.Sprintf("cnv_%d", i))
		}(i)
	}
	wg.Wait()

	assert.Equal(t, Counts{Discussions: 20, Plans: 20}, LoadState(root, testAgentID).Counts())
}

// TestUpdateState_NoChangeSkipsWrite: a no-op update must not touch the file.
func TestUpdateState_NoChangeSkipsWrite(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, UpdateState(root, testAgentID, func(s *State) bool { return false }))
	assert.NoFileExists(t, StatePath(root, testAgentID))
	assert.Error(t, UpdateState(root, "bad id", func(s *State) bool { return true }))
}

const (
	testCnv  = "cnv_019ffe10-0000-7000-8000-000000000011"
	testCnv2 = "cnv_019ffe10-0000-7000-8000-000000000022"
)

// TestNormalizeRef: the retrieval hook and the use-evidence writers must name
// one item the same way. Failure prevented: a fetched discussion that is then
// used shows as two discussions because one path saw a link and the other an ID.
func TestNormalizeRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, refType, ref, want string
	}{
		{"bare conversation id", "discussion", testCnv, testCnv},
		{"sageox.ai link", "discussion", "https://sageox.ai/c/" + testCnv, testCnv},
		{"sageox uri with cue", "discussion", "sageox://team/conversation/" + testCnv + "#t=12", testCnv},
		{"team-ctx slug", "team-context", "team-ctx/acme", TeamContextRef},
		{"team-ctx retrieval ref", "team-context", TeamContextRef, TeamContextRef},
		{"plan slug trimmed", "plan", "  my-plan ", "my-plan"},
		{"doc path kept", "doc", "docs/adr/001.md", "docs/adr/001.md"},
		{"empty", "team-context", "  ", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, NormalizeRef(tt.refType, tt.ref))
		})
	}
}

// TestState_MarkUsed counts each used item once, alongside — never instead
// of — its kind. Failure prevented: one fetch plus one use of the same
// discussion renders as "2 discussions", re-reporting a use inflates "used",
// or an item used straight from a whisper never shows its kind.
func TestState_MarkUsed(t *testing.T) {
	t.Parallel()

	type op struct {
		use          bool
		refType, ref string
	}
	fetch := func(rt, ref string) op { return op{false, rt, ref} }
	use := func(rt, ref string) op { return op{true, rt, ref} }

	tests := []struct {
		name string
		ops  []op
		want Counts
	}{
		{"fetch then use is one item, one use", []op{fetch("discussion", testCnv), use("discussion", testCnv)}, Counts{Discussions: 1, Used: 1}},
		{"use without fetch still counts its kind", []op{use("murmur", "m1")}, Counts{Murmurs: 1, Used: 1}},
		{"same use reported twice counts once", []op{use("plan", "p"), use("plan", "p"), use("plan", " p ")}, Counts{Plans: 1, Used: 1}},
		{"distinct uses each count", []op{use("discussion", testCnv), use("discussion", testCnv2), use("doc", "a.md")}, Counts{Discussions: 2, Docs: 1, Used: 3}},
		{"link and bare id dedup", []op{fetch("discussion", testCnv), use("discussion", "https://sageox.ai/c/"+testCnv), use("discussion", testCnv)}, Counts{Discussions: 1, Used: 1}},
		{"team-ctx slug dedups with the retrieval ref", []op{fetch("team-context", TeamContextRef), use("team-context", "team-ctx/acme")}, Counts{Docs: 1, Used: 1}},
		{"use before fetch", []op{use("session", "ses_1"), fetch("session", "ses_1")}, Counts{Sessions: 1, Used: 1}},
		{"unknown type is ignored", []op{use("query", "auth"), use("", "x")}, Counts{}},
		{"empty ref is ignored", []op{use("plan", ""), use("plan", " ")}, Counts{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var s State
			for _, o := range tt.ops {
				if o.use {
					s.MarkUsed(o.refType, o.ref)
				} else {
					s.Bump(o.refType, o.ref)
				}
			}
			assert.Equal(t, tt.want, s.Counts())
			assert.Len(t, s.UsedRefs, tt.want.Used)
		})
	}

	t.Run("reports change", func(t *testing.T) {
		t.Parallel()
		var s State
		require.True(t, s.Bump("plan", "p"))
		assert.True(t, s.MarkUsed("plan", "p"), "first use of a fetched item is a change")
		assert.False(t, s.MarkUsed("plan", "p"))
		assert.False(t, s.MarkUsed("nope", "p"))
	})

	t.Run("used refs capped, newest kept", func(t *testing.T) {
		t.Parallel()
		var s State
		for i := range MaxSeenRefs + 5 {
			require.True(t, s.MarkUsed("doc", fmt.Sprintf("d%d", i)))
		}
		assert.Equal(t, MaxSeenRefs+5, s.Used)
		require.Len(t, s.UsedRefs, MaxSeenRefs)
		assert.Equal(t, "doc\x00d5", s.UsedRefs[0], "oldest dropped first")
		assert.False(t, s.MarkUsed("doc", fmt.Sprintf("d%d", MaxSeenRefs+4)), "a kept ref still dedups")
	})
}

// TestRecordUse_PersistsAndDedups drives the on-disk path the commands use.
// Failure prevented: `ox agent <id> context used` run twice for one item
// shows "2 used", or the use lands in a file no status line reads.
func TestRecordUse_PersistsAndDedups(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	key := StateKey(testAgentID, "claude-1")

	require.NoError(t, RecordInfluence(root, testAgentID, "claude-1", "discussion", testCnv))
	require.NoError(t, RecordUse(root, testAgentID, "claude-1", "discussion", testCnv))
	require.NoError(t, RecordUse(root, testAgentID, "claude-1", "discussion", "https://sageox.ai/c/"+testCnv))
	require.NoError(t, RecordUse(root, testAgentID, "claude-1", "team-context", "team-ctx/acme"))

	got := LoadState(root, key)
	assert.Equal(t, Counts{Discussions: 1, Docs: 1, Used: 2}, got.Counts())
	assert.False(t, got.UpdatedAt.IsZero())
	// rendering of Used lands with the status line change
	assert.Equal(t, State{}, LoadState(root, StateKey(testAgentID, "claude-2")), "other sessions untouched")
}

// TestRecordUse_Noops mirror RecordInfluence's rules. Failure prevented: an
// `--ref-type query` use or an empty ref creating state or a count.
func TestRecordUse_Noops(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, rt := range []string{"query", "code", ""} {
		require.NoError(t, RecordUse(root, testAgentID, "s", rt, "x"), rt)
	}
	require.NoError(t, RecordUse(root, testAgentID, "s", "plan", "  "))
	assert.NoDirExists(t, filepath.Join(root, ".sageox"))

	assert.ErrorIs(t, RecordUse(root, "../x", "s", "plan", "p"), ErrInvalidAgentID)
	assert.ErrorContains(t, RecordUse("", testAgentID, "s", "plan", "p"), "record use: empty project root")
}

// TestLoadState_PreUsedSchema reads a file written before the used count
// existed. Failure prevented: upgrading ox loses the session's retrieval counts
// or fails to decode, blanking the bar.
func TestLoadState_PreUsedSchema(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	key := StateKey(testAgentID, "s1")
	path := StatePath(root, key)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	old := `{"discussions":2,"plans":1,"seen_refs":["discussion\u0000a","discussion\u0000b","plan\u0000p"],"updated_at":"2026-10-23T12:00:00Z"}`
	require.NoError(t, os.WriteFile(path, []byte(old), 0o600))

	got := LoadState(root, key)
	assert.Equal(t, Counts{Discussions: 2, Plans: 1}, got.Counts())
	assert.Empty(t, got.UsedRefs)

	require.NoError(t, RecordUse(root, testAgentID, "s1", "plan", "p"))
	assert.Equal(t, Counts{Discussions: 2, Plans: 1, Used: 1}, LoadState(root, key).Counts())
}
