package daemon

// Cross-process leader election for global-sync work (team-context pulls
// and the KB ListBubbles API call). Both operations touch shared XDG state
// that is the same for every project the user has open, so when N
// per-repo daemons run concurrently they would otherwise each hit the
// same APIs every 15s — wasted bandwidth and rate-limit risk.
//
// We use a per-(user, endpoint) advisory POSIX flock(2) on a single lease
// file. Exactly one daemon per endpoint can hold LOCK_EX|LOCK_NB at any
// time; the rest skip the global-sync ticker calls and keep doing their
// per-repo work (ledger pull, code index, sessions, github sync,
// whispers, heartbeats — all unchanged).
//
// The lease is acquired once at daemon startup and held for the daemon's
// lifetime. On process death the kernel auto-releases the lock, so the
// next daemon's next acquire attempt picks up the lease. No explicit
// hand-off, no heartbeat, no RPC between daemons.
//
// Per `.claude/rules/lfs-no-git-lfs-binary.md` and the existing kb_lock.go
// design, we use stdlib `syscall.Flock` rather than a third-party lock
// library, keeping the dependency surface minimal.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/paths"
)

// ErrNotOwner is returned by AcquireGlobalSyncLease when another daemon
// process already holds the lease for the given endpoint. The caller
// should set its in-memory lease handle to nil and skip global-sync
// tickers for the daemon's lifetime; the next daemon-level retry happens
// implicitly the next time a new daemon process starts up.
var ErrNotOwner = errors.New("another daemon owns the global-sync lease for this endpoint")

// globalLeaseDirName is the subdirectory under DaemonStateDir() that
// holds per-endpoint lease files. Kept distinct from kbLockDirName so a
// future endpoint rename / kb migration doesn't blow up both lock dirs
// at once.
const globalLeaseDirName = "global-sync-leases"

// globalLeaseFileName is the filename of the lease file inside the
// per-endpoint subdirectory. The endpoint slug is the parent directory
// name, so this constant is the same for every endpoint.
const globalLeaseFileName = "global-sync.lease"

// Lease is a per-(user, endpoint) advisory lock claiming ownership of
// global-sync work. Only the daemon that successfully Acquired the lease
// runs team-context pulls and KB ListBubbles; other daemons set their
// lease handle to nil and skip those tickers.
//
// Lease lifetime is the daemon's lifetime — acquired in initComponents
// and released in shutdown. Process death releases the lock at the
// kernel level via flock semantics, so a crashed owner doesn't strand
// global sync.
type Lease struct {
	file     *os.File
	endpoint string

	// held is set to 1 while we still own the lock. Release uses
	// CompareAndSwap to make repeated calls a no-op (the file would
	// already be closed; double-Close is harmless but logging twice on
	// shutdown is noise).
	held atomic.Int32
}

// globalLeaseDirOnce guards lazy creation of the lease parent dir. We
// only need to attempt MkdirAll once per process; the per-endpoint
// subdirectory is created on demand inside AcquireGlobalSyncLease.
var (
	globalLeaseDirOnce   sync.Once
	globalLeaseDirCached string
	globalLeaseDirErr    error
)

// globalLeaseDir returns the directory containing per-endpoint lease
// subdirectories, creating it on first call. Lives under
// paths.DaemonStateDir() so it inherits the project's XDG vs legacy
// resolution and the daemon's existing state cleanup posture.
func globalLeaseDir() (string, error) {
	globalLeaseDirOnce.Do(func() {
		base := paths.DaemonStateDir()
		if base == "" {
			globalLeaseDirErr = errors.New("daemon state dir unresolved")
			return
		}
		dir := filepath.Join(base, globalLeaseDirName)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			globalLeaseDirErr = fmt.Errorf("create global lease dir: %w", err)
			return
		}
		globalLeaseDirCached = dir
	})
	if globalLeaseDirErr != nil {
		return "", globalLeaseDirErr
	}
	return globalLeaseDirCached, nil
}

// globalLeasePath returns the absolute path to the lease file for the
// given endpoint. The endpoint string is run through NormalizeSlug to
// produce a filesystem-safe path component (strips scheme, port,
// subdomain prefixes per .claude/rules/endpoints.md). Empty endpoint
// returns an error rather than a generic "default" path — the caller
// almost certainly has a bug if they got here with no endpoint.
func globalLeasePath(ep string) (string, error) {
	if ep == "" {
		return "", errors.New("empty endpoint")
	}
	slug := endpoint.NormalizeSlug(ep)
	if slug == "" || slug == "unknown" {
		return "", fmt.Errorf("invalid endpoint for lease path: %q", ep)
	}
	// defense in depth: a slug containing a path separator would escape
	// the lease dir. NormalizeSlug strips ports and schemes so this
	// shouldn't happen, but a stray bad input must not be able to flock
	// arbitrary files.
	if filepath.Base(slug) != slug {
		return "", fmt.Errorf("invalid endpoint slug for lease path: %q", slug)
	}
	dir, err := globalLeaseDir()
	if err != nil {
		return "", err
	}
	endpointDir := filepath.Join(dir, slug)
	if err := os.MkdirAll(endpointDir, 0o700); err != nil {
		return "", fmt.Errorf("create endpoint lease dir: %w", err)
	}
	return filepath.Join(endpointDir, globalLeaseFileName), nil
}

// AcquireGlobalSyncLease attempts a non-blocking exclusive flock on the
// per-endpoint lease file. Returns:
//
//   - (lease, nil) on success — caller MUST defer lease.Release() on
//     daemon shutdown.
//   - (nil, ErrNotOwner) when another daemon already holds the lease for
//     this endpoint. The daemon is still healthy — it just won't run
//     team-context or KB ListBubbles ticks.
//   - (nil, err) on filesystem failure or invalid endpoint.
func AcquireGlobalSyncLease(ep string) (*Lease, error) {
	path, err := globalLeasePath(ep)
	if err != nil {
		return nil, err
	}
	f, acquired, err := platformAcquireGlobalLease(path)
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, ErrNotOwner
	}
	l := &Lease{file: f, endpoint: ep}
	l.held.Store(1)
	return l, nil
}

// Release releases the underlying flock and closes the file. Idempotent:
// repeated calls after the first return nil without re-closing the fd.
// Safe to defer at daemon shutdown even if the daemon failed to start.
func (l *Lease) Release() error {
	if l == nil {
		return nil
	}
	if !l.held.CompareAndSwap(1, 0) {
		return nil
	}
	if err := platformReleaseGlobalLease(l.file); err != nil {
		// Restore held-state so callers can retry — otherwise a transient
		// flock release failure strands lock ownership until process exit.
		l.held.Store(1)
		return fmt.Errorf("release global sync lease: %w", err)
	}
	return nil
}

// IsHeld returns true while this Lease still owns the lock. Used by
// doctor checks and tests; daemon code should compare `d.lease != nil`
// directly since a non-nil lease that's been Released has already
// triggered daemon shutdown.
func (l *Lease) IsHeld() bool {
	if l == nil {
		return false
	}
	return l.held.Load() == 1
}

// Endpoint returns the endpoint string the lease was acquired for. For
// logging / doctor surfacing only.
func (l *Lease) Endpoint() string {
	if l == nil {
		return ""
	}
	return l.endpoint
}
