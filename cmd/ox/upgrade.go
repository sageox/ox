package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/upgrade"
	"github.com/sageox/ox/internal/version"
	"github.com/spf13/cobra"
)

// installMethod describes how ox was installed on this system.
type installMethod string

const (
	installHomebrew  installMethod = "homebrew"
	installGoInstall installMethod = "go-install"
	installBinary    installMethod = "binary"
	installSource    installMethod = "source"
)

type upgradeResult struct {
	Status          string        `json:"status"`
	PreviousVersion string        `json:"previous_version"`
	NewVersion      string        `json:"new_version,omitempty"`
	InstallMethod   installMethod `json:"install_method"`
	ReleaseURL      string        `json:"release_url,omitempty"`
	Message         string        `json:"message,omitempty"`
	DaemonsStopped  int           `json:"daemons_stopped,omitempty"`
}

var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Args:  cobra.NoArgs,
	Short: "Upgrade ox to the latest or a specific version",
	Long: `Detect how ox was installed and upgrade using the appropriate method: Homebrew, go install, or an in-place download that verifies and replaces the binary.

Targets must be release versions such as v0.18.0. Go installations require the
v prefix and do not accept build metadata other than +incompatible.
For Go and direct binary installations, --target installs the specified release
without checking the latest release. It can reinstall the current version or
select an older release. Homebrew and source installations do not support --target.

When ox is on PATH, the upgrade succeeds only if that ox then reports the new
version. An installer can finish without changing it: Homebrew does when its
tap does not have the release yet, and an ox earlier on PATH hides one
upgraded elsewhere.

Failed update checks and installations exit with status 1. With --json,
stdout contains only the result; installer logs are written to stderr.`,
	RunE: runUpgrade,
}

func init() {
	upgradeCmd.Flags().Bool("json", false, "output as JSON")
	upgradeCmd.Flags().String("target", "",
		"pin upgrade to a specific version tag (e.g. v0.18.0); empty = latest from GitHub releases API")
}

