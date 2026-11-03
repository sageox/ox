package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/upgrade"
	"github.com/spf13/cobra"
)

var (
	installDirFlag   string
	installSkipLogin bool
)

// Seams for tests: the running binary, the login state and flow, the daemon
// restart, and the system-wide install directory.
var (
	installExecutable    = os.Executable
	installLoggedIn      = func() bool { ok, _ := auth.IsAuthCredentialValid(); return ok }
	installRunLogin      = runInstalledLogin
	installRetireDaemons = retireStaleDaemonsAfterUpgrade
	installSystemBinDir  = "/usr/local/bin"
)

var installCmd = &cobra.Command{
	Use:   "install",
	Args:  cobra.NoArgs,
	Short: "Install ox on this machine and log in",
	Long: `Copy ox and its adapters into a directory on PATH, then log in to SageOx.

This is what "npx @sageox/ox@latest install" runs. It sets up this machine
only: it changes no repository and installs no AI coworker hooks. To link a
repository afterwards, run ox init inside it.

The binaries go to /usr/local/bin when it is writable, otherwise to
~/.local/bin, the same places install.sh uses. An ox managed by Homebrew is
left in place.`,
	Example: `  # Install from npm and log in
  npx @sageox/ox@latest install

  # Install into a specific directory without logging in
  ox install --dir ~/bin --skip-login`,
	RunE: runInstall,
}

func init() {
	installCmd.Flags().StringVar(&installDirFlag, "dir", "", "directory to install into (default: /usr/local/bin if writable, else ~/.local/bin)")
	installCmd.Flags().BoolVar(&installSkipLogin, "skip-login", false, "install without running ox login (default: false)")
}

// installResult is the --json output of ox install.
type installResult struct {
	Status   string   `json:"status"` // installed, already_installed, homebrew, npm_global
	OxPath   string   `json:"ox_path"`
	Binaries []string `json:"binaries,omitempty"`
	OnPath   bool     `json:"on_path"`
	LoggedIn bool     `json:"logged_in"`
	Next     []string `json:"next"`
}

