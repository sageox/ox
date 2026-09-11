package checks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/sageox/ox/internal/doctor"
)

// LookPathFunc abstracts exec.LookPath for testability.
type LookPathFunc func(file string) (string, error)

// ShellProbeFunc resolves a binary name the way the given shell would when
// invoked non-interactively with a scrubbed environment -- the same
// environment AI coding tool hooks actually run in. It returns:
//   - (path, nil) if the shell resolved the binary
//   - ("", ErrNotFoundInShell) if the shell ran successfully but could not
//     resolve the binary
//   - ("", ErrShellProbeInconclusive) if the probe itself could not run
//     (missing shell, permission error, timeout) -- this must never be
//     reported as an off-PATH failure
type ShellProbeFunc func(ctx context.Context, shellPath, binary string) (string, error)

// ErrNotFoundInShell indicates the probe shell ran successfully but could
// not resolve the requested binary.
var ErrNotFoundInShell = errors.New("not found in shell")

// ErrShellProbeInconclusive indicates the shell probe itself could not be
// run to completion. Callers must treat this as "unknown", never as a
// false negative.
var ErrShellProbeInconclusive = errors.New("shell probe inconclusive")

// shellProbeTimeout bounds how long we wait on the probe shell. A user's
// exotic shell config (slow .zshenv, a hung command) must not make
// `ox doctor` hang.
const shellProbeTimeout = 2 * time.Second

// shellKind identifies the shell family being probed, matching the
// onboarding-fixes contract's shell table (D1).
type shellKind string

const (
	shellZsh     shellKind = "zsh"
	shellBash    shellKind = "bash"
	shellFish    shellKind = "fish"
	shellUnknown shellKind = "unknown"
)

// OxInPathCheck verifies that ox is reachable from the non-interactive,
// non-login shell that AI coding tool hooks actually run in -- not just
// whatever PATH happened to launch `ox doctor` itself. Testing the latter
// is a false green: it passes in an interactive terminal even when hooks
// (which run `zsh -c '...'`, sourcing only ~/.zshenv) cannot find ox at all.
type OxInPathCheck struct {
	lookPath   LookPathFunc
	shellProbe ShellProbeFunc
	executable func() (string, error)
}

// NewOxInPathCheck creates a new OxInPathCheck.
// If lookPath is nil, exec.LookPath is used.
func NewOxInPathCheck(lookPath LookPathFunc) *OxInPathCheck {
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	return &OxInPathCheck{
		lookPath:   lookPath,
		shellProbe: probeShellPath,
		executable: os.Executable,
	}
}

// NewOxInPathCheckForTest creates an OxInPathCheck with every environment
// dependency injectable, so the check is unit-testable without depending
// on the developer's real shell configuration, PATH, or installed ox.
// Passing nil for any parameter falls back to the production default.
func NewOxInPathCheckForTest(lookPath LookPathFunc, shellProbe ShellProbeFunc, executable func() (string, error)) *OxInPathCheck {
	c := NewOxInPathCheck(lookPath)
	if shellProbe != nil {
		c.shellProbe = shellProbe
	}
	if executable != nil {
		c.executable = executable
	}
	return c
}

func (c *OxInPathCheck) Name() string {
	return "ox in PATH"
}

func (c *OxInPathCheck) Category() string {
	return "Ecosystem"
}

func (c *OxInPathCheck) Run(ctx context.Context, _ bool) doctor.CheckResult {
	exePath, exeErr := c.executable()
	if exeErr != nil {
		// os.Executable() essentially never fails while the calling binary
		// is running; fall back to a plain LookPath as a last resort so we
		// still have a candidate path to report against the shell probe.
		if p, err := c.lookPath("ox"); err == nil {
			exePath, exeErr = p, nil
		}
	}

	probeCtx, cancel := context.WithTimeout(ctx, shellProbeTimeout)
	defer cancel()

	shellPath, kind := detectHookShell()
	resolved, probeErr := c.shellProbe(probeCtx, shellPath, "ox")

	switch {
	case errors.Is(probeErr, ErrShellProbeInconclusive):
		// never report a false failure (or false pass) when we couldn't
		// actually probe the hook shell.
		return doctor.CheckResult{
			Name:    c.Name(),
			Status:  doctor.StatusSkip,
			Message: "could not probe the non-interactive shell environment",
		}

	case probeErr == nil:
		if exeErr == nil && !samePath(resolved, exePath) {
			return doctor.CheckResult{
				Name:   c.Name(),
				Status: doctor.StatusWarn,
				Message: fmt.Sprintf(
					"ox on PATH resolves to %s, which differs from the running binary at %s.",
					resolved, exePath,
				),
				Fix: "These should be the same install. Remove the stale entry (old PATH " +
					"directory or leftover binary) so hooks and your interactive shell agree.",
			}
		}
		return doctor.CheckResult{
			Name:    c.Name(),
			Status:  doctor.StatusPass,
			Message: filepath.Dir(resolved),
		}

	case errors.Is(probeErr, ErrNotFoundInShell):
		if exeErr == nil {
			dir := filepath.Dir(exePath)
			return doctor.CheckResult{
				Name:   c.Name(),
				Status: doctor.StatusWarn,
				Message: fmt.Sprintf(
					"ox is installed at %s but is not on PATH for non-interactive shells.",
					exePath,
				),
				Fix: offPathFixText(kind, dir),
			}
		}
		return doctor.CheckResult{
			Name:    c.Name(),
			Status:  doctor.StatusWarn,
			Message: "ox is not installed",
			Fix:     notInstalledFixText,
		}

	default:
		// unreachable: shellProbe implementations must only return nil,
		// ErrNotFoundInShell, or ErrShellProbeInconclusive.
		return doctor.CheckResult{
			Name:    c.Name(),
			Status:  doctor.StatusSkip,
			Message: "could not probe the non-interactive shell environment",
		}
	}
}

