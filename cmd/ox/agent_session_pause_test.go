package main

import (
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
)

// TestComputeExcludedSinceLastPause exercises the helper that the resume
// command uses to report how many entries were excluded between the most
// recent open pause and the resume seq.
func TestComputeExcludedSinceLastPause(t *testing.T) {
	now := time.Now()

	cases := []struct {
		name      string
		lifecycle []session.LifecycleEvent
		resumeSeq int
		want      int
	}{
		{
			name:      "empty lifecycle",
			lifecycle: nil,
			resumeSeq: 10,
			want:      0,
		},
		{
			name: "pause at 5, resume at 10 → 5 excluded",
			lifecycle: []session.LifecycleEvent{
				{Action: session.LifecycleActionStart, Seq: 0, At: now},
				{Action: session.LifecycleActionPause, Seq: 5, At: now},
			},
			resumeSeq: 10,
			want:      5,
		},
		{
			name: "last action is resume — no open pause",
			lifecycle: []session.LifecycleEvent{
				{Action: session.LifecycleActionPause, Seq: 5, At: now},
				{Action: session.LifecycleActionResume, Seq: 10, At: now},
			},
			resumeSeq: 15,
			want:      0,
		},
		{
			name: "multi-cycle with last open pause",
			lifecycle: []session.LifecycleEvent{
				{Action: session.LifecycleActionPause, Seq: 5, At: now},
				{Action: session.LifecycleActionResume, Seq: 7, At: now},
				{Action: session.LifecycleActionPause, Seq: 12, At: now},
			},
			resumeSeq: 15,
			want:      3,
		},
		{
			name: "stop closes pause — no open pause",
			lifecycle: []session.LifecycleEvent{
				{Action: session.LifecycleActionPause, Seq: 5, At: now},
				{Action: session.LifecycleActionStop, Seq: 10, At: now},
			},
			resumeSeq: 15,
			want:      0,
		},
		{
			name: "resume seq before pause seq → 0 (defensive)",
			lifecycle: []session.LifecycleEvent{
				{Action: session.LifecycleActionPause, Seq: 20, At: now},
			},
			resumeSeq: 10,
			want:      0,
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got := computeExcludedSinceLastPause(c.lifecycle, c.resumeSeq)
			assert.Equal(t, c.want, got)
		})
	}
}

// TestFormatPausedDuration exercises the duration formatter used in resume output.
func TestFormatPausedDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{d: 30 * time.Second, want: "30s"},
		{d: 4*time.Minute + 30*time.Second, want: "4m 30s"},
		{d: 2 * time.Hour, want: "2h"},
		{d: 1*time.Hour + 15*time.Minute, want: "1h 15m"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.want, func(t *testing.T) {
			assert.Equal(t, c.want, formatPausedDuration(c.d))
		})
	}
}
