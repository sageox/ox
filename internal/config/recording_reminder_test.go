package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeRecordingReminder(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"on", RecordingReminderOn},
		{"off", RecordingReminderOff},
		{"", RecordingReminderOn},
		{"garbage", RecordingReminderOn},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, NormalizeRecordingReminder(tt.input))
		})
	}
}

func TestResolveRecordingReminder_Default(t *testing.T) {
	// no config files → default "on"
	tmpDir := t.TempDir()
	result := ResolveRecordingReminder(tmpDir)
	assert.Equal(t, RecordingReminderOn, result)
}

func TestResolveRecordingReminder_ProjectConfig(t *testing.T) {
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0o755))

	// write project config with recording_reminder=off
	configData := `{"recording_reminder": "off"}`
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(configData), 0o644))

	result := ResolveRecordingReminder(tmpDir)
	assert.Equal(t, RecordingReminderOff, result)
}

func TestRecordingReminderEnabled(t *testing.T) {
	tmpDir := t.TempDir()
	// default is on
	assert.True(t, RecordingReminderEnabled(tmpDir))
}
