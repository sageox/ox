package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestRecordingState_IsSuspended: only a recording with SuspendedAt set is
// paused. Failure prevented: a nil state or a resumed recording reads as paused.
func TestRecordingState_IsSuspended(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name  string
		state *RecordingState
		want  bool
	}{
		{"nil receiver", nil, false},
		{"not suspended", &RecordingState{}, false},
		{"suspended", &RecordingState{SuspendedAt: &now}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.state.IsSuspended())
		})
	}
}
