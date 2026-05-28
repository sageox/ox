// Package runtime probes the *capabilities* of the environment ox is
// running in — persistent disk, daemon viability, browser availability,
// network reachability, env lifetime — and exposes them as a single
// struct.
//
// Why a capability surface (and not a "mode" flag):
//
//	Every sandbox lands at a different point on five orthogonal axes.
//	The old IsEphemeral() boolean flattened that to one bit, and each
//	subsystem then had to guess what that bit meant for it. With Caps()
//	each subsystem asks the question it actually cares about:
//	the daemon asks DaemonViable, KB asks PersistDisk, ox login asks
//	Browser. New capabilities slot in without inventing new modes.
//
// See plan §10.7 (`~/.claude/plans/system-instruction-you-are-working-
// tranquil-gizmo.md`) for the design discussion, and the senior-principal
// review at `...-agent-a5dc2a726d9515dfc.md` for why this replaces the
// earlier named-profile proposal.
//
// Probing is side-effect-free (no env mutation, no socket bind, no test
// writes) and cached via sync.Once on first call. Subsequent calls return
// the same struct. Tests that need to override individual capabilities
// should construct a Capabilities literal directly rather than mutating
// the cache.
package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sageox/ox/internal/paths"
)

// Lifetime is a coarse hint about how long the current environment (the
// sandbox, the CI job, the laptop session) is expected to live.
// Subsystems use it to amortize work that costs more to set up than it
// saves over a short run (warm TLS pools, codedb index).
//
// The zero value is LifetimePersistent — a default-constructed
// Capabilities{} reads as "laptop." Probe always commits to one of the
// three values below; there is no Unknown. If detection logic can't
// decide, it returns LifetimePersistent and the load-bearing bools
// (PersistDisk, DaemonViable) catch any mismatch.
type Lifetime int

const (
	// LifetimePersistent — developer laptop, Codespaces, long-running server.
	LifetimePersistent Lifetime = iota
	// LifetimeHours — Devin / Claude Code Cloud / multi-hour sandboxes.
	LifetimeHours
	// LifetimeMinutes — most CI jobs, short sandbox tasks.
	LifetimeMinutes
)

// String renders the lifetime bucket for structured logs and doctor
// output. The string values match the historical names so existing log
// grep patterns keep working.
func (l Lifetime) String() string {
	switch l {
	case LifetimeMinutes:
		return "minutes"
	case LifetimeHours:
		return "hours"
	case LifetimePersistent:
		return "persistent"
	}
	return "persistent"
}

// Capabilities is the probed envelope of what the runtime can do.
//
// Each field is independent. Callers ask the specific question they need
// to answer; don't roll multiple fields together with || or && unless
// the composite is the genuine concern.
type Capabilities struct {
	// PersistDisk reports whether ~/.sageox writes survive the next
	// invocation. False on sandboxes whose FS is wiped between commands.
	PersistDisk bool

	// DaemonViable reports whether a background helper process (the
	// daemon) can be started and reach across multiple ox invocations.
	// False when the sandbox dies with the CLI process, when persistent
	// disk is unavailable, or when the operator has set OX_NO_DAEMON.
	DaemonViable bool

	// TmpdirWritable reports whether $TMPDIR (or the default temp dir on
	// this platform) is writable. Used for session staging when
	// PersistDisk is false.
	TmpdirWritable bool

	// Browser reports whether we can open an interactive auth URL. False
	// in sandboxes (no display, no localhost callback) and in
	// non-interactive shells. Subsystems that need a browser flow must
	// fall back to PAT auth when this is false.
	Browser bool

	// Network reports whether outbound HTTPS is expected to reach
	// api.sageox.ai. True on dev laptops, CI runners, sandboxes that
	// allowlist the SageOx control plane. False only when the operator
	// has explicitly declared offline. HTTP_PROXY / HTTPS_PROXY are
	// orthogonal — the HTTP client layer honors them regardless.
	Network bool

	// EnvLifetime is a coarse expected-runtime bucket for the environment
	// the CLI is running in. Drives caching strategy (don't warm caches
	// that won't pay back) and telemetry batching cadence.
	EnvLifetime Lifetime
}

