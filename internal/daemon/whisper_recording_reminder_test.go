package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/sageox/ox/internal/session"
)

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		expected string
	}{
		{"zero", 0, "<1m"},
		{"30 seconds rounds up", 30 * time.Second, "1m"},
		{"sub-30 seconds", 20 * time.Second, "<1m"},
		{"one minute", 1 * time.Minute, "1m"},
		{"minutes", 23 * time.Minute, "23m"},
		{"one hour", 60 * time.Minute, "1h"},
		{"hours and minutes", 1*time.Hour + 23*time.Minute, "1h 23m"},
		{"multiple hours", 3*time.Hour + 45*time.Minute, "3h 45m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, formatDuration(tt.duration))
		})
	}
}

func TestFormatReminderContent(t *testing.T) {
	state := &session.RecordingState{
		StartedAt:  time.Now().Add(-1*time.Hour - 23*time.Minute),
		EntryCount: 45,
	}
	content := formatReminderContent(state)
	assert.Contains(t, content, "Tell the user")
	assert.Contains(t, content, "SageOx recording active")
	assert.Contains(t, content, "45 turns")
	assert.Contains(t, content, "1h 23m")
	assert.Contains(t, content, "shared with your team")
}

func TestRecordingReminderSource_Name(t *testing.T) {
	s := &RecordingReminderSource{}
	assert.Equal(t, "recording-reminder", s.Name())
}

func TestRecordingReminderSource_Interval(t *testing.T) {
	s := NewRecordingReminderSource(nil, nil, time.Hour, "")
	assert.Equal(t, 1*time.Minute, s.Interval(), "default tick is 1 minute")

	s.SetTick(5 * time.Second)
	assert.Equal(t, 5*time.Second, s.Interval(), "tick is configurable")
}

func TestRecordingReminderSource_Produce_NilDeps(t *testing.T) {
	s := &RecordingReminderSource{}
	assert.Nil(t, s.Produce(context.Background()))
}