func runUpgrade(cmd *cobra.Command, _ []string) error {
	jsonOutput, _ := cmd.Flags().GetBool("json")
	target, _ := cmd.Flags().GetString("target")
	method := installMethodDetector()
	result := upgradeResult{
		PreviousVersion: version.Version,
		InstallMethod:   method,
	}

	if err := validateUpgradeTarget(method, target); err != nil {
		result.Status = "failed"
		result.Message = err.Error()
		return outputUpgradeResult(cmd, result, jsonOutput)
	}

	newVersion := strings.TrimPrefix(target, "v")
	if target == "" {
		vResult := checkVersionFromCache()
		if vResult == nil {
			// No cached update is available; check the current release directly.
			latestTag, err := latestReleaseFetcher()
			if err == nil && strings.TrimPrefix(latestTag, "v") == "" {
				err = errors.New("GitHub returned an empty release tag")
			}
			if err != nil {
				result.Status = "failed"
				result.Message = fmt.Sprintf("could not check for updates: %v", err)
				return outputUpgradeResult(cmd, result, jsonOutput)
			}

			latest := strings.TrimPrefix(latestTag, "v")
			current := strings.TrimPrefix(version.Version, "v")
			vResult = &versionCheckResult{
				UpdateAvailable: isNewerVersion(latest, current),
				LatestVersion:   latest,
				CurrentVersion:  current,
			}
			// Caching is best effort; the live result must survive a cache write failure.
			writeVersionCacheFromDoctor(latestTag)
		}

		if !vResult.UpdateAvailable {
			result.Status = "up-to-date"
			result.Message = fmt.Sprintf("ox v%s is already the latest version", version.Version)
			return outputUpgradeResult(cmd, result, jsonOutput)
		}
		newVersion = vResult.LatestVersion
	}

	result.NewVersion = newVersion
	result.ReleaseURL = fmt.Sprintf("https://github.com/sageox/ox/releases/tag/v%s", newVersion)

	if !jsonOutput {
		fmt.Printf("%s v%s → v%s\n\n",
			cli.StyleBrand.Render("ox"),
			cli.StyleDim.Render(strings.TrimPrefix(version.Version, "v")),
			cli.StyleSuccess.Render(newVersion))
		fmt.Printf("%s %s\n", cli.StyleDim.Render("Install method:"), string(method))
	}

	// perform upgrade based on install method
	var err error
	switch method {
	case installHomebrew:
		err = upgradeViaHomebrew(jsonOutput)
	case installGoInstall:
		// Require an operator-supplied pin, even when the release check selected
		// a concrete version. A cached or API-selected release is not explicit.
		if target == "" && os.Getenv("OX_UPGRADE_REQUIRE_PIN") == "1" {
			err = fmt.Errorf("OX_UPGRADE_REQUIRE_PIN=1 set: pass --target=<version> explicitly")
		} else {
			err = upgradeViaGoInstallWithTarget(jsonOutput, "v"+newVersion)
		}
	case installSource:
		result.Status = "manual"
		result.Message = "Dev build detected. Use 'make build && make install' to upgrade."
		return outputUpgradeResult(cmd, result, jsonOutput)
	case installBinary:
		err = upgradeViaSelfReplace(jsonOutput, newVersion)
	}
	if err == nil {
		var installed string
		installed, err = confirmUpgradeOnPath(method, strings.TrimPrefix(version.Version, "v"), newVersion)
		if err == nil && installed != newVersion {
			// Homebrew installed a newer release than the one selected.
			newVersion = installed
			result.NewVersion = newVersion
			result.ReleaseURL = fmt.Sprintf("https://github.com/sageox/ox/releases/tag/v%s", newVersion)
		}
	}

	if err != nil {
		result.Status = "failed"
		result.Message = err.Error()
		return outputUpgradeResult(cmd, result, jsonOutput)
	}

	// clear version cache (and the notice ledger with it) so the next check
	// starts from a clean slate
	clearVersionCacheAfterUpgrade()

	result.Status = "upgraded"
	result.Message = fmt.Sprintf("Upgraded to v%s", newVersion)
	// This process still contains the OLD compiled-in version and skill catalog,
	// even after brew/go install/self-replace updates the executable on disk. It
	// may safely stop old daemons, but must leave inventory reconciliation to the
	// next invocation of the new binary (`ox agent prime`). Running maintenance
	// here, before rendering, also keeps --json behavior identical to text mode.
	result.DaemonsStopped = retireStaleDaemonsAfterUpgrade()
	return outputUpgradeResult(cmd, result, jsonOutput)
}

// validateUpgradeTarget rejects installation paths that cannot honor a pinned
// release. Accepting --target and then installing an arbitrary latest version
// is worse than failing: it creates a false compatibility guarantee.
func validateUpgradeTarget(method installMethod, target string) error {
	// go-install and direct-binary (self-replace) installs can both honor a
	// pinned version — one via go install @tag, the other by downloading that
	// tag's release tarball. Homebrew and dev/source builds cannot.
	if target == "" {
		return nil
	}
	if method != installGoInstall && method != installBinary {
		return fmt.Errorf("--target is supported only for go-install and binary installations; %s upgrades cannot safely honor a pinned release", method)
	}
	// Go treats noncanonical spellings as revision queries, which can follow branches.
	_, metadata, _ := strings.Cut(target, "+")
	goRevision := method == installGoInstall && (!strings.HasPrefix(target, "v") || (metadata != "" && metadata != "incompatible"))
	if goRevision || !upgrade.IsValidVersion(strings.TrimPrefix(target, "v")) {
		return fmt.Errorf("--target must be a release version such as v0.18.0; got %q", target)
	}
	return nil
}

