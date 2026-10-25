package main

import (
	"errors"
	"os"
	"testing"

	"github.com/sageox/ox/internal/cli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// silenceStdout points os.Stdout at /dev/null for the duration of fn. The
// uninstall confirmation prints a full DANGER ZONE banner that would otherwise
// swamp test output.
func silenceStdout(t *testing.T, fn func()) {
	t.Helper()

	devNull, err := os.Open(os.DevNull)
	require.NoError(t, err)
	orig := os.Stdout
	os.Stdout = devNull
	t.Cleanup(func() {
		os.Stdout = orig
		devNull.Close()
	})

	fn()
}

// TestConfirmUninstallWithInput_TellsDeclinedApartFromUnanswered is the
// regression gate for the reported data loss: with nothing on stdin, uninstall
// reported "Uninstall canceled" and exited 0, which reads as success.
func TestConfirmUninstallWithInput_TellsDeclinedApartFromUnanswered(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    bool
		wantErr bool
	}{
		{
			name:    "closed stdin is not an answer",
			input:   "",
			wantErr: true,
		},
		{
			name:    "whitespace-only stdin is not an answer",
			input:   "   ",
			wantErr: true,
		},
		{
			name:  "typing the repo name confirms",
			input: "my-repo\n",
			want:  true,
		},
		{
			name:  "typing uninstall confirms",
			input: "uninstall\n",
			want:  true,
		},
		{
			name:  "typing uninstall is case insensitive",
			input: "UNINSTALL\n",
			want:  true,
		},
		{
			name:  "the wrong word is a real decline, not a missing answer",
			input: "nope\n",
			want:  false,
		},
		{
			// typed answer with no trailing newline still counts
			name:  "repo name without a trailing newline confirms",
			input: "my-repo",
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gitRoot := t.TempDir()

			var got bool
			var err error
			silenceStdout(t, func() {
				withStdin(t, tt.input, func() {
					got, err = confirmUninstallWithInput("my-repo", "https://sageox.ai", false, gitRoot)
				})
			})

			if tt.wantErr {
				assert.True(t, errors.Is(err, cli.ErrConfirmationRequired),
					"want ErrConfirmationRequired, got %v", err)
				assert.False(t, got, "an unanswered prompt must never confirm")
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestConfirmUninstallWithInput_YesDoesNotSatisfyTheTypedGate pins the
// deliberate carve-out: --yes is not a substitute for typing the repository
// name, because that gate exists precisely to resist automation. --force is the
// explicit escape hatch, and it is checked by the caller.
func TestConfirmUninstallWithInput_YesDoesNotSatisfyTheTypedGate(t *testing.T) {
	cli.SetAssumeYes(true)
	t.Cleanup(func() { cli.SetAssumeYes(false) })

	gitRoot := t.TempDir()

	var got bool
	var err error
	silenceStdout(t, func() {
		withStdin(t, "", func() {
			got, err = confirmUninstallWithInput("my-repo", "https://sageox.ai", false, gitRoot)
		})
	})

	assert.True(t, errors.Is(err, cli.ErrConfirmationRequired),
		"--yes must not confirm an uninstall; got %v", err)
	assert.False(t, got)
}

// TestConfirmUninstallWithInput_AllEndpointsBanner covers the all-endpoints
// branch of the banner, which names a different blast radius than the
// single-endpoint one.
func TestConfirmUninstallWithInput_AllEndpointsBanner(t *testing.T) {
	gitRoot := t.TempDir()

	var got bool
	var err error
	silenceStdout(t, func() {
		withStdin(t, "uninstall\n", func() {
			got, err = confirmUninstallWithInput("my-repo", "", true, gitRoot)
		})
	})

	require.NoError(t, err)
	assert.True(t, got)
}
