package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/store"
)

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugCodeIndex,
		Name:        "Code index",
		Category:    "Code Search",
		FixLevel:    FixLevelAuto,
		Description: "Validates CodeDB index integrity (SQLite + Bleve)",
		Run:         checkCodeIndex,
	})
}

func checkCodeIndex(fix bool) checkResult {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return SkippedCheck("Code index", "not in a project", "")
	}
	return checkCodeIndexAtDir(resolveCodeDBDir(projectRoot), fix)
}

// checkCodeIndexAtDir is the testable core of checkCodeIndex.
// Exported for direct use in tests where the data dir is already known.
func checkCodeIndexAtDir(dataDir string, fix bool) checkResult {

	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		return PassedCheck("Code index", "no index (run 'ox code index' to create)")
	}

	db, err := codedb.Open(dataDir)
	if err != nil {
		// store.Open self-heals MappingCorruptError transparently (nuke +
		// recreate + marker) — so it is rare to reach this branch. Keep it as
		// defense-in-depth in case Open's self-heal itself fails (disk full,
		// EACCES on the bleve dir, panic recovery surfacing a different error).
		var mce *store.MappingCorruptError
		if errors.As(err, &mce) {
			if fix {
				rbErr := store.RebuildBleveSubIndex(dataDir, mce.Name)
				if rbErr == nil {
					return PassedCheck("Code index", fmt.Sprintf("%s sub-index rebuilt; run 'ox code index' to repopulate", mce.Name))
				}
				if errors.Is(rbErr, store.ErrFullReindexRequired) {
					_ = os.RemoveAll(dataDir)
					return PassedCheck("Code index", fmt.Sprintf("%s sub-index needs full reindex; dataDir wiped, run 'ox code index'", mce.Name))
				}
				return FailedCheck("Code index", fmt.Sprintf("rebuild %s sub-index failed: %v", mce.Name, rbErr), "run 'ox code index --full' to rebuild from scratch")
			}
			return FailedCheck("Code index", fmt.Sprintf("%s sub-index is structurally corrupt", mce.Name), "run 'ox doctor --fix' to rebuild only the affected sub-index")
		}
		if fix {
			_ = os.RemoveAll(dataDir)
			return PassedCheck("Code index", "corrupt index removed, run 'ox code index' to rebuild")
		}
		return FailedCheck("Code index", "failed to open index", "run 'ox doctor' to remove and rebuild")
	}
	defer db.Close()

	if err := db.Store().CheckIntegrity(); errors.Is(err, store.ErrCorrupt) {
		if fix {
			db.Close()
			_ = os.RemoveAll(dataDir)
			return PassedCheck("Code index", "corrupt index removed, run 'ox code index' to rebuild")
		}
		return FailedCheck("Code index", "index corruption detected", "run 'ox doctor' to remove and rebuild")
	}

	// Self-heal markers come BEFORE the empty-index check: a freshly-healed
	// store always looks empty (nuke + recreate left no docs in bleve, and the
	// store is opened against the same SQL state as before — but ParseComments
	// flags etc. weren't reset by Open's self-heal). Surfacing "empty index"
	// here would route comment-only corruptions through os.RemoveAll(dataDir),
	// destroying healthy code/diff data that the daemon's next pass could
	// otherwise preserve. The markers branch handles fix surgically (comment)
	// or via wipe (code/diff) based on what RebuildBleveSubIndex can do.
	if healing := store.NeedsReindexMarkers(dataDir); len(healing) > 0 {
		if fix {
			return handleHealMarkersFix(db, dataDir, healing)
		}
		return WarningCheck("Code index",
			fmt.Sprintf("auto-repair in progress for sub-index(es) %s — daemon will rebuild on next pass",
				strings.Join(healing, ", ")),
			"force immediate rebuild: 'ox code index --full'")
	}

	// Detect "empty index": schema exists but no data was ever written.
	// This happens when indexing was interrupted (daemon crash, context cancel)
	// after the DB was created but before any commits were processed.
	var commitCount, repoCount int
	_ = db.Store().QueryRow("SELECT COUNT(*) FROM commits").Scan(&commitCount)
	_ = db.Store().QueryRow("SELECT COUNT(*) FROM repos").Scan(&repoCount)
	if commitCount == 0 && repoCount == 0 {
		if fix {
			db.Close()
			_ = os.RemoveAll(dataDir)
			return PassedCheck("Code index", "empty index removed — run 'ox code index' to rebuild")
		}
		return FailedCheck("Code index", "index exists but is empty (indexing was never completed)", "run 'ox code index' or 'ox doctor --fix' to rebuild")
	}

	return PassedCheck("Code index", "healthy")
}

// handleHealMarkersFix is the --fix branch for self-heal markers. For each
// marker, try a targeted RebuildBleveSubIndex (works for "comment", which can
// be repopulated from SQL alone via the comments_parsed flag). If the rebuild
// reports ErrFullReindexRequired (the contract for "code" and "diff" — they
// can only be repopulated from a full git walk), fall back to a dataDir wipe
// and let the daemon's next pass do the full reindex. Surgical rebuilds clear
// their markers; wipes drop all markers along with the dataDir.
//
// Rationale: a comment-only corruption used to cost ~1GB of code+diff data
// because doctor's only remedy was os.RemoveAll(dataDir). Surgical paths
// preserve healthy peer sub-indexes byte-for-byte.
func handleHealMarkersFix(db *codedb.DB, dataDir string, markers []string) checkResult {
	var rebuilt, wiped []string
	for _, name := range markers {
		rbErr := store.RebuildBleveSubIndex(dataDir, name)
		if rbErr == nil {
			_ = store.ClearNeedsReindexMarker(dataDir, name)
			rebuilt = append(rebuilt, name)
			continue
		}
		if errors.Is(rbErr, store.ErrFullReindexRequired) {
			wiped = append(wiped, name)
			continue
		}
		return FailedCheck("Code index",
			fmt.Sprintf("rebuild %s sub-index failed: %v", name, rbErr),
			"run 'ox code index --full' to rebuild from scratch")
	}

	if len(wiped) > 0 {
		// At least one marker can only be cleared by a full reindex. Wipe
		// dataDir and let the daemon repopulate everything on its next pass.
		db.Close()
		_ = os.RemoveAll(dataDir)
		return PassedCheck("Code index",
			fmt.Sprintf("full reindex needed for %s; dataDir wiped, run 'ox code index' to rebuild now",
				strings.Join(wiped, ", ")))
	}
	return PassedCheck("Code index",
		fmt.Sprintf("surgically rebuilt %s sub-index(es); run 'ox code index' to repopulate",
			strings.Join(rebuilt, ", ")))
}