// notInstalledFixText points at the official, self-updating install routes
// rather than `go install`/`make install`, which do not track releases.
const notInstalledFixText = "brew tap sageox/tap && brew install ox        # recommended\n" +
	"curl -sSL https://raw.githubusercontent.com/sageox/ox/main/scripts/install.sh | bash"

// nonZshRestartLine is required by contract D15: non-interactive,
// non-login bash sources nothing by default (not ~/.bashrc, not
// ~/.profile -- only $BASH_ENV, which is too obscure and too global to
// send users to). So for every shell except zsh, editing the rc file
// alone does not help a tool that is already running: it must be
// restarted from a fresh terminal to pick up the change.
const nonZshRestartLine = "Then restart your AI coding tool from a new terminal so it picks up the change."

// offPathFixText renders the canonical off-PATH remediation for the
// detected shell: why the shell can't see PATH edits made elsewhere, the
// exact line to add to the file that shell actually reads, and (per D15)
// a restart reminder for every shell but zsh.
func offPathFixText(kind shellKind, dir string) string {
	rc := shellRCFor(kind)
	fix := explanationFor(kind) + "\n" +
		"Add this line to " + rc.file + ":\n" +
		"    " + rc.line(dir)
	if kind != shellZsh {
		fix += "\n" + nonZshRestartLine
	}
	return fix
}

// explanationFor returns why the given shell can't see PATH edits made to
// the wrong file. The zsh sentence is the contract's canonical wording
// (D2, amended by D14 to say "tools" not "agents") -- copied verbatim,
// since zsh is the macOS default and ~/.zshenv is the one file genuinely
// always read by a non-interactive shell. Per D15, non-zsh shells must NOT
// claim any file is "always read" (non-interactive, non-login bash reads
// nothing by default) -- state plainly that the tool only sees the
// environment of the terminal it was started from.
func explanationFor(kind shellKind) string {
	if kind == shellZsh {
		return "AI coding tools run hooks in a non-interactive shell, which reads ~/.zshenv but not ~/.zshrc."
	}
	return "AI coding tools inherit the environment of the terminal they were started from, not any change made after they launched."
}

// shellRCFile names the file a shell's non-interactive hook actually reads,
// plus how to render the PATH-append line for that shell.
type shellRCFile struct {
	file string
	line func(dir string) string
}

// shellRCFor maps a shell family to its rc file per contract D15 (the
// corrected D1 table). The zsh/bash/fish rows are the shells AI coding
// tools commonly run under; "unknown" never claims a specific file that
// might be wrong.
func shellRCFor(kind shellKind) shellRCFile {
	exportLine := func(dir string) string {
		return fmt.Sprintf(`export PATH="$PATH:%s"`, dir)
	}
	switch kind {
	case shellZsh:
		return shellRCFile{file: "~/.zshenv", line: exportLine}
	case shellBash:
		return shellRCFile{file: "~/.bashrc", line: exportLine}
	case shellFish:
		return shellRCFile{
			file: "~/.config/fish/config.fish",
			line: func(dir string) string { return fmt.Sprintf("fish_add_path %s", dir) },
		}
	default:
		return shellRCFile{file: "your shell's startup file", line: exportLine}
	}
}

