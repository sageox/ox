package checks

import (
	"context"
	"testing"

	"github.com/sageox/ox/internal/doctor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOxInPathCheck_DetectsOffPathForHooksEvenWhenProcessPATHHasIt is the
// direct regression test for the bug this check exists to catch: a plain
// exec.LookPath (the process's own, possibly interactive, PATH) can resolve
// ox just fine while the non-interactive shell that AI coding tool hooks
// actually run in cannot, because PATH was only added to ~/.zshrc.
//
// The stubbed lookPath alone demonstrates what the OLD check
// (internal/doctor/checks/ox_in_path.go pre-fix, which called
// exec.LookPath and nothing else) would have reported: a false pass. The
// check under test must instead warn, using the stubbed shell probe as the
// authority.
func TestOxInPathCheck_DetectsOffPathForHooksEvenWhenProcessPATHHasIt(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")

	stubLookPath := func(string) (string, error) { return "/opt/ox/bin/ox", nil }
	stubExecutable := func() (string, error) { return "/opt/ox/bin/ox", nil }
	stubShellProbe := func(_ context.Context, _, _ string) (string, error) {
		return "", ErrNotFoundInShell
	}

	// Sanity check: this is exactly the signal the OLD check trusted, and
	// it says "found" -- which is why it returned a false pass.
	oldSignal, err := stubLookPath("ox")
	require.NoError(t, err)
	require.NotEmpty(t, oldSignal, "process PATH resolves ox -- this is what made the pre-fix check a false green")

	check := NewOxInPathCheckForTest(stubLookPath, stubShellProbe, stubExecutable)
	result := check.Run(context.Background(), false)

	assert.Equal(t, doctor.StatusWarn, result.Status)
	assert.Contains(t, result.Message, "/opt/ox/bin/ox")
	assert.Contains(t, result.Message, "not on PATH for non-interactive shells")
	assert.Contains(t, result.Fix, "~/.zshenv")
	assert.Contains(t, result.Fix, `export PATH="$PATH:/opt/ox/bin"`)
}

func TestOxInPathCheck_OffPath_MessageMatchesContractD3(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")

	check := NewOxInPathCheckForTest(
		func(string) (string, error) { return "", assertNotCalled(t) },
		func(_ context.Context, _, _ string) (string, error) { return "", ErrNotFoundInShell },
		func() (string, error) { return "/home/dev/go/bin/ox", nil },
	)
	result := check.Run(context.Background(), false)

	require.Equal(t, doctor.StatusWarn, result.Status)
	assert.Equal(t, "ox is installed at /home/dev/go/bin/ox but is not on PATH for non-interactive shells.", result.Message)
	assert.Equal(t,
		"AI coding tools run hooks in a non-interactive shell, which reads ~/.zshenv but not ~/.zshrc.\n"+
			"Add this line to ~/.zshenv:\n"+
			`    export PATH="$PATH:/home/dev/go/bin"`,
		result.Fix,
	)
}

func TestOxInPathCheck_NotInstalled(t *testing.T) {
	check := NewOxInPathCheckForTest(
		func(string) (string, error) { return "", assertErrNotFound() },
		func(_ context.Context, _, _ string) (string, error) { return "", ErrNotFoundInShell },
		func() (string, error) { return "", assertErrNotFound() },
	)
	result := check.Run(context.Background(), false)

	assert.Equal(t, doctor.StatusWarn, result.Status)
	assert.Equal(t, "ox is not installed", result.Message)
	assert.Contains(t, result.Fix, "brew tap sageox/tap")
}

func TestOxInPathCheck_Pass_WhenShellAgreesWithRunningBinary(t *testing.T) {
	check := NewOxInPathCheckForTest(
		func(string) (string, error) { return "", assertNotCalled(t) },
		func(_ context.Context, _, _ string) (string, error) { return "/usr/local/bin/ox", nil },
		func() (string, error) { return "/usr/local/bin/ox", nil },
	)
	result := check.Run(context.Background(), false)

	assert.Equal(t, doctor.StatusPass, result.Status)
}

func TestOxInPathCheck_Shadowed_WhenShellResolvesADifferentBinary(t *testing.T) {
	check := NewOxInPathCheckForTest(
		func(string) (string, error) { return "", assertNotCalled(t) },
		func(_ context.Context, _, _ string) (string, error) { return "/opt/homebrew/bin/ox", nil },
		func() (string, error) { return "/Users/dev/go/bin/ox", nil },
	)
	result := check.Run(context.Background(), false)

	assert.Equal(t, doctor.StatusWarn, result.Status)
	assert.Contains(t, result.Message, "/opt/homebrew/bin/ox")
	assert.Contains(t, result.Message, "/Users/dev/go/bin/ox")
}

// TestOxInPathCheck_InconclusiveProbe_NeverReportsAFalseFailure verifies
// that when the shell probe itself cannot run to completion (missing
// shell, permission error, timeout), the check skips rather than reporting
// a failure or a pass it cannot back up. A user's exotic shell config must
// never make `ox doctor` lie.
func TestOxInPathCheck_InconclusiveProbe_NeverReportsAFalseFailure(t *testing.T) {
	check := NewOxInPathCheckForTest(
		func(string) (string, error) { return "", assertNotCalled(t) },
		func(_ context.Context, _, _ string) (string, error) { return "", ErrShellProbeInconclusive },
		func() (string, error) { return "/usr/local/bin/ox", nil },
	)
	result := check.Run(context.Background(), false)

	assert.Equal(t, doctor.StatusSkip, result.Status)
}

// TestOxInPathCheck_OffPathFixText_PerShell covers contract D15: only zsh
// has a rc file genuinely always read by a non-interactive shell, so every
// other shell must (a) name its own rc file for editing the interactive
// shell, (b) never claim that file is "always read", and (c) carry the
// restart-the-tool line, since editing the file alone does not affect an
// already-running non-interactive invocation.
func TestOxInPathCheck_OffPathFixText_PerShell(t *testing.T) {
	tests := []struct {
		name         string
		kind         shellKind
		wantFile     string
		wantLine     string
		wantsRestart bool
	}{
		{"zsh", shellZsh, "~/.zshenv", `export PATH="$PATH:/some/dir"`, false},
		{"bash", shellBash, "~/.bashrc", `export PATH="$PATH:/some/dir"`, true},
		{"fish", shellFish, "~/.config/fish/config.fish", "fish_add_path /some/dir", true},
		{"unknown", shellUnknown, "your shell's startup file", `export PATH="$PATH:/some/dir"`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := offPathFixText(tt.kind, "/some/dir")
			assert.Contains(t, got, "Add this line to "+tt.wantFile+":")
			assert.Contains(t, got, tt.wantLine)
			if tt.kind != shellZsh {
				assert.NotContains(t, got, "~/.zshrc", "must never say .zshrc for a non-zsh shell")
				assert.NotContains(t, got, "always read", "non-zsh shells must not claim a file is always read (D15)")
			}
			if tt.wantsRestart {
				assert.Contains(t, got, nonZshRestartLine)
			} else {
				assert.NotContains(t, got, nonZshRestartLine, "zsh's ~/.zshenv is always read; no restart needed")
			}
		})
	}
}

