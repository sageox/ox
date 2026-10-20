package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/sageox/ox/internal/fileutil"
)

// CheckSlugSessionAbortRecord repairs the record of aborted sessions.
const CheckSlugSessionAbortRecord = "session-abort-record"

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugSessionAbortRecord,
		Name:        "aborted sessions record",
		Category:    "Sessions",
		FixLevel:    FixLevelAuto,
		Description: "Repairs an unreadable record of aborted sessions, keeping every session it still names",
		Run:         func(fix bool) checkResult { return checkAbortRecord(fix) },
	})
}

// checkAbortRecord finds a record of aborted sessions that cannot be read.
// Such a record stops `ox session import` and prime's auto-recording, because
// reading it as empty would bring back sessions the coworker discarded. The
// repair keeps every session ID the damaged bytes still hold and sets the
// original aside, so it never forgets an abort.
func checkAbortRecord(fix bool) checkResult {
	const name = "aborted sessions record"
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck(name, "not in a git repository", "")
	}
	var damaged []string
	for _, path := range abortRecordPaths(getLedgerPath(), getRepoIDOrDefault(gitRoot)) {
		if _, err := loadAbortRecordFile(path); err != nil {
			damaged = append(damaged, path)
		}
	}
	if len(damaged) == 0 {
		return PassedCheck(name, "readable")
	}
	if !fix {
		return FailedCheck(name, "cannot be read; session import and auto-recording are stopped",
			"run `ox doctor --fix` to repair it; the repair keeps every aborted session it names")
	}
	var notes []string
	for _, path := range damaged {
		kept, aside, err := repairAbortRecord(path, time.Now())
		if err != nil {
			return FailedCheck(name, "could not repair "+path, err.Error())
		}
		notes = append(notes, fmt.Sprintf("kept %d aborted session ID%s; the damaged record is at %s", kept, plural(kept), aside))
	}
	return PassedCheck(name, "repaired: "+strings.Join(notes, "; "))
}

// abortRecordIDPattern finds native session IDs and ses_ IDs in damaged bytes.
var abortRecordIDPattern = regexp.MustCompile(`(ses_)?[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// repairAbortRecord replaces a damaged record with one holding every ID it
// still contains, dated when the record last changed. It moves the damaged
// record aside rather than deleting it, and refuses when it cannot read it,
// since a rewrite would then drop the IDs inside.
func repairAbortRecord(path string, now time.Time) (int, string, error) {
	kept, aside := 0, path+".damaged-"+now.UTC().Format("20060102T150405Z")
	err := fileutil.WithFileLock(context.Background(), path, func() error {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		content, err := readDamagedAbortRecord(path, info)
		if err != nil {
			return fmt.Errorf("cannot read it to keep the sessions it names: %w", err)
		}
		record := newAbortedSessions()
		at := info.ModTime().UTC()
		for _, match := range abortRecordIDPattern.FindAllString(string(content), -1) {
			if id, isRecording := strings.CutPrefix(match, "ses_"); isRecording {
				record.Recordings["ses_"+id] = abortedSession{AbortedAt: at}
			} else {
				record.Sessions[match] = abortedSession{AbortedAt: at}
			}
		}
		kept = len(record.Sessions) + len(record.Recordings)
		if err := os.Rename(path, aside); err != nil {
			return err
		}
		return fileutil.AtomicWriteJSON(path, record, 0o600)
	})
	return kept, aside, err
}

// readDamagedAbortRecord reads a damaged record's bytes; a directory where
// the file belongs is read through its regular files, scoped to it so a
// symlink inside cannot lead the read elsewhere.
func readDamagedAbortRecord(path string, info os.FileInfo) ([]byte, error) {
	if !info.IsDir() {
		return os.ReadFile(path)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var content []byte
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		data, err := root.ReadFile(e.Name())
		if err != nil {
			return nil, err
		}
		content = append(append(content, data...), '\n')
	}
	return content, nil
}
