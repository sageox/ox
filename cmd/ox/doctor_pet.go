package main

import (
	"errors"
	"io/fs"
	"os"

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
		Description: "Rewrites the Oxy mod for Claude Code when an ox upgrade left it from an older version",
		Run:         func(fix bool) checkResult { return checkPetOxy(newPetInstaller(), version.Version, fix) },
	})
}

// checkPetOxy keeps an enabled Oxy mod on this ox's version. Claude Code reads
// the mod folder in place, so rewriting it is the whole refresh: no claude CLI
// call, and nothing outside ox's own ox-cli-* folder is touched. A machine that
// never ran `ox pet enable` is skipped, so the pet stays opt-in.
//
// A refresh that stopped between its two renames left the live folder gone
// and the old copy beside it; that is put back first, so it never reads as
// "not enabled".
func checkPetOxy(in pet.Installer, want string, fix bool) checkResult {
	const name = "Oxy pet"
	dir := in.Dir
	if err := pet.RecoverSwap(dir); err != nil {
		return FailedCheck(name, "interrupted refresh", err.Error())
	}
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return SkippedCheck(name, "not enabled", "")
	}
	have, err := pet.WrittenVersion(dir)
	if err == nil && have == want {
		return PassedCheck(name, "up to date")
	}
	if !fix {
		return WarningCheck(name, "from an older ox", "Run `ox doctor --fix` or `ox pet enable` to refresh it")
	}
	in.Version = want
	if err := in.WriteMod(); err != nil {
		return FailedCheck(name, "refresh failed", err.Error())
	}
	return PassedCheck(name, "refreshed to "+want+"; run /reload-plugins in open Claude Code sessions")
}
