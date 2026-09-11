package checks

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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
	// (/usr/bin:/bin:/usr/sbin:/sbin), proving the scrub doesn't zero PATH
	// outright.
	resolved, err := probeShellPath(context.Background(), "/bin/sh", "sh")
	require.NoError(t, err)
	assert.NotEmpty(t, resolved)
}

func TestProbeShellPath_BinaryNotFound_ReturnsErrNotFoundInShell(t *testing.T) {
	_, err := probeShellPath(context.Background(), "/bin/sh", "definitely-not-a-real-binary-xyz")
	assert.ErrorIs(t, err, ErrNotFoundInShell)
}

// TestProbeShellPath_StartupOutputDoesNotMaskTheAnswer covers a shell whose
// startup file prints to stdout before the probe script runs -- an `echo` in
// ~/.zshenv is the ordinary case. The answer is the final line; treating the
// whole output as the answer would read "noise\n<sentinel>" as a resolved
// path and report a shadowed binary that does not exist, and would corrupt a
// real resolved path with the noise prefix.
func TestProbeShellPath_StartupOutputDoesNotMaskTheAnswer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shells only")
	}
	dir := t.TempDir()
	chattyShell := filepath.Join(dir, "chatty-shell.sh")
	require.NoError(t, os.WriteFile(chattyShell,
		[]byte("#!/bin/sh\nprintf 'welcome to your shell\\n'\nexec /bin/sh \"$@\"\n"), 0o755))

	_, err := probeShellPath(context.Background(), chattyShell, "definitely-not-a-real-binary-xyz")
	assert.ErrorIs(t, err, ErrNotFoundInShell, "startup noise must not mask an absent binary")

	resolved, err := probeShellPath(context.Background(), chattyShell, "sh")
	require.NoError(t, err)
	assert.NotContains(t, resolved, "welcome", "startup noise must not contaminate the resolved path")
}

// TestProbeShellPath_UnterminatedStartupOutputDoesNotMaskTheAnswer covers the
// harder half of the same problem: a startup file that writes WITHOUT a
// trailing newline. Without a delimiter of our own, the shell's text and the
// answer share one line ("welcome/bin/sh"), so taking the last line is not
// enough -- the probe script has to open with a newline to guarantee its
// answer starts clean.
func TestProbeShellPath_UnterminatedStartupOutputDoesNotMaskTheAnswer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shells only")
	}
	dir := t.TempDir()
	chattyShell := filepath.Join(dir, "unterminated-shell.sh")
	require.NoError(t, os.WriteFile(chattyShell,
		[]byte("#!/bin/sh\nprintf 'welcome'\nexec /bin/sh \"$@\"\n"), 0o755))

	_, err := probeShellPath(context.Background(), chattyShell, "definitely-not-a-real-binary-xyz")
	assert.ErrorIs(t, err, ErrNotFoundInShell, "unterminated startup noise must not mask an absent binary")

	resolved, err := probeShellPath(context.Background(), chattyShell, "sh")
	require.NoError(t, err)
	assert.NotContains(t, resolved, "welcome", "unterminated startup noise must not contaminate the resolved path")
}

// TestProbeShellPath_SucceedsButSwallowsStdout_IsInconclusive pins that a
// shell which exits 0 while producing no output is reported as unknown, not
// as "ox is missing". The probe script always prints either a path or the
// sentinel, so empty output means the answer never reached us -- a startup
// file doing `exec >/dev/null` is the realistic cause. Reporting that as an
// off-PATH failure would be the false negative ErrShellProbeInconclusive
// exists to prevent.
func TestProbeShellPath_SucceedsButSwallowsStdout_IsInconclusive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shells only")
	}
	dir := t.TempDir()
	silentShell := filepath.Join(dir, "silent-shell.sh")
	require.NoError(t, os.WriteFile(silentShell, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	_, err := probeShellPath(context.Background(), silentShell, "ox")
	assert.ErrorIs(t, err, ErrShellProbeInconclusive)
}

// TestProbeShellPath_NotFoundIsShellIndependent pins the not-found answer
// across every POSIX shell present, not just whichever one is /bin/sh
// here. It is the regression guard for reading the answer out of the exit
// code: bash and zsh exit 1 on a failed `command -v`, dash exits 127. On
// Debian and Ubuntu /bin/sh IS dash, so the code-reading version reported
// ErrShellProbeInconclusive on most Linux machines -- silently disabling
// the check in exactly the case it exists to catch. macOS never caught it
// because its /bin/sh is not dash.
func TestProbeShellPath_NotFoundIsShellIndependent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shells only")
	}
	for _, name := range []string{"sh", "bash", "zsh", "dash"} {
		shell, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		t.Run(name, func(t *testing.T) {
			_, err := probeShellPath(context.Background(), shell, "definitely-not-a-real-binary-xyz")
			assert.ErrorIs(t, err, ErrNotFoundInShell)

			resolved, err := probeShellPath(context.Background(), shell, "sh")
			require.NoError(t, err)
			assert.NotEmpty(t, resolved)
		})
	}
}

