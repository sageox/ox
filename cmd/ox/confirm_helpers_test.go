package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/cli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureConfirmOutput swaps os.Stdout for a pipe and returns everything written
// during fn. The confirm helpers below report to the user via fmt.Print*, so
// their user-visible output is the behavior under test.
func captureConfirmOutput(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stdout
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
		done <- buf.String()
	}()

	fn()

	os.Stdout = orig
	require.NoError(t, w.Close())
	out := <-done
	require.NoError(t, r.Close())
	return out
}

// --- ox init: rebinding a repo to a different endpoint -----------------------

func TestConfirmEndpointRebind(t *testing.T) {
	t.Run("no stored endpoint proceeds without prompting", func(t *testing.T) {
		var ok bool
		var err error
		withStdin(t, "", func() { ok, err = confirmEndpointRebind("", "https://sageox.ai") })
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("matching endpoint proceeds without prompting", func(t *testing.T) {
		var ok bool
		var err error
		withStdin(t, "", func() {
			ok, err = confirmEndpointRebind("https://sageox.ai", "https://sageox.ai")
		})
		require.NoError(t, err)
		assert.True(t, ok)
	})

	// The gate: this prompt defaults to YES and registration has no inverse in
	// the CLI, so an unanswered prompt must not rebind the repo.
	t.Run("unanswered prompt aborts instead of rebinding", func(t *testing.T) {
		var ok bool
		var err error
		out := captureConfirmOutput(t, func() {
			withStdin(t, "", func() {
				ok, err = confirmEndpointRebind("https://old.example", "https://new.example")
			})
		})

		assert.False(t, ok, "a default-yes prompt must not rebind unattended")
		require.Error(t, err)
		assert.True(t, errors.Is(err, cli.ErrConfirmationRequired), "got %v", err)
		assert.Contains(t, out, "SAGEOX_ENDPOINT=https://old.example",
			"the abort must tell the user how to keep the stored endpoint")
	})

	t.Run("explicit no aborts quietly", func(t *testing.T) {
		var ok bool
		var err error
		out := captureConfirmOutput(t, func() {
			withStdin(t, "n\n", func() {
				ok, err = confirmEndpointRebind("https://old.example", "https://new.example")
			})
		})

		require.NoError(t, err, "a deliberate decline is not an error")
		assert.False(t, ok)
		assert.Contains(t, out, "SAGEOX_ENDPOINT=https://old.example")
	})

	t.Run("explicit yes rebinds", func(t *testing.T) {
		var ok bool
		var err error
		captureConfirmOutput(t, func() {
			withStdin(t, "y\n", func() {
				ok, err = confirmEndpointRebind("https://old.example", "https://new.example")
			})
		})
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("global yes rebinds", func(t *testing.T) {
		cli.SetAssumeYes(true)
		t.Cleanup(func() { cli.SetAssumeYes(false) })

		var ok bool
		var err error
		captureConfirmOutput(t, func() {
			withStdin(t, "", func() {
				ok, err = confirmEndpointRebind("https://old.example", "https://new.example")
			})
		})
		require.NoError(t, err)
		assert.True(t, ok)
	})
}

// --- ox login ---------------------------------------------------------------

func TestConfirmReauthenticate(t *testing.T) {
	t.Run("unanswered prompt errors instead of looking like a broken login", func(t *testing.T) {
		var out bytes.Buffer
		var ok bool
		var err error
		captureConfirmOutput(t, func() {
			withStdin(t, "", func() { ok, err = confirmReauthenticate(&out) })
		})

		assert.False(t, ok)
		assert.True(t, errors.Is(err, cli.ErrConfirmationRequired), "got %v", err)
		assert.NotContains(t, out.String(), "Authentication canceled.",
			"an unanswered prompt must not report a cancel the user never made")
	})

	t.Run("explicit no cancels quietly", func(t *testing.T) {
		var out bytes.Buffer
		var ok bool
		var err error
		captureConfirmOutput(t, func() {
			withStdin(t, "n\n", func() { ok, err = confirmReauthenticate(&out) })
		})

		require.NoError(t, err)
		assert.False(t, ok)
		assert.Contains(t, out.String(), "Authentication canceled.")
	})

	t.Run("explicit yes re-authenticates", func(t *testing.T) {
		var out bytes.Buffer
		var ok bool
		var err error
		captureConfirmOutput(t, func() {
			withStdin(t, "y\n", func() { ok, err = confirmReauthenticate(&out) })
		})

		require.NoError(t, err)
		assert.True(t, ok)
		assert.Empty(t, out.String())
	})
}

func TestConfirmEndpointFallback(t *testing.T) {
	const alt = "https://alt.example"

	// The gate: this prompt defaults to YES, so an unanswered prompt would
	// have authenticated against a different endpoint than the one asked for.
	t.Run("unanswered prompt stays put and says why", func(t *testing.T) {
		var out bytes.Buffer
		var got string
		captureConfirmOutput(t, func() {
			withStdin(t, "", func() { got = confirmEndpointFallback(&out, alt) })
		})

		assert.Empty(t, got, "a default-yes prompt must not switch endpoints unattended")
		assert.Contains(t, out.String(), "Not switching endpoints")
		assert.Contains(t, strings.ToLower(out.String()), "--endpoint")
	})

	t.Run("explicit no stays put", func(t *testing.T) {
		var out bytes.Buffer
		var got string
		captureConfirmOutput(t, func() {
			withStdin(t, "n\n", func() { got = confirmEndpointFallback(&out, alt) })
		})

		assert.Empty(t, got)
		assert.NotContains(t, out.String(), "Not switching endpoints",
			"a deliberate no is not the same as an unanswered prompt")
	})

	t.Run("explicit yes switches", func(t *testing.T) {
		var out bytes.Buffer
		var got string
		captureConfirmOutput(t, func() {
			withStdin(t, "y\n", func() { got = confirmEndpointFallback(&out, alt) })
		})
		assert.Equal(t, alt, got)
	})
}

// --- ox init: continuing when the team list cannot be fetched ---------------

func TestConfirmContinueWithoutTeam(t *testing.T) {
	t.Run("unanswered prompt names the flag to pass instead", func(t *testing.T) {
		var err error
		captureConfirmOutput(t, func() {
			withStdin(t, "", func() { err = confirmContinueWithoutTeam("https://sageox.ai") })
		})

		require.Error(t, err)
		assert.True(t, errors.Is(err, cli.ErrConfirmationRequired), "got %v", err)
		assert.Contains(t, err.Error(), "--team",
			"an unattended caller must be told how to proceed without a prompt")
	})

	t.Run("explicit no cancels", func(t *testing.T) {
		var err error
		captureConfirmOutput(t, func() {
			withStdin(t, "n\n", func() { err = confirmContinueWithoutTeam("https://sageox.ai") })
		})

		require.Error(t, err)
		assert.False(t, errors.Is(err, cli.ErrConfirmationRequired),
			"a deliberate decline is not a missing answer")
		assert.Contains(t, err.Error(), "canceled")
	})

	t.Run("explicit yes continues", func(t *testing.T) {
		var err error
		captureConfirmOutput(t, func() {
			withStdin(t, "y\n", func() { err = confirmContinueWithoutTeam("https://sageox.ai") })
		})
		assert.NoError(t, err)
	})
}

// --- ox uninstall: the type-to-confirm gate ---------------------------------

func TestConfirmUninstallGate(t *testing.T) {
	t.Run("force skips the gate entirely", func(t *testing.T) {
		var ok bool
		var err error
		captureConfirmOutput(t, func() {
			withStdin(t, "", func() {
				ok, err = confirmUninstallGate(t.TempDir(), "https://sageox.ai", false, true)
			})
		})
		require.NoError(t, err)
		assert.True(t, ok)
	})

	// The gate: an unattended run used to print "Uninstall canceled" and exit
	// 0, which reads as a completed uninstall.
	t.Run("unanswered gate errors and points at --force", func(t *testing.T) {
		gitRoot := t.TempDir()
		var ok bool
		var err error
		out := captureConfirmOutput(t, func() {
			withStdin(t, "", func() {
				ok, err = confirmUninstallGate(gitRoot, "https://sageox.ai", false, false)
			})
		})

		assert.False(t, ok)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--force")
		assert.NotContains(t, err.Error(), "--yes",
			"--yes does not satisfy this gate, so the message must not suggest it")
		assert.NotContains(t, out, "Uninstall canceled",
			"an unanswered gate must not report a cancel the user never made")
	})

	t.Run("declining reports an ordinary cancel", func(t *testing.T) {
		gitRoot := t.TempDir()
		var ok bool
		var err error
		out := captureConfirmOutput(t, func() {
			withStdin(t, "nope\n", func() {
				ok, err = confirmUninstallGate(gitRoot, "https://sageox.ai", false, false)
			})
		})

		require.NoError(t, err)
		assert.False(t, ok)
		assert.Contains(t, out, "Uninstall canceled")
	})

	t.Run("typing the repo name proceeds", func(t *testing.T) {
		gitRoot := t.TempDir()
		var ok bool
		var err error
		captureConfirmOutput(t, func() {
			withStdin(t, filepath.Base(gitRoot)+"\n", func() {
				ok, err = confirmUninstallGate(gitRoot, "https://sageox.ai", false, false)
			})
		})

		require.NoError(t, err)
		assert.True(t, ok)
	})
}

// --- ox uninstall: the cloud-deletion confirmation link ----------------------

// TestOfferCloudDeletionConfirmation_AlwaysPrintsTheURL is the regression gate
// for the reported data loss. The uninstall request is already submitted here;
// if this step is skipped without surfacing the URL, cloud records outlive the
// local uninstall with nothing on screen pointing at them.
func TestOfferCloudDeletionConfirmation_AlwaysPrintsTheURL(t *testing.T) {
	const (
		ep     = "https://sageox.ai"
		repoID = "repo_123"
		url    = "https://sageox.ai/repos/repo_123/uninstall/confirm"
	)

	t.Run("unanswered prompt warns and prints the URL", func(t *testing.T) {
		out := captureConfirmOutput(t, func() {
			withStdin(t, "", func() { offerCloudDeletionConfirmation(ep, repoID, false) })
		})

		assert.Contains(t, out, url, "the confirmation URL must never be silently dropped")
		// "Confirm here:" is the unanswered-prompt framing; the deliberate
		// decline says "Confirm later at:" instead. (The ⚠ warning itself goes
		// to stderr, so it is not asserted on this stream.)
		assert.Contains(t, out, "Confirm here:")
	})

	t.Run("declining still prints the URL for later", func(t *testing.T) {
		out := captureConfirmOutput(t, func() {
			withStdin(t, "n\n", func() { offerCloudDeletionConfirmation(ep, repoID, false) })
		})

		assert.Contains(t, out, url)
		assert.Contains(t, out, "Confirm later at:", "a deliberate decline is not a failure")
		assert.NotContains(t, out, "Confirm here:")
	})

	t.Run("accepting attempts the browser and still surfaces the URL on failure", func(t *testing.T) {
		// SKIP_BROWSER makes cli.OpenInBrowser a no-op success, so this covers
		// the accepted branch without launching anything.
		t.Setenv("SKIP_BROWSER", "1")

		out := captureConfirmOutput(t, func() {
			withStdin(t, "y\n", func() { offerCloudDeletionConfirmation(ep, repoID, false) })
		})

		assert.NotContains(t, out, "Confirm here:")
		assert.NotContains(t, out, "Confirm later at:")
	})

	t.Run("force skips the prompt and takes the browser path", func(t *testing.T) {
		t.Setenv("SKIP_BROWSER", "1")

		out := captureConfirmOutput(t, func() {
			withStdin(t, "", func() { offerCloudDeletionConfirmation(ep, repoID, true) })
		})

		assert.NotContains(t, out, "Confirm here:",
			"--force must confirm rather than strand the cloud records")
	})
}