func outputUpgradeResult(cmd *cobra.Command, result upgradeResult, jsonOutput bool) error {
	if jsonOutput {
		if err := cli.PrintJSONTo(cmd.OutOrStdout(), result); err != nil {
			return err
		}
	} else {
		switch result.Status {
		case "up-to-date":
			fmt.Printf("%s %s\n",
				cli.StyleSuccess.Render("✓"),
				result.Message)
		case "upgraded":
			fmt.Printf("\n%s %s\n", cli.StyleSuccess.Render("✓"), result.Message)
			fmt.Printf("%s %s\n", cli.StyleDim.Render("Release notes:"), result.ReleaseURL)
			if result.DaemonsStopped > 0 {
				fmt.Printf("%s %s\n", cli.StyleDim.Render("Daemons:"),
					"stopped so they restart on the new version (they respawn on demand)")
			}
			fmt.Printf("%s %s\n", cli.StyleDim.Render("Tip:"), "Restart your terminal to pick up the new binary in this shell")
		case "manual":
			fmt.Printf("\n%s\n", result.Message)
			if result.ReleaseURL != "" {
				fmt.Printf("%s %s\n", cli.StyleDim.Render("Release notes:"), result.ReleaseURL)
			}
		case "failed":
			fmt.Fprintf(cmd.ErrOrStderr(), "\n%s %s\n", cli.StyleWarning.Render("✗"), result.Message)
		}
	}

	if result.Status == "failed" {
		return &commandExitError{ExitCode: 1, Message: result.Message}
	}
	return nil
}

// upgradeViaSelfReplace downloads the release tarball for a directly-installed
// (install.sh / manual binary) ox and atomically replaces the running binary
// and any bundled adapters in place. The tarball's SHA-256 is verified against
// the release checksums before anything is overwritten.
func upgradeViaSelfReplace(quiet bool, targetVersion string) error {
	if !quiet {
		fmt.Printf("%s downloading ox v%s (%s/%s) and verifying checksum...\n",
			cli.StyleDim.Render("Running:"), targetVersion, runtime.GOOS, runtime.GOARCH)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	err := upgrade.ReplaceRunningBinary(ctx, upgrade.Config{
		Version: targetVersion,
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
	})
	if errors.Is(err, upgrade.ErrNotWritable) {
		return fmt.Errorf("%w\n  Re-run with elevated permissions: sudo ox upgrade", err)
	}
	return err
}

// confirmUpgradeOnPath checks the ox a shell now resolves and returns the
// version it reports. That must be want, except under Homebrew, which installs
// whatever its tap has, so any version newer than from counts. An installer
// can exit 0 without changing that binary: brew does when its sageox/tap
// formula has no newer version, and an ox earlier on PATH shadows one upgraded
// in another directory.
func confirmUpgradeOnPath(method installMethod, from, want string) (string, error) {
	path, err := exec.LookPath("ox")
	if errors.Is(err, exec.ErrNotFound) {
		// No ox on PATH means no `ox` for a shell to run, so nothing to confirm.
		return want, nil
	}
	if err != nil {
		return "", fmt.Errorf("could not confirm the upgrade: %w", err)
	}
	// Bounded because this runs whatever PATH resolves as "ox".
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version", "--json").Output()
	if err != nil {
		return "", fmt.Errorf("could not confirm the upgrade: %s version --json: %w", path, err)
	}
	var info versionInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return "", fmt.Errorf("could not confirm the upgrade: %s version --json: %w", path, err)
	}
	got := strings.TrimPrefix(info.Version, "v")
	if got == want || (method == installHomebrew && isNewerVersion(got, from)) {
		return got, nil
	}
	cause := "another ox comes first on PATH"
	if method == installHomebrew {
		cause = "Homebrew's sageox/tap does not have it yet (brew exits successfully without upgrading), or " + cause
	}
	return "", fmt.Errorf("ox on PATH (%s) reports v%s, not v%s: %s", path, got, want, cause)
}

