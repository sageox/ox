package session

import (
	"encoding/json"
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

// A person's pause names the paused agent's process, so a child launched while
// that agent's recording state is briefly gone (/clear) still finds the root
// paused. Failure prevented: the direct child of a paused root recording in
// the /clear gap; and an old marker, or a root that has exited, still holding.
func TestPausedRootAnchorHoldsOnlyWhileThePausedProcessRuns(t *testing.T) {
	self := os.Getpid()
	cases := []struct {
		name  string
		setup func(t *testing.T, projectRoot string)
		want  bool
	}{
		{"a person paused a running agent", func(t *testing.T, root string) { pausedRoot(t, root, "OxRoot", self) }, true},
		{"the paused agent has exited", func(t *testing.T, root string) { pausedRoot(t, root, "OxRoot", deadPID) }, false},
		{"a marker from before processes were recorded", func(t *testing.T, root string) {
			require.NoError(t, writePauseMarker(root, "OxRoot", pauseMarkerPayload{Seq: 1}))
		}, false},
		{"an inherited marker is not a person's pause", func(t *testing.T, root string) {
			own := ProcessAnchorOf(self)
			require.NoError(t, writePauseMarker(root, "OxRoot", pauseMarkerPayload{Seq: 1, PID: own.PID, PIDStart: own.Start}))
		}, false},
		{"nothing is paused", func(t *testing.T, root string) {}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			projectRoot := newPauseMarkerProject(t)
			tc.setup(t, projectRoot)
			anchor, held := PausedRootAnchor(projectRoot, "OxRoot")
			assert.Equal(t, tc.want, held)
			if held {
				assert.Equal(t, self, anchor.PID)
			}
		})
	}
}

// Failure prevented: a leftover recording whose PID the OS handed to another
// process made the pause save the replacement's start time, so the pause held
// for as long as that stranger ran. A pause records the paused agent's process
// only when it is still that process.
func TestPauseOnARecordingWhosePIDWasReusedAnchorsNothing(t *testing.T) {
	projectRoot := newPauseMarkerProject(t)
	_, err := StartRecording(projectRoot, StartRecordingOptions{AgentID: "OxRoot", AdapterName: "claude-code", Username: "person-a", ParentPID: os.Getpid()})
	require.NoError(t, err)
	require.NoError(t, UpdateRecordingStateForAgent(projectRoot, "OxRoot", func(s *RecordingState) { s.ParentStartTime = 1 }))
	require.NoError(t, MarkExplicitPause(projectRoot, "OxRoot", 1))

	_, held := PausedRootAnchor(projectRoot, "OxRoot")
	assert.False(t, held, "a reused PID anchored a pause")
}

// Failure prevented: a person-set pause stored its process under pid, which an
// older ox reads as an inherited pause and keeps holding after the root resumes.
func TestPersonPauseStoresItsProcessUnderKeysOlderReadersIgnore(t *testing.T) {
	projectRoot := newPauseMarkerProject(t)
	pausedRoot(t, projectRoot, "OxRoot", os.Getpid())

	data, err := os.ReadFile(filepath.Join(resolveSessionsWritePath(projectRoot), explicitPauseMarker+".OxRoot"))
	require.NoError(t, err)
	var keys map[string]any
	require.NoError(t, json.Unmarshal(data, &keys))
	assert.NotContains(t, keys, "pid")
	assert.NotContains(t, keys, "pid_start")
	assert.Contains(t, keys, "person_pid")
}

// Failure prevented: the chain's anchor re-sampled the PID's start time after
// it was validated, so a PID reused in between was recorded as the root.
func TestLaunchedPauseAnchorKeepsTheValidatedStartTime(t *testing.T) {
	projectRoot := newPauseMarkerProject(t)
	anchor := LaunchedPauseAnchorFrom(projectRoot, "OxRoot", PauseAnchor{PID: os.Getpid(), Start: 12345})
	assert.Equal(t, int64(12345), anchor.RootStart)
	assert.Equal(t, int64(12345), anchor.Start)
}
