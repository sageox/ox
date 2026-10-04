package ledger

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// backfillLogRecorder captures every slog record, at every level.
type backfillLogRecorder struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *backfillLogRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (h *backfillLogRecorder) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}
func (h *backfillLogRecorder) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *backfillLogRecorder) WithGroup(string) slog.Handler      { return h }

// attrInt returns the int attribute key of the first record matching level and
// message, and whether such a record exists.
func (h *backfillLogRecorder) attrInt(level slog.Level, msg, key string) (int64, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Level != level || r.Message != msg {
			continue
		}
		var val int64
		found := false
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == key {
				val, found = a.Value.Int64(), true
				return false
			}
			return true
		})
		return val, found
	}
	return 0, false
}

// count returns how many records match the level and message.
func (h *backfillLogRecorder) count(level slog.Level, msg string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, r := range h.records {
		if r.Level == level && r.Message == msg {
			n++
		}
	}
	return n
}

// TestBackfillPRCommits_MissingFilesDoNotWarn pins the log level of the
// PR-backfill read failure.
//
// Failure prevented: a PR file listed by the directory walk but absent on read
// (outside the ledger's sparse checkout, or removed by a concurrent checkout)
// is an expected condition, yet it logged a WARN per file — 925 lines in three
// minutes after one daemon restart — drowning the warnings that matter. Real
// read failures (permissions, I/O) must stay WARN.
func TestBackfillPRCommits_MissingFilesDoNotWarn(t *testing.T) {
	const (
		warnMsg  = "read PR file for backfill failed"
		debugMsg = "PR file for backfill not on disk"
	)

	tests := []struct {
		name      string
		prepare   func(t *testing.T, prDir string)
		wantWarn  int
		wantDebug int
		skip      func() string
	}{
		{
			name: "dangling entries are expected: debug, never warn",
			prepare: func(t *testing.T, prDir string) {
				for _, name := range []string{"9001.json", "9002.json", "9003.json"} {
					symlinkOrSkip(t, filepath.Join(prDir, "gone-"+name), filepath.Join(prDir, name))
				}
			},
			wantDebug: 3, // one per missing file; the scan pass drops them

		},
		{
			name: "an unreadable file is a real failure and still warns",
			prepare: func(t *testing.T, prDir string) {
				path := filepath.Join(prDir, "9100.json")
				if err := os.WriteFile(path, []byte("{}"), 0o000); err != nil {
					t.Fatalf("write unreadable file: %v", err)
				}
			},
			wantWarn: 1,
			skip: func() string {
				if runtime.GOOS == "windows" {
					return "file modes are not enforced on windows"
				}
				if os.Geteuid() == 0 {
					return "root can read mode 000 files"
				}
				return ""
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.skip != nil {
				if reason := tt.skip(); reason != "" {
					t.Skip(reason)
				}
			}

			ledgerPath := t.TempDir()
			now := time.Now().UTC().Truncate(time.Second)

			// a real merged PR without commits, so the pass has genuine work
			// and we can see it still completes around the bad entries
			pr := &PRFile{Number: 800, Title: "Merged", State: "merged", Author: "alice", CreatedAt: now, UpdatedAt: now, MergedAt: &now, MergeCommit: "abc800"}
			if err := WriteGitHubPR(ledgerPath, pr); err != nil {
				t.Fatalf("write PR: %v", err)
			}
			files, err := ListGitHubDataFiles(ledgerPath, "pr")
			if err != nil || len(files) != 1 {
				t.Fatalf("list PR files: %v (got %d)", err, len(files))
			}
			tt.prepare(t, filepath.Dir(files[0]))

			recorder := &backfillLogRecorder{}
			fetcher := &mockFetcher{prCommits: map[int][]FetchedPRCommit{
				800: {{SHA: "eee800", Author: "alice", Date: now, Msg: "commit"}},
			}}

			backfilled, err := BackfillPRCommits(context.Background(), fetcher, ledgerPath, "org", "repo", slog.New(recorder))
			if err != nil {
				t.Fatalf("BackfillPRCommits: %v", err)
			}
			if backfilled != 1 {
				t.Errorf("the readable PR must still be backfilled around bad entries: got %d, want 1", backfilled)
			}

			if got := recorder.count(slog.LevelWarn, warnMsg); got != tt.wantWarn {
				t.Errorf("WARN %q count = %d, want %d", warnMsg, got, tt.wantWarn)
			}
			if got := recorder.count(slog.LevelDebug, debugMsg); got != tt.wantDebug {
				t.Errorf("DEBUG %q count = %d, want %d", debugMsg, got, tt.wantDebug)
			}
			if tt.wantDebug > 0 {
				if got := recorder.count(slog.LevelDebug, "PR backfill skipped files that are not on disk"); got != 1 {
					t.Errorf("per-pass summary count = %d, want exactly 1", got)
				}
				const summaryMsg = "PR backfill skipped files that are not on disk"
				if skipped, ok := recorder.attrInt(slog.LevelDebug, summaryMsg, "skipped"); !ok || skipped != int64(tt.wantDebug) {
					t.Errorf("per-pass summary skipped = %d (present=%v), want %d", skipped, ok, tt.wantDebug)
				}
			}
		})
	}
}

// symlinkOrSkip creates a symlink whose target does not exist, skipping the
// test on platforms where an unprivileged process cannot create symlinks.
func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
}