func TestProbeShellPath_ShellBinaryMissing_IsInconclusive(t *testing.T) {
	_, err := probeShellPath(context.Background(), "/no/such/shell", "ox")
	assert.ErrorIs(t, err, ErrShellProbeInconclusive)
}

func TestProbeShellPath_EmptyShellPath_IsInconclusive(t *testing.T) {
	_, err := probeShellPath(context.Background(), "", "ox")
	assert.ErrorIs(t, err, ErrShellProbeInconclusive)
}

func TestProbeShellPath_ResolvesARealBinaryViaScrubbedEnv(t *testing.T) {
	// "command -v sh" should resolve even under the scrubbed PATH
	// (/usr/bin:/bin), proving the scrub doesn't zero PATH outright.
	resolved, err := probeShellPath(context.Background(), "/bin/sh", "sh")
	require.NoError(t, err)
	assert.NotEmpty(t, resolved)
}

func TestProbeShellPath_BinaryNotFound_ReturnsErrNotFoundInShell(t *testing.T) {
	_, err := probeShellPath(context.Background(), "/bin/sh", "definitely-not-a-real-binary-xyz")
	assert.ErrorIs(t, err, ErrNotFoundInShell)
}

// assertNotCalled fails the test if the stub is ever invoked -- used for
// dependencies the code under test should short-circuit around.
func assertNotCalled(t *testing.T) error {
	t.Helper()
	t.Fatal("dependency should not have been called")
	return nil
}

// assertErrNotFound is a small helper for stubs representing "not found".
func assertErrNotFound() error {
	return errNotFoundStub
}

var errNotFoundStub = &notFoundErr{}

type notFoundErr struct{}

func (*notFoundErr) Error() string { return "not found" }
