package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The pause a person sets on an agent reaches every agent launched down its
// chain (#1987). Review found that rule one edge at a time across five rounds,
// each fix pinned by a test for its own case. This matrix covers the class:
// every combination of what the root is, what became of it, whether the
// person's pause still stands, and how far down the chain the new agent is.

type matrixRoot string

const (
	matrixClaudeRoot matrixRoot = "claude-code" // records its agent process
	matrixCursorRoot matrixRoot = "cursor"      // records no process: its GUI is shared by every conversation
)

type matrixLauncher string

const (
	launcherAlive    matrixLauncher = "alive"      // root's recording and process both run
	launcherExited   matrixLauncher = "exited"     // root's agent process ended
	launcherMidClear matrixLauncher = "mid-clear"  // /clear removed root's recording state; marker and process remain
	launcherReused   matrixLauncher = "pid-reused" // root's PID now names a different process
)

type matrixDepth string

const (
	depthChild      matrixDepth = "child"      // launched from the root's shell
	depthGrandchild matrixDepth = "grandchild" // launched from a skipped child's shell
)

type matrixCell struct {
	root     matrixRoot
	launcher matrixLauncher
	pauseSet bool // false: the person resumed the root after the chain started
	depth    matrixDepth
}

func (c matrixCell) name() string {
	pause := "pause-set"
	if !c.pauseSet {
		pause = "pause-unset"
	}
	return string(c.root) + "/" + string(c.launcher) + "/" + pause + "/" + string(c.depth)
}

// skippedByConstruction names why a cell cannot exist, or "" when it can.
func (c matrixCell) skippedByConstruction() string {
	if c.root != matrixCursorRoot {
		return ""
	}
	switch c.launcher {
	case launcherMidClear:
		return "a Cursor root names no process to carry the chain through /clear, so the gap is indistinguishable from the root ending"
	case launcherReused:
		return "a Cursor root records no PID, so there is none to reuse"
	}
	return ""
}

// agentRecords is the rule under test, stated once: an agent in the chain
// stays paused exactly while the person's pause on the root still stands and
// the root lives. The root lives while its recording state does, or, across
// /clear's gap, while the process it was recorded under still runs as the same
// process.
func (c matrixCell) agentRecords() bool {
	if !c.pauseSet {
		return true
	}
	var stateAlive, processLive bool
	switch c.launcher {
	case launcherAlive:
		stateAlive, processLive = true, c.root == matrixClaudeRoot
	case launcherMidClear:
		stateAlive, processLive = false, true
	case launcherExited, launcherReused:
		stateAlive, processLive = false, false
	}
	if c.root == matrixCursorRoot && c.launcher == launcherExited {
		// a Cursor root ends by its recording state going away
		stateAlive = false
	}
	return !stateAlive && !processLive
}

func TestPauseChainMatrix(t *testing.T) {
	roots := []matrixRoot{matrixClaudeRoot, matrixCursorRoot}
	launchers := []matrixLauncher{launcherAlive, launcherExited, launcherMidClear, launcherReused}
	depths := []matrixDepth{depthChild, depthGrandchild}
	for _, root := range roots {
		for _, launcher := range launchers {
			for _, pauseSet := range []bool{true, false} {
				for _, depth := range depths {
					cell := matrixCell{root: root, launcher: launcher, pauseSet: pauseSet, depth: depth}
					t.Run(cell.name(), func(t *testing.T) {
						if reason := cell.skippedByConstruction(); reason != "" {
							t.Skip("skipped by construction: " + reason)
						}
						runPauseChainCell(t, cell)
					})
				}
			}
		}
	}
}

func runPauseChainCell(t *testing.T, cell matrixCell) {
	projectRoot, repoID := setupTestProject(t)
	enableRecordingWithoutLedgerClone(t)

	// the root's agent process: a real one, so that it can end
	rootProc := exec.Command("sleep", "600")
	require.NoError(t, rootProc.Start())
	t.Cleanup(func() { _ = rootProc.Process.Kill(); _ = rootProc.Wait() })
	rootPID := 0
	if cell.root == matrixClaudeRoot {
		rootPID = rootProc.Process.Pid
	}
	createActiveRecordingWithPID(t, projectRoot, repoID, "OxRoot", rootPID)
	rootState, err := session.LoadRecordingStateForAgent(projectRoot, "OxRoot")
	require.NoError(t, err)
	require.NoError(t, session.MarkExplicitPause(projectRoot, "OxRoot", 1, session.PersonProcessOf(rootState)))

	launch := func(launcherID, agentID, agentType string) *sessionStatus {
		t.Helper()
		t.Setenv("SAGEOX_AGENT_ID", launcherID)
		t.Setenv("OX_PARENT_PID", strconv.Itoa(os.Getpid()))
		parent := inheritedParentOf(projectRoot, agentID)
		return startSessionRecordingUnlocked(projectRoot, agentID, agentType, parent, "", agentID+"-session", nil)
	}

	// the chain starts while the root is alive and paused
	probeLauncher := "OxRoot"
	if cell.depth == depthGrandchild {
		child := launch("OxRoot", "OxChild", string(cell.root))
		require.NotNil(t, child)
		require.False(t, child.Recording, "harness: a paused root's child skips recording")
		probeLauncher = "OxChild"
	}

	// then the root's fate and the person's choice change
	switch cell.launcher {
	case launcherAlive:
	case launcherExited:
		if cell.root == matrixClaudeRoot {
			require.NoError(t, rootProc.Process.Kill())
			_ = rootProc.Wait()
		} else {
			require.NoError(t, session.ClearRecordingStateForAgent(projectRoot, "OxRoot"))
		}
	case launcherMidClear:
		require.NoError(t, session.ClearRecordingStateForAgent(projectRoot, "OxRoot"))
	case launcherReused:
		reuseRootPID(t, projectRoot, repoID, "OxRoot", "OxChild")
	}
	if !cell.pauseSet {
		require.True(t, session.ClearExplicitPause(projectRoot, "OxRoot"), "harness: the root had a pause to resume")
	}

	probe := launch(probeLauncher, "OxProbe", "claude-code")

	require.NotNil(t, probe)
	want := cell.agentRecords()
	assert.Equal(t, want, probe.Recording, "records=%v wanted for %s", want, cell.name())
	state, err := session.LoadRecordingStateForAgent(projectRoot, "OxProbe")
	require.NoError(t, err)
	assert.Equal(t, want, state != nil, "a paused agent must leave no recording state")
}

// reuseRootPID makes the root's PID name a different process than the one
// every record of it was taken from: the recording's stored start time and the
// start times the chain's markers carry.
func reuseRootPID(t *testing.T, projectRoot, repoID, rootID, childID string) {
	t.Helper()
	const otherStart = 1
	require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, rootID, func(s *session.RecordingState) {
		s.ParentStartTime = otherStart
	}))
	// the root's own person-set marker names its process too, under its own keys
	for _, id := range []string{rootID, childID} {
		marker := filepath.Join(session.GetContextPath(repoID), "sessions", ".session_paused."+id)
		data, err := os.ReadFile(marker)
		if os.IsNotExist(err) {
			continue // depth child: no chain marker yet
		}
		require.NoError(t, err)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(data, &payload))
		for _, key := range []string{"pid_start", "root_pid_start", "person_pid_start"} {
			if _, ok := payload[key]; ok {
				payload[key] = otherStart
			}
		}
		data, err = json.Marshal(payload)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(marker, data, 0o600))
	}
}
