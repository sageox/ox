package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/paths"

	_ "modernc.org/sqlite"
)

// CheckSlugWhisperDB is the slug for the whisper DB integrity check.
const CheckSlugWhisperDB = "whisper-db"

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugWhisperDB,
		Name:        "whisper DB integrity",
		Category:    "Local State",
		FixLevel:    FixLevelAuto,
		Description: "Validates whisper SQLite databases are not corrupt",
		Run: func(fix bool) checkResult {
			return checkWhisperDBIntegrity(fix)
		},
	})
}

// checkWhisperDBIntegrity validates whisper DB files for corruption.
// Corrupt DBs are auto-fixed by deletion — the daemon recreates on next start.
func checkWhisperDBIntegrity(fix bool) checkResult {
	const name = "whisper DB integrity"

	gitRoot := findGitRoot()
	if gitRoot == "" || !config.IsInitialized(gitRoot) {
		return SkippedCheck(name, "project not initialized", "")
	}

	projectCfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil || projectCfg == nil || projectCfg.RepoID == "" {
		return SkippedCheck(name, "no project config", "")
	}

	ep := endpoint.GetForProject(gitRoot)
	if ep == "" {
		return SkippedCheck(name, "no endpoint configured", "")
	}

	type dbTarget struct {
		label string
		dir   string
	}

	var targets []dbTarget

	// ledger whisper DB
	ledgerDir := paths.WhisperDBDir(projectCfg.RepoID, ep)
	if ledgerDir != "" {
		targets = append(targets, dbTarget{label: "ledger", dir: ledgerDir})
	}

	// team whisper DB(s)
	localCfg, _ := config.LoadLocalConfig(gitRoot)
	if localCfg != nil {
		for _, tc := range localCfg.TeamContexts {
			if tc.TeamID == "" {
				continue
			}
			teamDir := paths.TeamWhisperDBDir(tc.TeamID, ep)
			if teamDir != "" {
				targets = append(targets, dbTarget{label: fmt.Sprintf("team/%s", tc.TeamID), dir: teamDir})
			}
		}
	}

	// also check the project-level team if not already covered by local config
	if projectCfg.TeamID != "" {
		teamDir := paths.TeamWhisperDBDir(projectCfg.TeamID, ep)
		if teamDir != "" {
			alreadyCovered := false
			for _, t := range targets {
				if t.dir == teamDir {
					alreadyCovered = true
					break
				}
			}
			if !alreadyCovered {
				targets = append(targets, dbTarget{label: fmt.Sprintf("team/%s", projectCfg.TeamID), dir: teamDir})
			}
		}
	}

	if len(targets) == 0 {
		return SkippedCheck(name, "no whisper DB paths resolved", "")
	}

	var checked, healthy, missing int
	var corruptLabels []string
	var fixedLabels []string
	var fixErrors []string

	for _, t := range targets {
		dbPath := filepath.Join(t.dir, "whisper.db")

		if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
			missing++
			continue
		}

		checked++

		corrupted := isWhisperDBCorrupt(dbPath)
		if !corrupted {
			healthy++
			continue
		}

		if fix {
			removeWhisperSQLiteFiles(dbPath)
			// verify removal
			if _, statErr := os.Stat(dbPath); errors.Is(statErr, os.ErrNotExist) {
				fixedLabels = append(fixedLabels, t.label)
				slog.Info("whisper DB auto-fixed: removed corrupt DB", "label", t.label, "path", dbPath)
			} else {
				fixErrors = append(fixErrors, t.label)
				slog.Error("whisper DB auto-fix failed: could not remove", "label", t.label, "path", dbPath)
			}
		} else {
			corruptLabels = append(corruptLabels, t.label)
		}
	}

	// all DBs missing — if daemon is running this likely means the GC reclone
	// destroyed the directory and the daemon holds a stale handle
	if checked == 0 && missing > 0 && fix && daemon.IsRunning() {
		for _, t := range targets {
			if err := os.MkdirAll(t.dir, 0o700); err != nil {
				slog.Warn("failed to create whisper DB dir", "dir", t.dir, "error", err)
			}
		}
		return PassedCheck(name, "recreated whisper DB directory (daemon will rebuild)")
	}
	if checked == 0 {
		return PassedCheck(name, "no whisper DBs present (created on demand)")
	}

	// report fix results
	if len(fixedLabels) > 0 && len(fixErrors) == 0 {
		return PassedCheck(name,
			fmt.Sprintf("auto-fixed: removed %d corrupt DB(s) (%s), daemon will recreate",
				len(fixedLabels), strings.Join(fixedLabels, ", ")))
	}
	if len(fixErrors) > 0 {
		return WarningCheck(name,
			fmt.Sprintf("fixed %d, failed %d", len(fixedLabels), len(fixErrors)),
			fmt.Sprintf("could not remove: %s", strings.Join(fixErrors, ", ")))
	}

	// report corruption without fix
	if len(corruptLabels) > 0 {
		return FailedCheck(name,
			fmt.Sprintf("%d corrupt DB(s): %s", len(corruptLabels), strings.Join(corruptLabels, ", ")),
			"corrupt whisper DBs will be auto-fixed on next run").
			WithFixInfo(CheckSlugWhisperDB, FixLevelAuto)
	}

	return PassedCheck(name, fmt.Sprintf("%d DB(s) healthy", healthy))
}

// isWhisperDBCorrupt opens a raw sqlite connection and runs PRAGMA integrity_check.
func isWhisperDBCorrupt(dbPath string) bool {
	dsn := dbPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(3000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		slog.Debug("whisper DB open failed during integrity check", "path", dbPath, "error", err)
		return true
	}
	defer db.Close()

	var result string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		slog.Debug("whisper DB integrity_check query failed", "path", dbPath, "error", err)
		return true
	}

	return result != "ok"
}

// removeWhisperSQLiteFiles removes the .db, .db-wal, and .db-shm files.
// Mirrors the pattern from internal/whisper/store/store.go:removeSQLiteFiles.
func removeWhisperSQLiteFiles(dbPath string) {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		p := dbPath + suffix
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("failed to remove whisper db file", "path", p, "error", err)
		}
	}
}