// TestProbeShellPath_ShellExitsNonOneForUnrelatedReason_IsInconclusive is
// the red-first proof for the fix distinguishing "the shell ran fine and
// reported not-found" (POSIX `command -v`'s exit code 1, the only real
// not-found signal) from "the shell itself failed for an unrelated
// reason" -- e.g. a broken startup file, or an explicit `exit N` in it.
// Before this fix, ANY non-zero exit from the probe shell was read as
// ErrNotFoundInShell, so a supported shell dying in its own rc file would
// have been misreported as "ox is not on PATH" instead of "could not
// probe this shell".
func TestProbeShellPath_ShellExitsNonOneForUnrelatedReason_IsInconclusive(t *testing.T) {
	dir := t.TempDir()
	fakeShell := filepath.Join(dir, "broken-shell.sh")
	// Ignores its "-c <cmd>" args entirely and exits with a code that is
	// not 1 -- simulating a shell whose startup died for a reason that has
	// nothing to do with whether ox is on PATH.
	require.NoError(t, os.WriteFile(fakeShell, []byte("#!/bin/sh\nexit 7\n"), 0o755))

	_, err := probeShellPath(context.Background(), fakeShell, "ox")
	assert.ErrorIs(t, err, ErrShellProbeInconclusive)
	assert.NotErrorIs(t, err, ErrNotFoundInShell, "a non-1 exit code must never be misread as the not-found signal")
}

// TestScrubbedShellEnv_IncludesPlatformDefaultSbinDirs is the red-first
// proof for including /usr/sbin and /sbin in the scrubbed probe PATH. The
// two failure modes this trades off: inheriting the caller's real PATH
// reproduces the false-green this check exists to catch (a PATH entry
// added only to ~/.zshrc, which a real hook shell never sources); scrubbing
// PATH down to too little makes a healthy machine look off-PATH when a
// real hook shell would have resolved ox via its inherited system PATH.
// The platform-default set (/usr/bin:/bin:/usr/sbin:/sbin) is what a real
// non-interactive, non-login shell is actually seeded with.
func TestScrubbedShellEnv_IncludesPlatformDefaultSbinDirs(t *testing.T) {
	var path string
	for _, kv := range scrubbedShellEnv() {
		if p, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = p
		}
	}
	require.NotEmpty(t, path, "scrubbedShellEnv must set a PATH entry")

	dirs := strings.Split(path, ":")
	for _, want := range []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"} {
		assert.Contains(t, dirs, want, "scrubbed PATH must include the platform-default dir %q", want)
	}
}

// TestProbeShellPath_ResolvesABinaryOnlyInSbin proves the platform-default
// PATH fix end to end: a binary that lives ONLY in /usr/sbin or /sbin (never
// /usr/bin or /bin) must still resolve through the scrubbed probe
// environment, the same way it would for a real AI coding tool hook shell.
func TestProbeShellPath_ResolvesABinaryOnlyInSbin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sbin/PATH-scrub semantics are POSIX-shell specific")
	}

	// visudo and chroot are the most reliably present /usr/sbin binaries
	// across macOS and mainstream Linux distros (both ship with the base
	// sudo/coreutils-equivalent packages). Skip -- rather than fail -- if
	// none exist on this machine/CI image: an absent binary proves nothing
	// about the PATH-scrub logic either way.
	candidates := []string{"/usr/sbin/visudo", "/usr/sbin/chroot", "/sbin/ping"}
	var binary string
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			binary = c
			break
		}
	}
	if binary == "" {
		t.Skip("no known /usr/sbin or /sbin binary found on this machine to probe")
	}

	resolved, err := probeShellPath(context.Background(), "/bin/sh", filepath.Base(binary))
	require.NoError(t, err, "a binary living only in /usr/sbin or /sbin must resolve under the scrubbed PATH -- "+
		"if this fails, PATH was scrubbed down to /usr/bin:/bin only (the prior bug) instead of including the platform-default sbin dirs")
	assert.NotEmpty(t, resolved)
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
