package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/paths"
)

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugCredentialIntegrity,
		Name:        "credential file integrity",
		Category:    "Authentication",
		FixLevel:    FixLevelAuto,
		Description: "Validates credential files are valid JSON",
		Run: func(fix bool) checkResult {
			return checkCredentialIntegrity(fix)
		},
	})
}

// checkCredentialIntegrity validates that credential files contain valid JSON.
// Corrupt files are auto-fixed by removal — the user will re-auth on next use.
func checkCredentialIntegrity(fix bool) checkResult {
	return checkCredentialIntegrityInDir(paths.ConfigDir(), fix)
}

// checkCredentialIntegrityInDir is the testable core of the integrity check.
func checkCredentialIntegrityInDir(configDir string, fix bool) checkResult {
	const name = "credential file integrity"

	if configDir == "" {
		return SkippedCheck(name, "config dir unknown", "")
	}

	filesToCheck := []string{
		filepath.Join(configDir, "auth.json"),
		filepath.Join(configDir, "git-credentials.json"),
	}

	// per-endpoint git credential files
	matches, _ := filepath.Glob(filepath.Join(configDir, "git-credentials-*.json"))
	filesToCheck = append(filesToCheck, matches...)

	var corrupt []string
	var checked int

	for _, path := range filesToCheck {
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			slog.Debug("credential integrity: read error", "path", path, "error", err)
			continue
		}

		checked++
		if !json.Valid(data) {
			corrupt = append(corrupt, path)
		}
	}

	if checked == 0 {
		return SkippedCheck(name, "no credential files found", "")
	}

	if len(corrupt) == 0 {
		return PassedCheck(name, fmt.Sprintf("%d file(s) valid", checked))
	}

	if fix {
		return fixCorruptCredentialFiles(corrupt)
	}

	names := make([]string, len(corrupt))
	for i, p := range corrupt {
		names[i] = filepath.Base(p)
	}
	return WarningCheck(name,
		fmt.Sprintf("%d corrupt file(s)", len(corrupt)),
		fmt.Sprintf("invalid JSON in: %s\nrun `ox doctor --fix` to remove (you will need to re-login)",
			strings.Join(names, ", ")))
}

// fixCorruptCredentialFiles removes corrupt credential files so the user can re-auth cleanly.
func fixCorruptCredentialFiles(corrupt []string) checkResult {
	const name = "credential file integrity"

	var removed, failed int
	for _, path := range corrupt {
		if err := os.Remove(path); err != nil {
			slog.Error("credential integrity: remove failed", "path", path, "error", err)
			failed++
			continue
		}
		slog.Info("credential integrity: removed corrupt file", "path", path)
		removed++
	}

	if failed > 0 {
		return WarningCheck(name,
			fmt.Sprintf("removed %d, failed %d", removed, failed),
			"some corrupt files could not be removed")
	}

	return PassedCheck(name,
		fmt.Sprintf("removed %d corrupt file(s), run `ox login` to re-authenticate", removed))
}
