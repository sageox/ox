// Package updatenotice keeps ox's "you should upgrade" surfaces calm.
//
// ox consults a version cache and talks to the API on nearly every command, so
// without a persisted memory both the server's deprecation header and the
// GitHub-derived "update available" line reprint on every single invocation,
// forever. A sync.Once cannot help: for a CLI, one process is one command, so
// process-scoped dedup dedupes nothing. This package owns the cross-process
// memory instead — a two-field ledger stored alongside the existing version
// cache — plus the one question every notice site asks: may we speak now?
//
// One ledger serves BOTH notice tiers deliberately. The server's deprecation
// warning and the calm "update available" line are the same fact wearing two
// hats; a coworker who just read one should not immediately read the other.
package updatenotice

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/sageox/ox/internal/paths"
)

// NotifyInterval caps how often a coworker hears about the same release line.
//
// 24h, tuned so the reminder lands at most once per working day: short enough
// that an available upgrade stays top of mind, long enough that someone running
// forty ox commands in an afternoon hears about it once rather than forty
// times. Shortening it reintroduces exactly the alarm fatigue this ledger
// exists to kill; lengthening it past a day lets a coworker work a full day
// without ever learning a release shipped.
const NotifyInterval = 24 * time.Hour

// Data is the on-disk version cache. The first three fields are the GitHub
// release check (written by the daemon, by `ox doctor`, and by `ox status`);
// the last two are the notice ledger.
//
// Every writer of this file MUST round-trip the fields it does not own. A
// writer that rebuilds this struct from a fresh GitHub check and saves it drops
// the ledger, which silently restores the every-invocation nag — see
// CarryLedger.
type Data struct {
	LatestVersion string    `json:"latest_version"`
	CheckedAt     time.Time `json:"checked_at"`
	ETag          string    `json:"etag,omitempty"`

	// LastNaggedLine is the X.Y release line we last spoke about, e.g. "0.16".
	// Keyed on the line rather than the exact version so a patch release does
	// not reset the cap, while a genuinely new release line does.
	LastNaggedLine string `json:"last_nagged_line,omitempty"`
	// LastNaggedAt is when we last spoke about LastNaggedLine (RFC 3339).
	LastNaggedAt time.Time `json:"last_nagged_at,omitzero"`
}

// Path is the version cache file. A var so tests redirect it to a temp dir
// rather than writing to the real ~/.cache/sageox/.
var Path = filepath.Join(paths.CacheDir(), "version-check.json")

// Read returns the cache, or nil when it is missing or unreadable. A corrupt
// cache is treated as absent: printing one extra notice is cheaper than
// failing a command over a cache file.
func Read() *Data {
	raw, err := os.ReadFile(Path)
	if err != nil {
		return nil
	}
	var d Data
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil
	}
	return &d
}

// Write persists d atomically (temp file + rename, 0600).
func Write(d *Data) error {
	if d == nil {
		return nil
	}
	raw, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(Path), 0700); err != nil {
		return err
	}
	tmp := Path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, Path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Reset drops the whole cache, ledger included. Called after a successful
// `ox upgrade`: the running process still reports its OLD compiled-in version,
// so a surviving cache would keep claiming an update is available — and a
// surviving ledger would then suppress the FIRST notice about the next release
// line, which is the one notice that matters most.
func Reset() {
	_ = os.Remove(Path)
}

// CarryLedger copies the notice ledger from src into dst, leaving dst's version
// fields alone. Every writer of the cache file must call this before saving a
// freshly built Data, or a routine version refresh erases the memory.
func CarryLedger(dst, src *Data) {
	if dst == nil || src == nil {
		return
	}
	dst.LastNaggedLine = src.LastNaggedLine
	dst.LastNaggedAt = src.LastNaggedAt
}

// ReleaseLine reduces a version to its X.Y release line: "0.16.3" and
// "v0.16.0-rc1+abc123" both yield "0.16".
//
// Pre-release and build metadata are stripped BEFORE the split so a local
// `0.14.3-dev+<sha>` build does not read as a brand-new line on every rebuild
// and reset the cap each time. Returns "" for anything unparseable, which
// callers must treat as "stay quiet" rather than "always speak" — an unknown
// line has no ledger key, so speaking would be uncappable.
func ReleaseLine(v string) string {
	v = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "v"))
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return ""
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return ""
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return ""
	}
	return strconv.Itoa(major) + "." + strconv.Itoa(minor)
}

// Line is the release line a notice is keyed on: the newest line we know exists
// upstream, falling back to the running binary's own line before any cache has
// been written. Both tiers key on the same value so one ledger caps both.
func Line(currentVersion string) string {
	if d := Read(); d != nil {
		if line := ReleaseLine(d.LatestVersion); line != "" {
			return line
		}
	}
	return ReleaseLine(currentVersion)
}

// ShouldNotify reports whether a notice about line may be printed at now.
//
// First sight of a release line always speaks; after that the ledger caps it to
// once per NotifyInterval. d may be nil (no cache yet).
func ShouldNotify(d *Data, line string, now time.Time) bool {
	if line == "" {
		return false
	}
	if d == nil || d.LastNaggedLine != line {
		return true
	}
	// A stamp in the future means the clock moved backwards (VM restore, NTP
	// correction). Treat it as never-stamped rather than muting the notice
	// until real time catches up, which could be years.
	if d.LastNaggedAt.After(now) {
		return true
	}
	return now.Sub(d.LastNaggedAt) >= NotifyInterval
}

// RecordNotified stamps the ledger for line. Best-effort: a cache we cannot
// write means we speak again next time, which is the safe direction to fail.
func RecordNotified(line string, now time.Time) {
	if line == "" {
		return
	}
	d := Read()
	if d == nil {
		d = &Data{}
	}
	d.LastNaggedLine = line
	d.LastNaggedAt = now
	_ = Write(d)
}

// machineOutput records whether this invocation emits machine-readable output.
// Atomic because the daemon shares this package with concurrent request paths.
var machineOutput atomic.Bool

// SetMachineOutput records whether this invocation is emitting machine-readable
// output (--json / --text). Called once from the CLI bootstrap.
func SetMachineOutput(enabled bool) { machineOutput.Store(enabled) }

// StderrIsTTY reports whether stderr is a terminal. A func var so tests can
// simulate both a human at a terminal and an agent capturing stderr through a
// pipe, without allocating a pty.
var StderrIsTTY = func() bool {
	return isatty.IsTerminal(os.Stderr.Fd()) || isatty.IsCygwinTerminal(os.Stderr.Fd())
}

// Suppressed reports whether update notices must stay silent.
//
// The non-TTY rule is the load-bearing one: an ox invocation inside a coding
// agent has stdout and stderr captured into the agent's transcript, where an
// upgrade nag burns context tokens on every command and can pull the agent off
// the task it was actually given. Machine-output modes are suppressed for that
// reason plus a harder one — a stray notice line in --json output is a parse
// error for whatever is reading it.
//
// Structured FIELDS are not notices. `ox agent prime` and `ox status --json`
// still carry update availability as data, because the caller asked for it.
func Suppressed() bool {
	return machineOutput.Load() || !StderrIsTTY()
}
