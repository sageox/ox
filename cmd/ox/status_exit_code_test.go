package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestStatusExitError pins the exit-code contract for `ox status` (non-JSON):
// https://github.com/sageox/ox/issues/866 reported that `ox status` exits 0
// in every state -- healthy, uninitialized, unauthenticated, not a git repo
// -- so `ox status && <next step>` was meaningless in a script. This is the
// red-first proof: before statusExitError existed, RunE always returned nil
// here regardless of authenticated/projectInitialized, so every case below
// would report a nil error.
func TestStatusExitError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		authenticated      bool
		projectInitialized bool
		wantErr            bool
		wantSubstr         string
	}{
		{
			name:               "healthy: authenticated and initialized exits clean",
			authenticated:      true,
			projectInitialized: true,
			wantErr:            false,
		},
		{
			name:               "unauthenticated but initialized still fails",
			authenticated:      false,
			projectInitialized: true,
			wantErr:            true,
			wantSubstr:         "not authenticated",
		},
		{
			name:               "authenticated but uninitialized still fails",
			authenticated:      true,
			projectInitialized: false,
			wantErr:            true,
			wantSubstr:         "not initialized",
		},
		{
			name:               "neither authenticated nor initialized (e.g. empty dir) fails",
			authenticated:      false,
			projectInitialized: false,
			wantErr:            true,
			wantSubstr:         "not authenticated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := statusExitError(tt.authenticated, tt.projectInitialized)
			if !tt.wantErr {
				assert.NoError(t, err, "the only 'everything is fine' state must exit clean")
				return
			}
			if assert.Error(t, err) {
				assert.Contains(t, err.Error(), tt.wantSubstr)
			}
		})
	}
}
