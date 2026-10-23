package main

import (
	"errors"
	"io/fs"
	"os"
	"strings"

	"github.com/sageox/ox/internal/pet"
	"github.com/sageox/ox/internal/version"
)

// CheckSlugPetOxy is the slug for the Oxy pet refresh check.
const CheckSlugPetOxy = "pet-oxy"

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugPetOxy,
		Name:        "Oxy pet",
		Category:    "Integration",
		FixLevel:    FixLevelAuto,
		Description: "Rewrites Oxy's files for Claude Code and Codex when an ox upgrade left them from an older version",
		Run: func(fix bool) checkResult {
			return checkPetOxy(newPetInstaller(), newCodexPetInstaller(), version.Version, fix)
		},
	})
}

// petHostCheck is one host's part of the Oxy check.
type petHostCheck int

const (
	petHostSkipped petHostCheck = iota // never enabled
	petHostCurrent
	petHostStale     // stale, and fix is off
	petHostRefreshed // stale, and rewritten
)

// checkPetOxy keeps an enabled Oxy on this ox's version, in Claude Code and
// in Codex. Both hosts read Oxy's folder in place, so rewriting it is the
// whole refresh: no claude CLI call, no config.toml edit, and nothing outside
// Oxy's own folders is touched. A host that never ran `ox pet enable` is
// skipped, so the pet stays opt-in.
func checkPetOxy(claude pet.Installer, codex pet.CodexInstaller, want string, fix bool) checkResult {
	const name = "Oxy pet"
	claudeState, err := checkPetClaude(claude, want, fix)
	if err != nil {
		return FailedCheck(name, "Claude Code refresh failed", err.Error())
	}
	codexState, err := checkPetCodex(codex, want, fix)
	if err != nil {
		return FailedCheck(name, "Codex refresh failed", err.Error())
	}

	var stale, refreshed []string
	for _, h := range []struct {
		host  string
		state petHostCheck
	}{{"Claude Code", claudeState}, {"Codex", codexState}} {
		switch h.state {
		case petHostStale:
			stale = append(stale, h.host)
		case petHostRefreshed:
			refreshed = append(refreshed, h.host)
		}
	}
	switch {
	case len(stale) > 0:
		return WarningCheck(name, "from an older ox ("+strings.Join(stale, ", ")+")", "Run `ox doctor --fix` or `ox pet enable` to refresh it")
	case len(refreshed) > 0:
		msg := "refreshed " + strings.Join(refreshed, " and ") + " to " + want
		var next []string
		if claudeState == petHostRefreshed {
			next = append(next, "run /reload-plugins in open Claude Code sessions")
		}
		if codexState == petHostRefreshed {
			next = append(next, "restart Codex")
		}
		return PassedCheck(name, msg+"; "+strings.Join(next, " and "))
	case claudeState == petHostSkipped && codexState == petHostSkipped:
		return SkippedCheck(name, "not enabled", "")
	default:
		return PassedCheck(name, "up to date")
	}
}

// checkPetClaude refreshes the Claude Code mod. A refresh that stopped
// between its two renames left the live folder gone and the old copy beside
// it; that is put back first, so it never reads as "not enabled".
func checkPetClaude(in pet.Installer, want string, fix bool) (petHostCheck, error) {
	dir := in.Dir
	if err := pet.RecoverSwap(dir); err != nil {
		return 0, err
	}
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return petHostSkipped, nil
	}
	if have, err := pet.WrittenVersion(dir); err == nil && have == want {
		return petHostCurrent, nil
	}
	if !fix {
		return petHostStale, nil
	}
	in.Version = want
	if err := in.WriteMod(); err != nil {
		return 0, err
	}
	return petHostRefreshed, nil
}

// checkPetCodex refreshes Oxy's Codex files only. config.toml is the
// coworker's: doctor never reselects a pet they switched away from.
func checkPetCodex(in pet.CodexInstaller, want string, fix bool) (petHostCheck, error) {
	if in.Home == "" {
		return petHostSkipped, nil
	}
	dir := pet.CodexDir(in.Home)
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		// an interrupted refresh leaves only a complete dir.old, which
		// WriteFiles restores; anything less means never enabled
		if _, err := pet.CodexWrittenVersion(dir + ".old"); err != nil {
			return petHostSkipped, nil
		}
	}
	if have, err := pet.CodexWrittenVersion(dir); err == nil && have == want {
		return petHostCurrent, nil
	}
	if !fix {
		return petHostStale, nil
	}
	in.Version = want
	if err := in.WriteFiles(); err != nil {
		return 0, err
	}
	return petHostRefreshed, nil
}