// envOverrides captures the capability-level OX_* env vars that let an
// operator force a single capability off without claiming the whole
// environment is ephemeral. Useful for testing and for sandboxes the
// auto-probe gets wrong.
//
// Semantics:
//   - OX_PERSIST_DISK=0 forces PersistDisk=false.
//   - OX_NO_DAEMON=1 forces DaemonViable=false.
//   - OX_BROWSER=0 forces Browser=false.
//   - OX_NETWORK=0 or OX_OFFLINE=1 forces Network=false.
//
// "Force on" is intentionally not supported — these are downgrades only,
// because granting a capability the runtime didn't actually probe is a
// good way to ship code that races on missing infrastructure.
const (
	envPersistDisk = "OX_PERSIST_DISK"
	envNoDaemon    = "OX_NO_DAEMON"
	envBrowser     = "OX_BROWSER"
	envNetwork     = "OX_NETWORK"
	envOffline     = "OX_OFFLINE"
)

var (
	probeOnce sync.Once
	cached    Capabilities
)

// Caps returns the cached capability probe. The first call probes the
// environment; subsequent calls return the same struct.
//
// Tests that need to swap the cached value should call Reset() in a
// t.Cleanup; never mutate the returned struct.
func Caps() Capabilities {
	probeOnce.Do(func() {
		cached = Probe()
	})
	return cached
}

// Reset clears the cached capability probe. Intended for use in tests.
// Production code must not call this — runtime capabilities don't change
// during a single ox invocation.
func Reset() {
	probeOnce = sync.Once{}
	cached = Capabilities{}
}

// Probe inspects the environment and returns a Capabilities struct. It is
// side-effect-free: no env mutation, no daemon socket bind, no test
// writes to the user's home directory. Callers should prefer Caps() so
// the result is cached process-wide; Probe() is exposed for tests that
// want to bypass the cache.
func Probe() Capabilities {
	c := Capabilities{
		PersistDisk:    probePersistDisk(),
		TmpdirWritable: probeTmpdir(),
		Browser:        probeBrowser(),
		Network:        probeNetwork(),
		EnvLifetime:    probeLifetime(),
	}
	// DaemonViable composes other capabilities, so probe it last.
	c.DaemonViable = probeDaemonViable(c)
	return c
}

// probePersistDisk decides whether on-disk state survives. We treat the
// SageOx data dir as the canonical persistence anchor — if it exists and
// is writable, or if it doesn't yet exist but its parent is writable (so
// first-run create succeeds), persistence is available.
//
// OX_PERSIST_DISK=0 (or any falsy value) forces this off without
// probing, for operators who know better.
func probePersistDisk() bool {
	if v := strings.TrimSpace(os.Getenv(envPersistDisk)); v != "" {
		// explicit downgrade; "1"/"true" do NOT force-on — see envOverrides doc
		switch strings.ToLower(v) {
		case "0", "false", "no", "off":
			return false
		}
	}
	// the legacy/historical signal stays authoritative as a coarse override:
	// any positive Reason() (OX_EPHEMERAL, CLAUDE_CODE_REMOTE, DEVIN_TASK_ID,
	// user-config opt-in) implies non-persistent FS regardless of probe.
	if Reason() != "" {
		return false
	}
	dir := candidateSageoxDir()
	if dir == "" {
		return false
	}
	// permission check: Stat tells us the dir exists; require at least one
	// write bit so a read-only dir doesn't get reported as persistable. We
	// still avoid an actual write to keep Probe side-effect-free; subsystems
	// that can't write here will discover that on their own and fail closed.
	if dirLikelyWritable(dir) {
		return true
	}
	// not yet created — fall back to "is the parent writable"
	parent := filepath.Dir(dir)
	if parent == "" {
		return false
	}
	if dirLikelyWritable(parent) {
		return true
	}
	return false
}

// dirLikelyWritable is a side-effect-free heuristic: the path must exist,
// be a directory, and carry at least one write bit in its mode. A read-only
// directory returns false. False negatives are acceptable — they push us
// toward the conservative non-persistent branch, which is the safer default.
func dirLikelyWritable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	return info.Mode().Perm()&0o222 != 0
}

// candidateSageoxDir returns the directory we'd persist into if asked.
// In legacy mode this is ~/.sageox; in XDG mode SageoxDir() returns ""
// so we probe the config dir instead.
func candidateSageoxDir() string {
	if d := paths.SageoxDir(); d != "" {
		return d
	}
	return paths.ConfigDir()
}

