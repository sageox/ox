package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deadPID names no running process.
const deadPID = 999999999

// pausedRoot starts a recording for agentID, run by agent process pid, and
// pauses it the way a person does.
func pausedRoot(t *testing.T, projectRoot, agentID string, pid int) {
	t.Helper()
	_, err := StartRecording(projectRoot, StartRecordingOptions{AgentID: agentID, AdapterName: "claude-code", Username: "person-a", ParentPID: pid})
	require.NoError(t, err)
	require.NoError(t, MarkExplicitPause(projectRoot, agentID, 1))
}

// The rule a launch chain's inherited pause holds by. Failure prevented: a
// chain released while the person's pause on its root still stood (/clear
// had removed the root's state for a moment), or held after the person
// resumed the root because a parent process up the chain still ran.
func TestInheritedPauseHoldsExactlyWhileTheRootsPersonSetPauseDoes(t *testing.T) {
	self, other := os.Getpid(), os.Getppid()
	cases := []struct {
		name   string
		setup  func(t *testing.T, projectRoot string)
		anchor PauseAnchor
		want   bool
	}{
		{"the root is recording and paused", func(t *testing.T, root string) { pausedRoot(t, root, "OxRoot", other) },
			PauseAnchor{RootAgentID: "OxRoot"}, true},
		{"a Cursor root, which names no process, holds by its state", func(t *testing.T, root string) { pausedRoot(t, root, "OxRoot", 0) },
			PauseAnchor{RootAgentID: "OxRoot"}, true},
		{"the root's state is gone during /clear but its process runs", func(t *testing.T, root string) {
			require.NoError(t, MarkExplicitPause(root, "OxRoot", 1))
		}, PauseAnchor{RootAgentID: "OxRoot", RootPID: other}, true},
		{"the root's state is gone and its process ended", func(t *testing.T, root string) {
			require.NoError(t, MarkExplicitPause(root, "OxRoot", 1))
		}, PauseAnchor{RootAgentID: "OxRoot", RootPID: deadPID}, false},
		{"the person resumed the root, though a process up the chain runs", func(t *testing.T, root string) {
			_, err := StartRecording(root, StartRecordingOptions{AgentID: "OxRoot", AdapterName: "claude-code", Username: "person-a", ParentPID: other})
			require.NoError(t, err)
		}, PauseAnchor{PID: other, RootAgentID: "OxRoot", RootPID: other}, false},
		{"the root's own pause is an inherited one, not a person's", func(t *testing.T, root string) {
			require.NoError(t, MarkInheritedPause(root, "OxRoot", PauseAnchor{PID: other}))
		}, PauseAnchor{RootAgentID: "OxRoot", RootPID: other}, false},
		{"a marker with no root holds while its process runs", nil, PauseAnchor{PID: other}, true},
		{"a marker with no root is never held by this process itself", nil, PauseAnchor{PID: self}, false},
		{"a marker with no root whose process ended holds nothing", nil, PauseAnchor{PID: deadPID}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			projectRoot := newPauseMarkerProject(t)
			if tc.setup != nil {
				tc.setup(t, projectRoot)
			}
			require.NoError(t, MarkInheritedPause(projectRoot, "OxChild", tc.anchor))
			assert.Equal(t, tc.want, InheritedPauseHeld(projectRoot, "OxChild", self))
		})
	}
}

// A pause a person asked for is not an inherited one: it never passes itself
// on as held through InheritedPauseHeld, and neither does an unreadable
// legacy marker or a missing one.
func TestOnlyAnInheritedMarkerIsHeldAsInherited(t *testing.T) {
	projectRoot := newPauseMarkerProject(t)
	require.NoError(t, MarkExplicitPause(projectRoot, "OxPerson", 1))
	legacy := filepath.Join(resolveSessionsWritePath(projectRoot), explicitPauseMarker+".OxLegacy")
	require.NoError(t, os.WriteFile(legacy, []byte("paused"), 0o600))

	assert.False(t, InheritedPauseHeld(projectRoot, "OxPerson", os.Getpid()))
	assert.False(t, InheritedPauseHeld(projectRoot, "OxLegacy", os.Getpid()))
	assert.False(t, InheritedPauseHeld(projectRoot, "OxMissing", os.Getpid()))
	assert.False(t, InheritedPauseHeld(projectRoot, "", os.Getpid()), "no agent names no pause")
	_, _, found := PeekExplicitPause(projectRoot, "OxLegacy")
	assert.True(t, found, "an unreadable legacy marker still reads as a pause")
}

// Failure prevented: a marker that names neither a process nor a root would
// read as a pause a person asked for, and pass for one forever.
func TestMarkInheritedPauseRefusesAPauseWithNoAnchor(t *testing.T) {
	projectRoot := newPauseMarkerProject(t)

	require.Error(t, MarkInheritedPause(projectRoot, "OxChild", PauseAnchor{}))

	_, _, found := PeekExplicitPause(projectRoot, "OxChild")
	assert.False(t, found, "a refused inherited pause wrote a marker")
}

// What a launcher passes down the chain. Failure prevented: a chain lost its
// root when it passed through a skipped child, or lost the root's process
// that carries it through the root's /clear.
func TestLaunchedPauseAnchorPassesTheRootDownTheChain(t *testing.T) {
	projectRoot := newPauseMarkerProject(t)
	parent, child := os.Getppid(), os.Getpid()
	require.NoError(t, MarkExplicitPause(projectRoot, "OxRoot", 1))

	fromRoot := LaunchedPauseAnchor(projectRoot, "OxRoot", parent)
	assert.Equal(t, "OxRoot", fromRoot.RootAgentID, "a person-paused launcher is the root")
	assert.Equal(t, parent, fromRoot.RootPID, "the root's process carries the chain through /clear")
	assert.Equal(t, parent, fromRoot.PID)

	require.NoError(t, MarkInheritedPause(projectRoot, "OxSkipped", fromRoot))
	fromSkipped := LaunchedPauseAnchor(projectRoot, "OxSkipped", 0)
	assert.Equal(t, fromRoot, fromSkipped, "a skipped child with no process of its own passes on what it inherited")

	ownProcess := LaunchedPauseAnchor(projectRoot, "OxSkipped", child)
	assert.Equal(t, child, ownProcess.PID, "a launcher's own process is the nearest one")
	assert.Equal(t, "OxRoot", ownProcess.RootAgentID)
	assert.Equal(t, parent, ownProcess.RootPID)

	assert.Equal(t, PauseAnchor{}, ProcessAnchorOf(0), "no process names no anchor")
}