// detectHookShell returns the shell binary to probe and its family,
// detected from $SHELL's basename. When $SHELL is unset, it defaults to
// the zsh row on darwin and the bash row on linux (contract D1).
func detectHookShell() (path string, kind shellKind) {
	if shellEnv := os.Getenv("SHELL"); shellEnv != "" {
		return shellEnv, classifyShell(shellEnv)
	}
	switch runtime.GOOS {
	case "darwin":
		return "/bin/zsh", shellZsh
	case "linux":
		return "/bin/bash", shellBash
	default:
		return "", shellUnknown
	}
}

func classifyShell(path string) shellKind {
	switch filepath.Base(path) {
	case "zsh":
		return shellZsh
	case "bash":
		return shellBash
	case "fish":
		return shellFish
	default:
		return shellUnknown
	}
}

// notFoundSentinel is what the probe script prints when `command -v`
// fails. The not-found answer travels on stdout rather than in the exit
// code because shells disagree on the code: bash and zsh exit 1, but dash
// -- which IS /bin/sh on Debian and Ubuntu -- exits 127. Reading the code
// instead made the check report "inconclusive" on most Linux machines,
// exactly where it was supposed to report "ox is off PATH".
const notFoundSentinel = "__OX_NOT_FOUND__"

// probeShellPath runs `<shellPath> -c "command -v <binary>"` under a
// scrubbed environment -- deliberately NOT inheriting the caller's PATH,
// since that would just re-test whatever shell launched `ox doctor` and
// reproduce the exact false-green this check exists to catch.
func probeShellPath(ctx context.Context, shellPath, binary string) (string, error) {
	if shellPath == "" {
		return "", ErrShellProbeInconclusive
	}
	if _, err := os.Stat(shellPath); err != nil {
		return "", ErrShellProbeInconclusive
	}

	// The `|| printf` makes a clean not-found exit 0, so a non-zero exit
	// now means only one thing: the shell itself failed.
	script := "command -v " + binary + " 2>/dev/null || printf '%s' " + notFoundSentinel
	cmd := exec.CommandContext(ctx, shellPath, "-c", script)
	cmd.Env = scrubbedShellEnv()

	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", ErrShellProbeInconclusive
		}
		// Any non-zero exit means the shell exited for a reason unrelated
		// to whether ox is on PATH: a config error in the user's own
		// startup file (zsh -c always sources ~/.zshenv, even
		// non-interactively), or an explicit `exit N` in it. That must be
		// reported as inconclusive, never misread as "ox is missing" -- a
		// supported shell dying in its own rc file is not the same fact as
		// ox being absent. This also covers failing to start the shell at
		// all (bad path, permission denied).
		return "", ErrShellProbeInconclusive
	}

	resolved := strings.TrimSpace(string(out))
	if resolved == notFoundSentinel {
		return "", ErrNotFoundInShell
	}
	if resolved == "" {
		// The script always prints either a path or the sentinel, so empty
		// output means we never saw the answer -- a startup file that
		// redirects stdout (`exec >/dev/null`) is the realistic cause. That
		// is unknown, not absent, and must not become an off-PATH warning.
		return "", ErrShellProbeInconclusive
	}
	return resolved, nil
}

// scrubbedShellEnv builds the minimal environment a non-interactive agent
// hook shell actually gets: HOME/USER so shell startup files resolve
// correctly, plus the platform-default PATH a login-less shell inherits
// before its own startup files run (the macOS/Linux "_PATH_DEFPATH" set) --
// the shell's own startup files are expected to extend it further.
//
// This trades off two failure modes, and picking the wrong side of either
// has already happened once on this exact line:
//   - Inheriting the caller's real PATH re-tests whatever shell launched
//     `ox doctor` and reproduces the exact false-green this check exists to
//     catch (a PATH entry ox needs lives only in ~/.zshrc, which the real
//     hook shell never sources).
//   - Scrubbing PATH down to nothing (or too little) makes a healthy
//     machine look off-PATH when a real hook shell would have resolved ox
//     via its inherited system PATH -- a false positive that trains people
//     to ignore doctor.
//
// The platform-default set is what a real non-interactive, non-login shell
// is actually seeded with before its own rc files run, so it reproduces
// that environment without importing the caller's PATH.
func scrubbedShellEnv() []string {
	env := []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		env = append(env, "HOME="+home)
	}
	if user := os.Getenv("USER"); user != "" {
		env = append(env, "USER="+user)
	}
	return env
}

// samePath reports whether two paths refer to the same file, resolving
// symlinks on both sides so a symlinked launcher isn't reported as
// "shadowed" by its own target.
func samePath(a, b string) bool {
	if a == b {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// compile-time interface check
var _ doctor.Check = (*OxInPathCheck)(nil)
