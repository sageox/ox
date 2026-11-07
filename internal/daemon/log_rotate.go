package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sageox/ox/internal/fileutil"
)

// maxDaemonLogBytes is the size above which the daemon log is rotated when the
// next daemon starts. One week of an unthrottled daemon produced a 96 MB log.
const maxDaemonLogBytes = 10 << 20

// daemonLogRetention is how long a daemon log can go without a write before
// pruneDaemonLogs deletes it.
const daemonLogRetention = 30 * 24 * time.Hour

// maxDaemonLogDirBytes caps the daemon logs kept in one directory, so the logs
// a daemon stuck in a loop leaves behind across restarts do not pile up for
// daemonLogRetention. It is not a hard bound: pruneDaemonLogs never deletes a
// current log or a workspace's most recently rotated log to meet it. In
// October 2026 a heavily used development machine wrote about 6.5 MB a day
// across its 8 daemons, about 200 MB in 30 days; 1 GiB keeps 30 days of that
// plus a daemon logging 96 MB a week, the worst runaway seen.
const maxDaemonLogDirBytes = 1 << 30

// OpenLog opens the log a daemon about to be spawned writes its stdout and
// stderr to, after rotating it and deleting expired logs. When the persistent
// log cannot be opened, it opens the /tmp one daemons wrote before logs became
// persistent, so a home directory ox cannot write to does not stop the daemon
// from starting. The caller passes the file to the child and closes its copy.
func OpenLog() (*os.File, error) {
	persistent, temp := logPaths()
	f, err := openLogFile(persistent)
	if err == nil {
		return f, nil
	}
	slog.Warn("cannot open the daemon log, falling back to the temporary directory", "path", persistent, "fallback", temp, "error", err)
	return openLogFile(temp)
}

func openLogFile(path string) (*os.File, error) {
	// with no home directory the persistent path is relative, and would land
	// in the spawning command's working directory, usually the user's repo
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("log path %q is not absolute", path)
	}
	// logs may contain sensitive information, and the /tmp fallback is a
	// predictable path another local user could prepare first
	dir := filepath.Dir(path)
	if err := fileutil.EnsurePrivateDir(dir); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	// Daemons of every workspace share dir, and an unlocked rotation can rename
	// a log another start has just created onto the generation it just made.
	// rotateAndOpen always returns nil, so a lock error means it never ran.
	var f *os.File
	var openErr error
	rotateAndOpen := func() error {
		now := time.Now()
		rotateDaemonLog(path, now)
		pruneDaemonLogs(dir, now)
		f, openErr = fileutil.OpenPrivateLog(path)
		return nil
	}
	lockErr := fileutil.WithFileLockTimeout(context.Background(), dir, time.Second, rotateAndOpen)
	var busy *fileutil.ErrLockTimeout
	switch {
	case errors.As(lockErr, &busy):
		// the start holding the lock prunes dir; this log rotates at its next start
		f, openErr = fileutil.OpenPrivateLog(path)
	case lockErr != nil:
		// the lock file cannot be opened, say because another user owns the
		// lock directory, and every later start would fail the same way
		_ = rotateAndOpen()
	}
	if openErr != nil {
		return nil, fmt.Errorf("failed to open log file: %w", openErr)
	}
	return f, nil
}

// rotateDaemonLog renames the daemon log at path to path.<UTC time> when it
// exceeds maxDaemonLogBytes. Call it before the log is opened with O_APPEND:
// the new daemon's stdout/stderr then starts a fresh file, and earlier
// generations stay until pruneDaemonLogs deletes them.
//
// Best effort by design — a log that cannot be rotated (permissions, a Windows
// sharing violation from a lingering writer) must never block daemon startup,
// so failures are logged at debug level and reported only as a false return.
// Returns true when the log was rotated.
func rotateDaemonLog(path string, now time.Time) bool {
	info, err := os.Stat(path)
	if err != nil || info.Size() <= maxDaemonLogBytes {
		return false
	}
	rotated := path + "." + now.UTC().Format("20060102T150405Z")
	if err := os.Rename(path, rotated); err != nil {
		slog.Debug("daemon log rotation failed", "path", path, "size", info.Size(), "error", err)
		return false
	}
	slog.Debug("rotated daemon log", "path", path, "rotated_to", rotated, "size", info.Size())
	return true
}

// pruneDaemonLogs deletes the daemon logs in dir last written more than
// daemonLogRetention ago. Then, while all daemon logs in dir exceed
// maxDaemonLogDirBytes, it deletes the earliest rotated log of the workspace
// holding the most bytes, and stops when that workspace has none it may
// delete, so one runaway daemon cannot evict the others' history. Two kinds
// count toward the cap but are never deleted for it:
//   - current logs: a running daemon may be writing one, and deleting a file a
//     process holds open frees no space on Unix and hides its output;
//   - each workspace's most recently rotated log, which the rotation just
//     before this call may have produced: a runaway daemon's log must survive
//     the restart that investigates it.
//
// Rotation order comes from the UTC time in the names, not the mtime, which
// rename keeps: a daemon that slept out a restart-loop throttle while its log
// was rotated can write an earlier generation after a later one. The cap can
// delete such a generation, hiding what that daemon writes next; on Unix its
// space is freed when the daemon exits.
//
// A current log expires too once nothing has written it for 30 days. Daemons
// log as they start and exit after an hour without activity
// (Config.InactivityTimeout), so a daemon still holding such a log would have
// run for 30 days without writing a line; on Unix it would then write into the
// deleted file until it restarted.
func pruneDaemonLogs(dir string, now time.Time) {
	// best effort, like rotation: a listing error defers pruning to the next start
	entries, _ := os.ReadDir(dir)
	rotated := map[string][]os.FileInfo{} // by current log name
	familyBytes := map[string]int64{}
	var total int64
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "daemon_") || !strings.Contains(name, ".log") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if now.Sub(info.ModTime()) > daemonLogRetention {
			_ = os.Remove(filepath.Join(dir, name))
			continue
		}
		total += info.Size()
		familyBytes[currentLogName(name)] += info.Size()
		if !strings.HasSuffix(name, ".log") {
			rotated[currentLogName(name)] = append(rotated[currentLogName(name)], info)
		}
	}
	for _, logs := range rotated {
		slices.SortFunc(logs, func(a, b os.FileInfo) int { return strings.Compare(a.Name(), b.Name()) })
	}
	for total > maxDaemonLogDirBytes {
		victim := ""
		for family := range familyBytes {
			if victim == "" || familyBytes[family] > familyBytes[victim] {
				victim = family
			}
		}
		// the last, most recently rotated, log is never a candidate
		if len(rotated[victim]) < 2 {
			return
		}
		oldest := rotated[victim][0]
		rotated[victim] = rotated[victim][1:]
		if os.Remove(filepath.Join(dir, oldest.Name())) == nil {
			total -= oldest.Size()
			familyBytes[victim] -= oldest.Size()
		}
	}
}

// currentLogName returns the name of the current log a daemon log belongs to:
// daemon_<repo>_<ws>.log for itself and for daemon_<repo>_<ws>.log.<UTC time>.
func currentLogName(name string) string {
	return name[:strings.LastIndex(name, ".log")+len(".log")]
}
