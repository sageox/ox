package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/cli"
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
}

var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Upgrade ox to the latest version",
	Long:  "Detect how ox was installed and upgrade using the appropriate method (Homebrew, go install, or manual download).",
	RunE:  runUpgrade,
}

func init() {
	upgradeCmd.Flags().Bool("json", false, "output as JSON")
	upgradeCmd.Flags().String("target", "",
		"pin upgrade to a specific version tag (e.g. v0.18.0); empty = latest from GitHub releases API")
	// ox-zbi5: when this env var is set, refuse to upgrade with an @latest
	// target. Forces operators to specify a pinned version, defeating the
	// "compromised GOPROXY serves backdoored bytes for @latest" path.
	// Defaults off so the typical interactive upgrade still works.
}

func runUpgrade(cmd *cobra.Command, _ []string) error {
	jsonOutput, _ := cmd.Flags().GetBool("json")
	method := detectInstallMethod()

	// check if update is available
	vResult := checkVersionFromCache()

	if vResult == nil {
		// no cache — try a live check
		latestTag, err := getLatestGitHubRelease()
		if err == nil {
			writeVersionCacheFromDoctor(latestTag)
			vResult = checkVersionFromCache()
		}
	}

	result := upgradeResult{
		PreviousVersion: version.Version,
		InstallMethod:   method,
	}

	// treat "no cache" and "already current" the same
	if vResult == nil || !vResult.UpdateAvailable {
		result.Status = "up-to-date"
		result.Message = fmt.Sprintf("ox v%s is already the latest version", version.Version)
		return outputUpgradeResult(cmd, result, jsonOutput)
	}

	result.NewVersion = vResult.LatestVersion
	result.ReleaseURL = fmt.Sprintf("https://github.com/sageox/ox/releases/tag/v%s", vResult.LatestVersion)

	if !jsonOutput {
		fmt.Printf("%s v%s → v%s\n\n",
			cli.StyleBrand.Render("ox"),
			cli.StyleDim.Render(vResult.CurrentVersion),
			cli.StyleSuccess.Render(vResult.LatestVersion))
		fmt.Printf("%s %s\n", cli.StyleDim.Render("Install method:"), string(method))
	}

	// perform upgrade based on install method
	target, _ := cmd.Flags().GetString("target")
	var err error
	switch method {
	case installHomebrew:
		err = upgradeViaHomebrew(jsonOutput)
	case installGoInstall:
		err = upgradeViaGoInstallWithTarget(jsonOutput, target)
	case installSource:
		result.Status = "manual"
		result.Message = "Dev build detected. Use 'make build && make install' to upgrade."
		return outputUpgradeResult(cmd, result, jsonOutput)
	case installBinary:
		result.Status = "manual"
		result.Message = "Download the latest release or run:\n  curl -sSL https://raw.githubusercontent.com/sageox/ox/main/scripts/install.sh | bash"
		return outputUpgradeResult(cmd, result, jsonOutput)
	}

	if err != nil {
		result.Status = "failed"
		result.Message = err.Error()
		return outputUpgradeResult(cmd, result, jsonOutput)
	}

	// clear version cache so next check picks up new version
	_ = os.Remove(versionCacheFile)

	result.Status = "upgraded"
	result.Message = fmt.Sprintf("Upgraded to v%s", vResult.LatestVersion)
	return outputUpgradeResult(cmd, result, jsonOutput)
}

func outputUpgradeResult(cmd *cobra.Command, result upgradeResult, jsonOutput bool) error {
	if jsonOutput {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	switch result.Status {
	case "up-to-date":
		fmt.Printf("%s %s\n",
			cli.StyleSuccess.Render("✓"),
			result.Message)
	case "upgraded":
		fmt.Printf("\n%s %s\n", cli.StyleSuccess.Render("✓"), result.Message)
		fmt.Printf("%s %s\n", cli.StyleDim.Render("Release notes:"), result.ReleaseURL)
		fmt.Printf("%s %s\n", cli.StyleDim.Render("Tip:"), "Restart your terminal or run 'ox daemon restart'")
	case "manual":
		fmt.Printf("\n%s\n", result.Message)
		if result.ReleaseURL != "" {
			fmt.Printf("%s %s\n", cli.StyleDim.Render("Release notes:"), result.ReleaseURL)
		}
	case "failed":
		fmt.Printf("\n%s %s\n", cli.StyleWarning.Render("✗"), result.Message)
	}

	return nil
}

func upgradeViaHomebrew(quiet bool) error {
	if !quiet {
		fmt.Printf("%s brew upgrade sageox/tap/ox\n", cli.StyleDim.Render("Running:"))
	}
	cmd := exec.Command("brew", "upgrade", "sageox/tap/ox")
	cmd.Stdout = os.Stdout
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
	"github.com/sageox/ox/cmd/ox-adapter-aider",
}

// resolveUpgradeTarget returns the version tag to install. Per ox-zbi5:
//   - --target=<tag> wins (operator explicit choice).
//   - Else fetch the latest tag from the GitHub releases API and pin to it.
//   - When OX_UPGRADE_REQUIRE_PIN=1, refuse @latest entirely; the operator
//     MUST specify --target. Defends against a compromised GOPROXY by
//     forcing an explicit version that the operator chose out-of-band.
//
// Returns "latest" as a fall-through ONLY when the require-pin gate is
// not set AND the GitHub release fetch failed; callers see this and can
// warn loudly before invoking go install.
func resolveUpgradeTarget(targetFlag string) (string, error) {
	if targetFlag != "" {
		return targetFlag, nil
	}
	if os.Getenv("OX_UPGRADE_REQUIRE_PIN") == "1" {
		return "", fmt.Errorf("OX_UPGRADE_REQUIRE_PIN=1 set: pass --target=<version> explicitly")
	}
	if tag, err := getLatestGitHubRelease(); err == nil && tag != "" {
		return tag, nil
	}
	return "latest", nil
}

func upgradeViaGoInstallWithTarget(quiet bool, targetFlag string) error {
	target, err := resolveUpgradeTarget(targetFlag)
	if err != nil {
		return err
	}
	if target == "latest" && !quiet {
		fmt.Fprintln(os.Stderr, cli.StyleDim.Render(
			"WARNING: upgrading to @latest because the GitHub releases API "+
				"could not be reached. The bytes go install fetches are "+
				"determined by GOPROXY. To pin: pass --target=v0.x.y or "+
				"set OX_UPGRADE_REQUIRE_PIN=1."))
	}

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
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// detectInstallMethod determines how ox was installed by examining the binary path.
func detectInstallMethod() installMethod {
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

	// homebrew check: try brew list first (authoritative)
	if isHomebrewInstall(oxPath) {
		return installHomebrew
	}

	// go install check: binary is under GOBIN or GOPATH/bin
	if isGoInstall(oxPath) {
		return installGoInstall
	}

	return installBinary
}

// homebrewPrefixes are common Homebrew install path prefixes.
var homebrewPrefixes = []string{
	"/opt/homebrew/",
	"/usr/local/Cellar/",
	"/home/linuxbrew/.linuxbrew/",
}

func isHomebrewInstall(oxPath string) bool {
	// fast path: check common Homebrew prefixes
	for _, prefix := range homebrewPrefixes {
		if strings.HasPrefix(oxPath, prefix) {
			return true
		}
	}

	// slow path: ask brew directly (handles custom prefixes)
	cmd := exec.Command("brew", "list", "sageox/tap/ox")
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
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
