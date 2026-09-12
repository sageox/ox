package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSelectLogoutEndpoints_UnreadableSelectionFailsLoudly is the gate for the
// review finding: with several endpoints logged in and nothing on stdin, logout
// printed "Logout canceled." and exited 0 — having revoked nothing and removed
// no local credentials. --yes cannot rescue it either, because which endpoint to
// log out of is a selection, not a yes/no.
func TestSelectLogoutEndpoints_UnreadableSelectionFailsLoudly(t *testing.T) {
	endpoints := []string{"https://sageox.ai", "https://test.sageox.ai"}

	var got []string
	var err error
	silenceStdout(t, func() {
		withStdin(t, "", func() {
			got, err = selectLogoutEndpoints(endpoints, false, "", false)
		})
	})

	require.Error(t, err, "an unreadable selection must not look like a successful cancel")
	assert.Nil(t, got)
	assert.Contains(t, err.Error(), "--endpoint")
	assert.Contains(t, err.Error(), "--all", "the error must name the deterministic alternatives")
}

func TestSelectLogoutEndpoints_DeterministicPaths(t *testing.T) {
	two := []string{"https://sageox.ai", "https://test.sageox.ai"}
	one := []string{"https://sageox.ai"}

	tests := []struct {
		name      string
		endpoints []string
		all       bool
		specified string
		force     bool
		want      []string
		wantErr   string
	}{
		{name: "--all selects every endpoint", endpoints: two, all: true, want: two},
		{name: "--force selects every endpoint", endpoints: two, force: true, want: two},
		{name: "a single endpoint needs no prompt", endpoints: one, want: one},
		{
			name:      "--endpoint selects the named one",
			endpoints: two,
			specified: "https://test.sageox.ai",
			want:      []string{"https://test.sageox.ai"},
		},
		{
			// slug normalization means the bare host resolves too
			name:      "--endpoint matches on normalized slug",
			endpoints: two,
			specified: "test.sageox.ai",
			want:      []string{"https://test.sageox.ai"},
		},
		{
			name:      "--endpoint that is not logged in errors",
			endpoints: two,
			specified: "https://other.example",
			wantErr:   "not logged in to endpoint",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			var err error
			silenceStdout(t, func() {
				withStdin(t, "", func() {
					got, err = selectLogoutEndpoints(tt.endpoints, tt.all, tt.specified, tt.force)
				})
			})

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestSelectLogoutEndpoints_PipedSelection proves an honest scripted answer
// still works — requiring a terminal would break every such caller.
func TestSelectLogoutEndpoints_PipedSelection(t *testing.T) {
	two := []string{"https://sageox.ai", "https://test.sageox.ai"}

	t.Run("a number picks that endpoint", func(t *testing.T) {
		var got []string
		var err error
		silenceStdout(t, func() {
			withStdin(t, "2\n", func() {
				got, err = selectLogoutEndpoints(two, false, "", false)
			})
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"https://test.sageox.ai"}, got)
	})

	t.Run("the last option picks all endpoints", func(t *testing.T) {
		var got []string
		var err error
		silenceStdout(t, func() {
			withStdin(t, "3\n", func() {
				got, err = selectLogoutEndpoints(two, false, "", false)
			})
		})
		require.NoError(t, err)
		assert.Equal(t, two, got)
	})

	t.Run("an invalid entry re-prompts rather than guessing", func(t *testing.T) {
		var got []string
		var err error
		silenceStdout(t, func() {
			withStdin(t, "9\n1\n", func() {
				got, err = selectLogoutEndpoints(two, false, "", false)
			})
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"https://sageox.ai"}, got)
	})
}
