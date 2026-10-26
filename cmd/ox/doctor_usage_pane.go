package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"

	"github.com/sageox/ox/internal/claudemod"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/version"
)

// CheckSlugUsagePane is the slug for the /ox-usage pane check.
const CheckSlugUsagePane = "usage-pane"

const (
	usagePaneCheckName    = "Usage pane"
	usagePaneInstalledMsg = "installed; run /reload-plugins in open Claude Code sessions, then /ox-usage"
)

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugUsagePane,
		Name:        usagePaneCheckName,
		Category:    "Integration",
		FixLevel:    FixLevelAuto,
		Description: "Installs the /ox-usage pane plugin into Claude Code when the usage_pane setting is on, and removes it when off",
		Run: func(fix bool) checkResult {
			enabled, err := usagePaneSettingOn()
			if err != nil {
				return SkippedCheck(usagePaneCheckName, "could not read the usage_pane setting", err.Error())
			}
			return checkUsagePane(newUsagePaneInstaller(), enabled, version.Version, fix)
		},
	})
}

// usagePaneSettingOn reads the user-level usage_pane setting (default on).
func usagePaneSettingOn() (bool, error) {
	cfg, err := config.LoadUserConfig()
	if err != nil {
		return false, err
	}
	return cfg.IsUsagePaneEnabled(), nil
}

// checkUsagePane keeps Claude Code's copy of the /ox-usage pane in line with
// the usage_pane setting: installed on this ox's version when on, gone when
// off. Without the claude CLI there is nothing ox can register, so the check
// is skipped rather than half-installing the pane.
func checkUsagePane(in claudemod.Installer, enabled bool, want string, fix bool) checkResult {
	const name = usagePaneCheckName
	ctx := context.Background()
	if err := claudemod.RecoverSwap(in.Dir); err != nil {
		return FailedCheck(name, "interrupted refresh", err.Error())
	}
	if in.Claude == nil {
		return SkippedCheck(name, "claude CLI not found", "")
	}
	in.Version = want
	st, err := in.Status(ctx)
	if err != nil {
		// Status's only claude call is `plugin list`; a CLI that cannot
		// answer it has no plugin support ox can use.
		if claudeLacksPlugins(err) {
			return SkippedCheck(name, "claude CLI too old for plugins", "")
		}
		return SkippedCheck(name, "could not list Claude Code plugins", err.Error())
	}

	if enabled {
		written, _ := claudemod.WrittenVersion(in.Dir)
		if st.Installed && written == want && st.InstalledVersion == want {
			return PassedCheck(name, "installed")
		}
		if !fix {
			msg := "not installed"
			if st.Installed || st.FilesPresent {
				msg = "from an older ox"
			}
			return WarningCheck(name, msg, "Run `ox doctor --fix` or `ox usage pane enable`")
		}
		if _, err := in.Enable(ctx); err != nil {
			return usagePaneCLIFailure(name, "install failed", err)
		}
		return PassedCheck(name, usagePaneInstalledMsg)
	}

	_, statErr := os.Stat(in.Dir)
	if errors.Is(statErr, fs.ErrNotExist) && !st.Installed {
		return PassedCheck(name, "off")
	}
	if !fix {
		return WarningCheck(name, "off but still installed", "Run `ox doctor --fix` or `ox usage pane disable`")
	}
	if _, err := in.Disable(ctx); err != nil {
		return usagePaneCLIFailure(name, "removal failed", err)
	}
	return PassedCheck(name, "removed (usage_pane is off)")
}

// usagePaneCLIFailure reports a failed claude CLI call, as skipped when the
// CLI predates plugins (nothing ox can fix) and as failed otherwise.
func usagePaneCLIFailure(name, msg string, err error) checkResult {
	if claudeLacksPlugins(err) {
		return SkippedCheck(name, "claude CLI too old for plugins", "")
	}
	return FailedCheck(name, msg, err.Error())
}

// claudeLacksPlugins reports whether a claude CLI error says the CLI has no
// `plugin` subcommand (or none of the flags ox passes it).
func claudeLacksPlugins(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unknown command") || strings.Contains(msg, "unknown option")
}

// ensureUsagePane installs the /ox-usage pane from `ox init` when the
// usage_pane setting is on and the claude CLI is present. Best-effort: it
// never fails init, and prints one line when it installed the pane.
func ensureUsagePane(quiet bool) {
	enabled, err := usagePaneSettingOn()
	if err != nil || !enabled {
		return
	}
	in := newUsagePaneInstaller()
	if in.Claude == nil {
		return
	}
	res := checkUsagePane(in, true, version.Version, true)
	switch {
	case res.skipped:
	case !res.passed:
		cli.PrintWarning("Could not install the /ox-usage pane: " + res.detail)
	case res.message == usagePaneInstalledMsg && !quiet:
		cli.PrintHint("Installed the /ox-usage pane in Claude Code. " + usagePaneReadyHint)
	}
}

// applyUsagePaneSetting brings Claude Code in line with the usage_pane
// setting right after `ox config set|unset` changes it. It does nothing
// without the claude CLI; `ox doctor --fix` reconciles later. The returned
// hint is for the coworker when the pane was installed.
func applyUsagePaneSetting(ctx context.Context) (string, error) {
	in := newUsagePaneInstaller()
	if in.Claude == nil {
		return "", nil
	}
	enabled, err := usagePaneSettingOn()
	if err != nil {
		return "", err
	}
	if enabled {
		if _, err := in.Enable(ctx); err != nil {
			return "", err
		}
		return usagePaneReadyHint, nil
	}
	if _, err := in.Disable(ctx); err != nil {
		return "", err
	}
	return "", nil
}
