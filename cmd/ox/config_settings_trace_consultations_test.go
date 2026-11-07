package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session/contexttrace"
)

// TestTraceConsultationsSetting_Lifecycle drives `ox config set|get|unset
// recap.trace_consultations` through the real settings plumbing. Failure
// prevented: a coworker opts out (ADR-027 rule 3) but the value never reaches
// the resolver the trace writers consult, so their reads keep syncing to the
// team — or an unset leaves them stuck off.
func TestTraceConsultationsSetting_Lifecycle(t *testing.T) {
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	project := t.TempDir()
	const key = "recap.trace_consultations"

	steps := []struct {
		name       string
		act        func() error
		wantValue  string
		wantSource ConfigLevel
		wantOn     bool
	}{
		{"default is on", func() error { return nil }, "on", ConfigLevelDefault, true},
		{"set off", func() error { return SetConfigValue(key, "off", ConfigLevelUser, "") }, "off", ConfigLevelUser, false},
		{"set on", func() error { return SetConfigValue(key, "on", ConfigLevelUser, "") }, "on", ConfigLevelUser, true},
		{"set off again", func() error { return SetConfigValue(key, "off", ConfigLevelUser, "") }, "off", ConfigLevelUser, false},
		{"unset falls back to default", func() error { return UnsetConfigValue(key, ConfigLevelUser, "") }, "on", ConfigLevelDefault, true},
	}
	for _, step := range steps {
		require.NoError(t, step.act(), step.name)
		cv, err := ResolveConfigValue(key, project)
		require.NoError(t, err, step.name)
		assert.Equal(t, step.wantValue, cv.Value, step.name)
		assert.Equal(t, step.wantSource, cv.Source, step.name)
		assert.Equal(t, "on", cv.Default, step.name)
		assert.Equal(t, step.wantOn, config.TraceConsultationsEnabled(), step.name)
	}

	userCfg, err := config.LoadUserConfig()
	require.NoError(t, err)
	assert.Nil(t, userCfg.Recap, "unset leaves no empty recap block behind")
}

// TestTraceConsultationsSetting_RejectsInvalidValues keeps the key to on/off at
// the user level. Failure prevented: "false" or a repo-level write silently
// stored and read as on, so the opt-out does nothing.
func TestTraceConsultationsSetting_RejectsInvalidValues(t *testing.T) {
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	for _, tt := range []struct {
		name  string
		value string
		level ConfigLevel
	}{
		{"typo", "of", ConfigLevelUser},
		{"boolean spelling", "false", ConfigLevelUser},
		{"repo level", "off", ConfigLevelRepo},
		{"team level", "off", ConfigLevelTeam},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Error(t, SetConfigValue("recap.trace_consultations", tt.value, tt.level, t.TempDir()))
		})
	}
	assert.True(t, config.TraceConsultationsEnabled(), "a rejected write must not flip the setting")
}

// TestTagSessionConsults_RespectsOptOut runs the finalize-time consult tagger
// with the opt-out off and on. Failure prevented: ADR-027 promises that off
// means "the reader's consulted events are not written", but finalize keeps
// tagging every `ox query` and team-doc read into the synced trace.
func TestTagSessionConsults_RespectsOptOut(t *testing.T) {
	projectRoot := setupSessionTestProject(t)
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	ctx, err := config.LoadProjectContext(projectRoot)
	require.NoError(t, err)
	require.NotEmpty(t, ctx.DefaultLedgerPath(), "the tagger needs a ledger root to run at all")

	run := func() []contexttrace.Event {
		dir := t.TempDir()
		raw := filepath.Join(dir, "raw.jsonl")
		require.NoError(t, os.WriteFile(raw, []byte(`{"tool_name":"Bash","tool_input":"{\"command\":\"ox query \\\"auth\\\"\"}"}`+"\n"), 0o644))
		tagSessionConsults(projectRoot, dir, raw)
		events, err := contexttrace.ReadEvents(filepath.Join(dir, contexttrace.FileName))
		require.NoError(t, err)
		return events
	}

	require.Len(t, run(), 1, "control: with the setting on, the retrieval is tagged")

	require.NoError(t, SetConfigValue("recap.trace_consultations", "off", ConfigLevelUser, ""))
	assert.Empty(t, run(), "off: no consulted events are written at finalize")
}
