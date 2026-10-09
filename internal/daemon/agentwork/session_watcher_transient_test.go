package agentwork

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsPipeDrainTimeout(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"wait delay", fmt.Errorf("adapter x read failed: %w (stderr: )", errors.New("exec: WaitDelay expired before I/O complete")), true},
		{"other adapter failure", errors.New("adapter error: bad offset"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isPipeDrainTimeout(tt.err); got != tt.want {
				t.Fatalf("isPipeDrainTimeout(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