func upgradeViaHomebrew(quiet bool) error {
	if !quiet {
		fmt.Printf("%s brew upgrade sageox/tap/ox\n", cli.StyleDim.Render("Running:"))
	}
	cmd := exec.Command("brew", "upgrade", "sageox/tap/ox")
	cmd.Stdout = os.Stdout
	if quiet {
		cmd.Stdout = os.Stderr
	}
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// adapterPackages are the go install targets for bundled adapter binaries.
// these must be upgraded alongside ox to keep protocol versions in sync.
var adapterPackages = []string{
	"github.com/sageox/ox/cmd/ox-adapter-claude-code",
	"github.com/sageox/ox/cmd/ox-adapter-gemini",
	"github.com/sageox/ox/cmd/ox-adapter-codex",
	"github.com/sageox/ox/cmd/ox-adapter-amp",
	"github.com/sageox/ox/cmd/ox-adapter-opencode",
	"github.com/sageox/ox/cmd/ox-adapter-pi",
	"github.com/sageox/ox/cmd/ox-adapter-omp",
	"github.com/sageox/ox/cmd/ox-adapter-aider",
	"github.com/sageox/ox/cmd/ox-adapter-droid",
	"github.com/sageox/ox/cmd/ox-adapter-goose",
}

func upgradeViaGoInstallWithTarget(quiet bool, target string) error {
	// install ox and all bundled adapters in one invocation, pinned to the
	// same target version so their protocol versions stay in sync.
	args := []string{"install", "github.com/sageox/ox/cmd/ox@" + target}
	for _, pkg := range adapterPackages {
		args = append(args, pkg+"@"+target)
	}
	if !quiet {
		fmt.Printf("%s go %s\n", cli.StyleDim.Render("Running:"), strings.Join(args, " "))
	}
	cmd := exec.Command("go", args...)
	cmd.Stdout = os.Stdout
	if quiet {
		cmd.Stdout = os.Stderr
	}
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// installMethodDetector and readBuildInfo are indirected so tests can choose
// the install method and describe a `go install` build.
var (
	installMethodDetector = detectInstallMethod
	readBuildInfo         = debug.ReadBuildInfo
)

// detectInstallMethod determines how ox was installed from its build info and
// binary path.
func detectInstallMethod() installMethod {
	// `go install …@version` sets no ldflags, so BuildDate is "unknown" as in a
	// dev build, but only a downloaded module has its checksum recorded.
	if info, ok := readBuildInfo(); ok && info.Main.Sum != "" {
		return installGoInstall
	}

	// dev build check
	if version.BuildDate == "unknown" || version.BuildDate == "" {
		return installSource
	}
	if strings.Contains(version.Version, "dev") || strings.Contains(version.Version, "dirty") {
		return installSource
	}

	// find where the ox binary lives
	oxPath, err := os.Executable()
	if err != nil {
		return installBinary
	}
	oxPath, _ = filepath.EvalSymlinks(oxPath)

	if isHomebrewInstall(oxPath) {
		return installHomebrew
	}

	// go install check: binary is under GOBIN or GOPATH/bin
	if isGoInstall(oxPath) {
		return installGoInstall
	}

	return installBinary
}

// isHomebrewInstall reports whether oxPath, with symlinks resolved, is in a
// keg of the ox formula; every Homebrew prefix keeps kegs under
// <prefix>/Cellar/<formula>/<version>/. It deliberately does not ask brew
// whether the formula is installed: that is true even when the running ox is
// a different install, and upgrading the brew copy would leave it unchanged.
func isHomebrewInstall(oxPath string) bool {
	return strings.Contains(oxPath, "/Cellar/ox/")
}

func isGoInstall(oxPath string) bool {
	// check GOBIN
	gobin, err := exec.Command("go", "env", "GOBIN").Output()
	if err == nil {
		bin := strings.TrimSpace(string(gobin))
		if bin != "" && strings.HasPrefix(oxPath, bin) {
			return true
		}
	}

	// check GOPATH/bin
	gopath, err := exec.Command("go", "env", "GOPATH").Output()
	if err == nil {
		bin := filepath.Join(strings.TrimSpace(string(gopath)), "bin")
		if strings.HasPrefix(oxPath, bin) {
			return true
		}
	}

	return false
}