// probeTmpdir returns true when os.TempDir() exists and is writable. The
// check intentionally avoids a real write so Probe stays side-effect-free,
// but requires at least one write bit in the directory mode so a read-only
// TMPDIR doesn't get reported as usable.
func probeTmpdir() bool {
	tmp := os.TempDir()
	if tmp == "" {
		return false
	}
	return dirLikelyWritable(tmp)
}

// probeBrowser returns true when we have a reasonable chance of opening
// an interactive auth URL. The signal set is intentionally conservative —
// false negatives just push users at PAT auth, which is the correct
// fallback in any constrained environment.
func probeBrowser() bool {
	if v := strings.TrimSpace(os.Getenv(envBrowser)); v != "" {
		switch strings.ToLower(v) {
		case "0", "false", "no", "off":
			return false
		}
	}
	// sandboxes can't open browsers; any positive venue/override Reason()
	// is the most reliable proxy we have today.
	if Reason() != "" {
		return false
	}
	// Headless markers — if any of these are set, we assume no browser.
	if os.Getenv("CI") != "" || os.Getenv("GITHUB_ACTIONS") != "" {
		return false
	}
	// We do not check DISPLAY / SSH_CONNECTION here because plenty of
	// valid dev environments (macOS laptop, WSL with browser bridge)
	// don't set them. Letting the auth flow open and fail is a better UX
	// than refusing pre-emptively.
	return true
}

// probeNetwork honors OX_NETWORK=0 / OX_OFFLINE=1 as operator declarations
// of "assume no outbound network." We do not probe the network ourselves
// — a real probe would have to make an outbound call, and Probe is
// side-effect-free by contract. Subsystems that genuinely need to verify
// reachability do their own per-request retry with a tight timeout.
func probeNetwork() bool {
	if v := strings.TrimSpace(strings.ToLower(os.Getenv(envOffline))); v != "" {
		switch v {
		case "1", "true", "yes", "on":
			return false
		}
	}
	if v := strings.TrimSpace(strings.ToLower(os.Getenv(envNetwork))); v != "" {
		switch v {
		case "0", "false", "no", "off", "offline":
			return false
		}
	}
	return true
}

// probeLifetime maps detected platforms to a coarse lifetime bucket. The
// mapping is deliberately blunt — if we're wrong by one bucket the
// caller's behavior degrades from "perfectly tuned" to "fine but slightly
// wasteful", which is the right asymmetry.
func probeLifetime() Lifetime {
	if os.Getenv("DEVIN_TASK_ID") != "" {
		return LifetimeHours
	}
	if os.Getenv("CLAUDE_CODE_REMOTE") != "" {
		return LifetimeHours
	}
	if isCodespaces() {
		return LifetimePersistent
	}
	if os.Getenv("CI") != "" || os.Getenv("GITHUB_ACTIONS") != "" {
		return LifetimeMinutes
	}
	if v := os.Getenv("OX_EPHEMERAL"); v != "" {
		// OX_EPHEMERAL=1 with no other marker means "treat this like a
		// short CI-shaped runner" by default.
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return LifetimeMinutes
		}
	}
	// default assumption: a dev shell or service that will run for a while
	return LifetimePersistent
}

// probeDaemonViable composes the other capability fields plus the
// OX_NO_DAEMON kill switch. The daemon is viable when:
//
//   - the operator has not explicitly disabled it via OX_NO_DAEMON=1;
//   - we have persistent disk to anchor sockets and PID files (a daemon
//     started in a sandbox that wipes FS on exit is just CLI-process
//     work dressed up in IPC overhead — fix the underlying bug, don't
//     ship the overhead);
//   - the environment is expected to outlive a single CLI invocation
//     (sub-minute / minutes-shaped runs pay for daemon startup and never
//     reuse it).
func probeDaemonViable(c Capabilities) bool {
	if v := strings.TrimSpace(os.Getenv(envNoDaemon)); v != "" {
		switch strings.ToLower(v) {
		case "1", "true", "yes", "on":
			return false
		}
	}
	if !c.PersistDisk {
		return false
	}
	if c.EnvLifetime == LifetimeMinutes {
		return false
	}
	return true
}