func runInstall(cmd *cobra.Command, _ []string) error {
	jsonOutput, _ := cmd.Flags().GetBool("json")
	out := cmd.OutOrStdout()
	if jsonOutput {
		out = io.Discard
	}

	exe, err := installExecutable()
	if err != nil {
		return fmt.Errorf("locate running ox: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	result, err := installBinaries(out, filepath.Dir(exe), installDirFlag)
	if err != nil {
		return err
	}
	oxPath := result.OxPath

	if jsonOutput {
		// ox login is interactive, so --json reports the login state instead
		result.LoggedIn = installLoggedIn()
		result.OnPath = dirOnPath(filepath.Dir(oxPath))
		ox := "ox"
		if found := lookPathOutsideNodeModules("ox"); found == "" || !sameFile(found, oxPath) {
			ox = shellQuote(oxPath)
		}
		if !result.LoggedIn {
			result.Next = append(result.Next, ox+" login")
		}
		result.Next = append(result.Next, "cd <repo> && "+ox+" init")
		return cli.PrintJSONTo(cmd.OutOrStdout(), result)
	}

	ranLogin := false
	if !installSkipLogin {
		if ranLogin, err = installLogin(out, oxPath); err != nil {
			return err
		}
	}

	// a completed `ox login` already ends by pointing at ox init
	if !ranLogin {
		fmt.Fprintln(out)
		fmt.Fprintf(out, "To link a repo to SageOx: %s\n", cli.StyleCommand.Render("cd <repo> && ox init"))
	}
	return nil
}

// installBinaries copies ox and its adapters from srcDir into the install
// directory and reports the ox that later steps should run.
func installBinaries(out io.Writer, srcDir, dirFlag string) (installResult, error) {
	if dirFlag == "" {
		if existing := lookPathOutsideNodeModules("ox"); existing != "" {
			resolved, err := filepath.EvalSymlinks(existing)
			switch {
			case err != nil:
			case isHomebrewInstall(resolved):
				fmt.Fprintf(out, "ox is managed by Homebrew at %s; update it with %s\n",
					existing, cli.StyleCommand.Render("brew upgrade sageox/tap/ox"))
				return installResult{Status: "homebrew", OxPath: existing}, nil
			case strings.Contains(filepath.ToSlash(resolved), "/node_modules/"):
				// a global npm install owns this path; replacing it would break npm uninstall
				fmt.Fprintf(out, "ox is installed globally with npm at %s; update it with %s\n",
					existing, cli.StyleCommand.Render("npm install -g @sageox/ox@latest"))
				return installResult{Status: "npm_global", OxPath: existing}, nil
			}
		}
	}

	destDir, err := resolveInstallTarget(dirFlag)
	if err != nil {
		return installResult{}, err
	}
	oxPath := filepath.Join(destDir, "ox")
	if sameDir(srcDir, destDir) {
		fmt.Fprintf(out, "ox is already installed at %s\n", oxPath)
		warnInstallPath(out, destDir, oxPath)
		return installResult{Status: "already_installed", OxPath: oxPath}, nil
	}

	_, statErr := os.Stat(oxPath)
	replaced := statErr == nil

	names, err := upgrade.InstallFromDir(srcDir, destDir)
	if errors.Is(err, upgrade.ErrNotWritable) {
		return installResult{}, fmt.Errorf("%w\n  Choose another directory with: ox install --dir <dir>", err)
	}
	if err != nil {
		return installResult{}, err
	}
	if replaced {
		// a daemon still running the replaced ox restarts on the new one
		installRetireDaemons()
	}

	cli.PrintSuccessTo(out, fmt.Sprintf("Installed ox%s to %s", adapterSuffix(len(names)-1), destDir))
	warnInstallPath(out, destDir, oxPath)
	return installResult{Status: "installed", OxPath: oxPath, Binaries: names}, nil
}

func adapterSuffix(n int) string {
	switch n {
	case 0:
		return ""
	case 1:
		return " and 1 adapter"
	default:
		return fmt.Sprintf(" and %d adapters", n)
	}
}

// resolveInstallTarget returns dirFlag, or the first usable default: the
// system bin directory when writable, else ~/.local/bin.
func resolveInstallTarget(dirFlag string) (string, error) {
	if dirFlag != "" {
		dir, err := filepath.Abs(dirFlag)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("create %s: %w", dir, err)
		}
		return dir, nil
	}
	if dirWritable(installSystemBinDir) {
		return installSystemBinDir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	dir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	return dir, nil
}

func dirWritable(dir string) bool {
	probe, err := os.CreateTemp(dir, ".ox-install-probe-*")
	if err != nil {
		return false
	}
	_ = probe.Close()
	_ = os.Remove(probe.Name())
	return true
}

// warnInstallPath tells the user when destDir is not on PATH, or when another
// ox earlier on PATH would run instead of the one just installed.
func warnInstallPath(out io.Writer, destDir, oxPath string) {
	if !dirOnPath(destDir) {
		if filepath.Base(os.Getenv("SHELL")) == "fish" {
			cli.PrintWarningTo(out, destDir+" is not on your PATH. Add it with:")
			fmt.Fprintf(out, "  fish_add_path \"%s\"\n", destDir)
			return
		}
		cli.PrintWarningTo(out, destDir+" is not on your PATH. Add this line to your shell profile:")
		fmt.Fprintf(out, "  export PATH=\"%s:$PATH\"\n", destDir)
		return
	}
	if found := lookPathOutsideNodeModules("ox"); found != "" && !sameFile(found, oxPath) {
		cli.PrintWarningTo(out, fmt.Sprintf("Another ox at %s comes first on your PATH and will run instead.", found))
	}
}

func dirOnPath(dir string) bool {
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if d != "" && sameDir(d, dir) {
			return true
		}
	}
	return false
}

// lookPathOutsideNodeModules is exec.LookPath that skips node_modules
// directories, so the npx launcher on PATH is not mistaken for an install.
func lookPathOutsideNodeModules(name string) string {
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if d == "" || strings.Contains(filepath.ToSlash(d)+"/", "/node_modules/") {
			continue
		}
		candidate := filepath.Join(d, name)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return candidate
		}
	}
	return ""
}

func sameDir(a, b string) bool {
	return sameFile(a, b)
}

func sameFile(a, b string) bool {
	ai, aerr := os.Stat(a)
	bi, berr := os.Stat(b)
	if aerr != nil || berr != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return os.SameFile(ai, bi)
}

// installLogin runs `ox login` with the installed binary unless the user is
// already logged in or no one is there to answer it. It reports whether the
// login flow ran.
func installLogin(out io.Writer, oxPath string) (bool, error) {
	if installLoggedIn() {
		fmt.Fprintln(out, "Already logged in to SageOx.")
		return false, nil
	}
	if cli.NoInput() {
		fmt.Fprintf(out, "Log in with: %s\n", cli.StyleCommand.Render("ox login"))
		return false, nil
	}
	fmt.Fprintln(out)
	if err := installRunLogin(oxPath); err != nil {
		return true, err
	}
	return true, nil
}

func runInstalledLogin(oxPath string) error {
	c := exec.Command(oxPath, "login")
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := c.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return &commandExitError{ExitCode: exitErr.ExitCode(), Message: "ox is installed, but login did not finish. Run ox login to try again."}
	}
	if err != nil {
		return fmt.Errorf("run ox login: %w", err)
	}
	return nil
}
