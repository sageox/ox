package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const cursorMixedSourceAgentID = "OxCurWrite"

type cursorMixedSourceFixture struct {
	projectRoot string
	sourcePath  string
	rawPath     string
	state       *session.RecordingState
	source      []byte
	raw         []byte
}

func newCursorMixedSourceFixture(t *testing.T, projectRoot string) *cursorMixedSourceFixture {
	t.Helper()
	source := []byte(`{"role":"user","message":{"content":[{"type":"text","text":"native Cursor source"}]}}` + "\n")
	sourcePath := filepath.Join(t.TempDir(), "cursor-native.jsonl")
	require.NoError(t, os.WriteFile(sourcePath, source, 0o600))
	hash := sha256.Sum256(source)
	state, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
		AgentID:            cursorMixedSourceAgentID,
		AgentSessionID:     "123e4567-e89b-12d3-a456-426614174099",
		AdapterName:        "cursor",
		SessionFile:        sourcePath,
		WorkspacePath:      projectRoot,
		WatchMode:          "tail",
		StartOffset:        int64(len(source)),
		StartOffsetKnown:   true,
		SourcePrefixSHA256: hex.EncodeToString(hash[:]),
	})
	require.NoError(t, err)
	raw := []byte(`{"type":"assistant","content":"already captured"}` + "\n")
	rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
	require.NoError(t, os.WriteFile(rawPath, raw, 0o600))
	require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, state.AgentID, func(current *session.RecordingState) {
		current.EntryCount = 1
	}))
	state, err = session.LoadRecordingStateForAgent(projectRoot, state.AgentID)
	require.NoError(t, err)
	require.NotNil(t, state)
	return &cursorMixedSourceFixture{
		projectRoot: projectRoot, sourcePath: sourcePath, rawPath: rawPath,
		state: state, source: source, raw: raw,
	}
}

func (f *cursorMixedSourceFixture) assertUnchanged(t *testing.T) {
	t.Helper()
	source, err := os.ReadFile(f.sourcePath)
	require.NoError(t, err)
	assert.Equal(t, f.source, source, "native Cursor JSONL must never receive ox-owned entries")
	raw, err := os.ReadFile(f.rawPath)
	require.NoError(t, err)
	assert.Equal(t, f.raw, raw, "saved Session must remain daemon-owned")
	state, err := session.LoadRecordingStateForAgent(f.projectRoot, f.state.AgentID)
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, f.state.EntryCount, state.EntryCount)
	assert.Equal(t, f.state.StartOffset, state.StartOffset)
	assert.Equal(t, f.state.SourceOffset, state.SourceOffset)
	assert.Equal(t, f.state.SourcePrefixSHA256, state.SourcePrefixSHA256)
}

func chdirCursorFixture(t *testing.T, projectRoot string) {
	t.Helper()
	original, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(projectRoot))
	t.Cleanup(func() { require.NoError(t, os.Chdir(original)) })
}

func TestCursorSessionLogRejectsMixedSourceWriteBeforeMutation(t *testing.T) {
	cfg = &config.Config{}
	projectRoot := setupSessionTestProject(t)
	fixture := newCursorMixedSourceFixture(t, projectRoot)
	chdirCursorFixture(t, projectRoot)

	err := runAgentSessionLog(io.Discard, &agentinstance.Instance{AgentID: fixture.state.AgentID}, []string{
		"--role", "assistant", "--content", "must not enter native source",
	})
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "unsupported")
	assert.Contains(t, strings.ToLower(err.Error()), "cursor")
	fixture.assertUnchanged(t)
}

func TestCursorCoworkerLoadStillDeliversContentWithoutSessionWrite(t *testing.T) {
	teamRoot := filepath.Join(t.TempDir(), "team-context")
	projectRoot, _ := setupCoworkerProject(t, "cursor-team", []config.TeamContext{{
		TeamID: "cursor-team", TeamName: "Cursor Team", Path: teamRoot,
	}})
	cacheRoot := t.TempDir()
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("HOME", cacheRoot)
	t.Setenv("XDG_CACHE_HOME", cacheRoot)
	require.NoError(t, config.SaveProjectConfig(projectRoot, &config.ProjectConfig{
		ConfigVersion: config.CurrentConfigVersion, RepoID: "cursor-guard-repo", TeamID: "cursor-team",
	}))
	fixture := newCursorMixedSourceFixture(t, projectRoot)
	agentPath := filepath.Join(teamRoot, "coworkers", "agents", "reviewer.md")
	require.NoError(t, os.WriteFile(agentPath, []byte("---\ndescription: Reviews Cursor changes\nmodel: sonnet\n---\n\nCURSOR_COWORKER_CONTENT\n"), 0o600))
	t.Setenv("CLAUDE_CODE_SESSION_ID", "cursor-mixed-source-test")
	t.Setenv("SAGEOX_AGENT_ID", fixture.state.AgentID)
	chdirCursorFixture(t, projectRoot)

	cmd, output := newCoworkerCmd(nil)
	require.NoError(t, runCoworkerLoad(cmd, []string{"reviewer"}))
	assert.Contains(t, output.String(), "CURSOR_COWORKER_CONTENT", "coworker content delivery must remain available")
	fixture.assertUnchanged(t)
}

func TestGenericCoworkerLoadStillWritesSessionEntry(t *testing.T) {
	projectRoot := setupSessionTestProject(t)
	state, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
		AgentID: "OxGenericCoworker", AdapterName: "generic",
	})
	require.NoError(t, err)

	logCoworkerLoad(projectRoot, state.AgentID, "reviewer", "sonnet")
	raw, err := os.ReadFile(filepath.Join(state.SessionPath, "raw.jsonl"))
	require.NoError(t, err)
	assert.True(t, bytes.Contains(raw, []byte(`"coworker_name":"reviewer"`)))
}
