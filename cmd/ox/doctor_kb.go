package main

// Knowledge-bubble doctor checks. Three independent checks per the kb plan:
//
//  1. Orphan kb dirs   — local <kbRoot>/<kb_id>/ exists but the API list
//                        doesn't include it. AutoFix (FixLevelAuto) triggers
//                        the daemon's kb-GC pass which moves orphans to .trash/.
//
//  2. Failed-provision  — API row reports lifecycle_state="provision-failed".
//                        Server-side issue, no client autofix; FixLevelCheckOnly
//                        with a `requires_agent` hint that the user (or support)
//                        must investigate at sageox.ai.
//
//  3. Stale sync        — local <kbRoot>/<kb_id>/.sageox/meta.json reports a
//                        last_sync older than 1h. AutoFix kicks the daemon
//                        sync loop (which calls syncBubbles).
//
// All three checks are independent: a panic or skip in one MUST NOT prevent
// the other two from running. Per the kb-GC design, ErrKBAPIUnavailable is
// not a doctor warning — it just disables checks 1 and 2 (we cannot reason
// about orphans or lifecycle without the API). Check 3 is local-only and
// keeps running.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/paths"
)

// kbStaleSyncThreshold is the maximum age of a meta.json last_sync timestamp
// before doctor flags the bubble as stale. Matches the typical daemon read
// cadence (well under an hour) so we only flag bubbles that are genuinely
// behind, not ones in the middle of a normal pull cycle.
const kbStaleSyncThreshold = 1 * time.Hour

// kbLifecycleProvisionFailed is the lifecycle_state value the kb API returns
// when a bubble's server-side provisioning failed. Not exported as a typed
// constant from internal/api — the field is doc'd as a string to allow
// forward-compatible new states.
const kbLifecycleProvisionFailed = "provision-failed"

// kbDoctorListerFn returns the canonical list of kb rows the caller has
// access to according to the kb API. Returning api.ErrKBAPIUnavailable signals
// "skip API-dependent checks this pass" — same posture as kb GC. Tests
// override this via SetKBDoctorHooks; production wires it to a real
// api.KBClient call.
type kbDoctorListerFn func(ctx context.Context) ([]api.KB, error)

// kbDoctorSyncFn triggers an immediate kb sync via the daemon. Returns nil
// on success; returns an error wrapping a hint when the daemon is unreachable
// (so the user knows to start it). Tests override via SetKBDoctorHooks.
type kbDoctorSyncFn func(ctx context.Context) error

// kbDoctorGCFn triggers an immediate kb-GC orphan-triage pass via the daemon.
// Mirrors kbDoctorSyncFn — returns a hint-wrapped error if the daemon is not
// available. Tests override via SetKBDoctorHooks.
type kbDoctorGCFn func(ctx context.Context) error

// kbDoctorRootFn returns the on-disk parent directory of all per-bubble
// canonical clones for the active endpoint, equivalent to paths.KBDir("").
// Wrapping it lets us recover from paths.KBDir's panic when the endpoint
// is unset (tests with no SAGEOX_ENDPOINT, fresh installs, etc.) instead
// of crashing the whole doctor run.
type kbDoctorRootFn func() (string, error)

// kbDoctorHooks is the seam tests use to substitute fakes for the real API,
// daemon, and filesystem helpers. nil fields fall back to production wiring
// inside each check, so tests can override only the hook they care about.
type kbDoctorHooks struct {
	List   kbDoctorListerFn
	Sync   kbDoctorSyncFn
	GC     kbDoctorGCFn
	Root   kbDoctorRootFn
	Now    func() time.Time
	Logger *slog.Logger
}

// kbDoctorActiveHooks holds the current hook overrides. Defaults to all-nil
// (production behavior). Tests set this via SetKBDoctorHooks and reset at
// teardown.
var kbDoctorActiveHooks kbDoctorHooks

// SetKBDoctorHooks installs hook overrides for kb doctor checks. Pass an
// empty struct to reset to production behavior. Intended for tests only;
// production code never calls this.
func SetKBDoctorHooks(h kbDoctorHooks) {
	kbDoctorActiveHooks = h
}

// kbHookList returns the lister hook, falling back to a real KB API client.
func kbHookList() kbDoctorListerFn {
	if kbDoctorActiveHooks.List != nil {
		return kbDoctorActiveHooks.List
	}
	return defaultKBDoctorList
}

// kbHookSync returns the sync hook, falling back to a daemon RPC.
func kbHookSync() kbDoctorSyncFn {
	if kbDoctorActiveHooks.Sync != nil {
		return kbDoctorActiveHooks.Sync
	}
	return defaultKBDoctorSync
}

// kbHookGC returns the GC hook, falling back to a daemon RPC.
func kbHookGC() kbDoctorGCFn {
	if kbDoctorActiveHooks.GC != nil {
		return kbDoctorActiveHooks.GC
	}
	return defaultKBDoctorGC
}

// kbHookRoot returns the kb-root hook, falling back to paths.KBDir("").
func kbHookRoot() kbDoctorRootFn {
	if kbDoctorActiveHooks.Root != nil {
		return kbDoctorActiveHooks.Root
	}
	return defaultKBDoctorRoot
}

// kbHookNow returns the clock hook, falling back to time.Now.
func kbHookNow() func() time.Time {
	if kbDoctorActiveHooks.Now != nil {
		return kbDoctorActiveHooks.Now
	}
	return time.Now
}

// kbHookLogger returns the logger hook, falling back to slog.Default.
func kbHookLogger() *slog.Logger {
	if kbDoctorActiveHooks.Logger != nil {
		return kbDoctorActiveHooks.Logger
	}
	return slog.Default()
}

// defaultKBDoctorRoot resolves the canonical kb root for the active endpoint,
// recovering paths.KBDir's panic so an unconfigured endpoint becomes a
// returned error rather than an aborted doctor run.
func defaultKBDoctorRoot() (root string, err error) {
	defer func() {
		if r := recover(); r != nil {
			root = ""
			err = fmt.Errorf("kb root resolution panicked: %v", r)
		}
	}()
	root = paths.KBDir("")
	if root == "" {
		return "", errors.New("kb root unresolved")
	}
	return root, nil
}

// defaultKBDoctorList builds a kb API client bound to the project endpoint
// and lists bubbles. Returns api.ErrKBAPIUnavailable when no project
// endpoint or auth token is available so callers skip API-dependent checks.
func defaultKBDoctorList(ctx context.Context) ([]api.KB, error) {
	gitRoot := findGitRoot()
	ep := endpoint.GetForProject(gitRoot)
	if ep == "" {
		ep = endpoint.Get()
	}
	if ep == "" {
		return nil, api.ErrKBAPIUnavailable
	}

	tok, _ := auth.GetTokenForEndpoint(ep)
	client := api.NewKBClientWithEndpoint(ep)
	if tok != nil && tok.AccessToken != "" {
		client = client.WithAuthToken(tok.AccessToken)
	}
	return client.ListBubbles(ctx)
}

// defaultKBDoctorSync asks the daemon to run an immediate sync. Returns a
// helpful hint when the daemon isn't running so the auto-fix message tells
// the user how to recover instead of failing silently.
func defaultKBDoctorSync(_ context.Context) error {
	if !daemon.IsRunning() {
		return errors.New("daemon not running — run `ox daemon start` to enable kb sync")
	}
	client := daemon.NewClientForCurrentRepoWithTimeout(5 * time.Second)
	return client.RequestSync()
}

// defaultKBDoctorGC asks the daemon to run an immediate GC pass which, as a
// side effect, also runs the kb GC triage that move-asides orphan kb dirs.
// Same daemon-not-running hint as the sync hook.
func defaultKBDoctorGC(_ context.Context) error {
	if !daemon.IsRunning() {
		return errors.New("daemon not running — run `ox daemon start` to enable kb GC")
	}
	client := daemon.NewClientForCurrentRepoWithTimeout(30 * time.Second)
	_, err := client.TriggerGC()
	return err
}

// kbMetaOnDisk is a narrow shape for parsing <bubble>/.sageox/meta.json. We
// only care about last_sync here; redefining this locally (rather than
// importing the daemon's unexported kbMeta) keeps cmd/ox decoupled from the
// daemon package's internal types.
type kbMetaOnDisk struct {
	LastSync time.Time `json:"last_sync"`
}

// listLocalKBIDs returns the set of kb_id directory names under kbRoot,
// excluding the .trash/ marker dir and any other dotfile siblings. Errors
// are returned to the caller — the doctor decides whether they're fatal
// or informational.
func listLocalKBIDs(kbRoot string) ([]string, error) {
	entries, err := os.ReadDir(kbRoot)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		name := ent.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		ids = append(ids, name)
	}
	sort.Strings(ids)
	return ids, nil
}

// readKBMeta loads <kbDir>/.sageox/meta.json and returns the parsed shape.
// Returns os.ErrNotExist (wrapped) when the file is absent so callers can
// distinguish "initial-clone-pending" from "corrupt meta".
func readKBMeta(kbDir string) (kbMetaOnDisk, error) {
	metaPath := filepath.Join(kbDir, ".sageox", "meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return kbMetaOnDisk{}, err
	}
	var meta kbMetaOnDisk
	if err := json.Unmarshal(data, &meta); err != nil {
		return kbMetaOnDisk{}, fmt.Errorf("parse %s: %w", metaPath, err)
	}
	return meta, nil
}

// ----------------------------------------------------------------------
// Check 1: orphan kb dirs
// ----------------------------------------------------------------------

// checkKBOrphans walks the kb root, fetches the API list, and reports any
// local kb_id that the API no longer recognizes. AutoFix triggers the
// daemon's kb-GC triage pass.
//
// Skips entirely when the kb API is unavailable: we can't reason about
// orphans without the source of truth. Skips when the kb root doesn't exist
// (fresh install) or when the endpoint is unconfigured.
func checkKBOrphans(fix bool) checkResult {
	const name = "kb orphan dirs"

	root, err := kbHookRoot()()
	if err != nil {
		return SkippedCheck(name, "endpoint unresolved", "")
	}
	if _, statErr := os.Stat(root); os.IsNotExist(statErr) {
		return SkippedCheck(name, "no local kb store yet", "")
	} else if statErr != nil {
		return SkippedCheck(name, "kb root unreadable", statErr.Error())
	}

	localIDs, err := listLocalKBIDs(root)
	if err != nil {
		return SkippedCheck(name, "kb root readdir failed", err.Error())
	}
	if len(localIDs) == 0 {
		return PassedCheck(name, "no local bubbles")
	}

	// API source-of-truth check. If unavailable, skip — never delete on guesses.
	listCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bubbles, err := kbHookList()(listCtx)
	if err != nil {
		if errors.Is(err, api.ErrKBAPIUnavailable) {
			kbHookLogger().Debug("kb_doctor orphan check skipped: kb API unavailable")
			return SkippedCheck(name, "kb API unavailable", "")
		}
		return SkippedCheck(name, "kb API list failed", err.Error())
	}

	known := make(map[string]struct{}, len(bubbles))
	for _, b := range bubbles {
		if b.KBID != "" {
			known[b.KBID] = struct{}{}
		}
	}

	var orphans []string
	for _, id := range localIDs {
		if _, ok := known[id]; !ok {
			orphans = append(orphans, id)
		}
	}

	if len(orphans) == 0 {
		return PassedCheck(name, fmt.Sprintf("%d bubble(s) reconciled", len(localIDs)))
	}

	msg := fmt.Sprintf("%d orphan(s): %s", len(orphans), strings.Join(orphans, ", "))
	detail := fmt.Sprintf("Local dir under %s; will move-aside to .trash/", root)

	if !fix {
		return WarningCheck(name, msg, detail)
	}

	// AutoFix path — kick the daemon's GC pass.
	gcCtx, gcCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer gcCancel()
	if gcErr := kbHookGC()(gcCtx); gcErr != nil {
		kbHookLogger().Warn("kb_doctor orphan autofix failed", "error", gcErr, "orphans", len(orphans))
		return FailedCheck(name, msg, fmt.Sprintf("Auto-fix failed: %v", gcErr))
	}

	// Recheck: confirm the orphans are gone from the canonical root.
	postIDs, err := listLocalKBIDs(root)
	if err != nil {
		// fix likely succeeded but we can't confirm — surface as warning.
		return WarningCheck(name, "orphans triaged; recheck failed", err.Error())
	}
	postSet := make(map[string]struct{}, len(postIDs))
	for _, id := range postIDs {
		postSet[id] = struct{}{}
	}
	var stillPresent []string
	for _, id := range orphans {
		if _, ok := postSet[id]; ok {
			stillPresent = append(stillPresent, id)
		}
	}
	if len(stillPresent) > 0 {
		return FailedCheck(name,
			fmt.Sprintf("%d orphan(s) still present after autofix", len(stillPresent)),
			strings.Join(stillPresent, ", "))
	}

	kbHookLogger().Info("kb_doctor orphan autofix complete", "moved", len(orphans))
	return PassedCheck(name, fmt.Sprintf("triaged %d orphan(s) to .trash/", len(orphans)))
}

// ----------------------------------------------------------------------
// Check 2: failed-provisioning bubbles
// ----------------------------------------------------------------------

// checkKBFailedProvision flags any bubble whose kb API row reports
// lifecycle_state="provision-failed". This is a server-side condition;
// the doctor cannot repair it from the client. The check marks itself
// as RequiresAgent so the priority-summary view routes it to the
// "Requires AI Coworker" / human-action bucket.
//
// Skips when the kb API is unavailable — same posture as the orphan check.
func checkKBFailedProvision(_ bool) checkResult {
	const name = "kb provisioning"

	listCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bubbles, err := kbHookList()(listCtx)
	if err != nil {
		if errors.Is(err, api.ErrKBAPIUnavailable) {
			kbHookLogger().Debug("kb_doctor provision check skipped: kb API unavailable")
			return SkippedCheck(name, "kb API unavailable", "")
		}
		return SkippedCheck(name, "kb API list failed", err.Error())
	}

	var failed []string
	for _, b := range bubbles {
		if b.LifecycleState == kbLifecycleProvisionFailed {
			label := b.Slug
			if label == "" {
				label = b.KBID
			}
			failed = append(failed, label)
		}
	}
	if len(failed) == 0 {
		return PassedCheck(name, fmt.Sprintf("%d bubble(s) provisioned", len(bubbles)))
	}

	sort.Strings(failed)
	msg := fmt.Sprintf("%d bubble(s) failed to provision: %s", len(failed), strings.Join(failed, ", "))
	detail := "Server-side provisioning failed. This is rare — contact support or check sageox.ai for the bubble's status."

	// requiresAgent: the user (or support) needs to act on the cloud side.
	// Doctor itself has nothing to fix locally.
	return AgentRequiredCheck(name, msg, detail)
}

// ----------------------------------------------------------------------
// Check 3: stale sync
// ----------------------------------------------------------------------

// checkKBStaleSync inspects each local bubble's meta.json and reports any
// whose last_sync is older than kbStaleSyncThreshold. Bubbles missing
// meta.json are skipped (initial-clone-pending). AutoFix kicks the daemon
// sync loop.
//
// This check intentionally does NOT depend on the kb API — staleness is
// an entirely local signal, and the fix only needs the daemon to be
// reachable. So it keeps running even when the kb API is unavailable,
// because that's exactly when staleness is most likely.
func checkKBStaleSync(fix bool) checkResult {
	const name = "kb sync freshness"

	root, err := kbHookRoot()()
	if err != nil {
		return SkippedCheck(name, "endpoint unresolved", "")
	}
	if _, statErr := os.Stat(root); os.IsNotExist(statErr) {
		return SkippedCheck(name, "no local kb store yet", "")
	} else if statErr != nil {
		return SkippedCheck(name, "kb root unreadable", statErr.Error())
	}

	localIDs, err := listLocalKBIDs(root)
	if err != nil {
		return SkippedCheck(name, "kb root readdir failed", err.Error())
	}
	if len(localIDs) == 0 {
		return PassedCheck(name, "no local bubbles")
	}

	now := kbHookNow()()
	var stale []string
	var checked int
	for _, id := range localIDs {
		bubbleDir := filepath.Join(root, id)
		meta, metaErr := readKBMeta(bubbleDir)
		if metaErr != nil {
			if os.IsNotExist(metaErr) {
				// initial-clone-pending — daemon hasn't written meta yet.
				continue
			}
			kbHookLogger().Warn("kb_doctor stale-sync read meta failed", "kb_id", id, "error", metaErr)
			continue
		}
		checked++
		if meta.LastSync.IsZero() {
			continue
		}
		if now.Sub(meta.LastSync) > kbStaleSyncThreshold {
			stale = append(stale, id)
		}
	}

	if len(stale) == 0 {
		if checked == 0 {
			return SkippedCheck(name, "no meta.json yet", "initial sync may still be pending")
		}
		return PassedCheck(name, fmt.Sprintf("%d bubble(s) fresh", checked))
	}

	sort.Strings(stale)
	msg := fmt.Sprintf("%d stale bubble(s): %s", len(stale), strings.Join(stale, ", "))
	detail := fmt.Sprintf("last_sync older than %s; daemon will refresh on next pass", kbStaleSyncThreshold)

	if !fix {
		return WarningCheck(name, msg, detail)
	}

	// AutoFix: kick an immediate daemon sync.
	syncCtx, syncCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer syncCancel()
	if syncErr := kbHookSync()(syncCtx); syncErr != nil {
		kbHookLogger().Warn("kb_doctor stale-sync autofix failed", "error", syncErr, "stale", len(stale))
		return WarningCheck(name, msg, fmt.Sprintf("Auto-fix hint: %v", syncErr))
	}

	// Recheck: a synchronous RequestSync should have written fresh meta.json
	// timestamps. If a bubble is still stale after the kick, we report it
	// (this can legitimately happen if the daemon is still in the middle of
	// processing earlier bubbles, but it's worth surfacing).
	now = kbHookNow()()
	var stillStale []string
	for _, id := range stale {
		meta, metaErr := readKBMeta(filepath.Join(root, id))
		if metaErr != nil {
			continue // already filtered above; treat as best-effort
		}
		if !meta.LastSync.IsZero() && now.Sub(meta.LastSync) > kbStaleSyncThreshold {
			stillStale = append(stillStale, id)
		}
	}
	if len(stillStale) > 0 {
		return WarningCheck(name,
			fmt.Sprintf("%d bubble(s) still stale after sync kick", len(stillStale)),
			strings.Join(stillStale, ", "))
	}
	kbHookLogger().Info("kb_doctor stale-sync autofix complete", "refreshed", len(stale))
	return PassedCheck(name, fmt.Sprintf("kicked sync for %d stale bubble(s)", len(stale)))
}

// runKBChecks runs the three kb checks with independent panic recovery so
// one check's failure cannot cascade into the others. Returns the aggregated
// check results in the order: orphans, provisioning, stale sync.
//
// Skips entirely when the kb root cannot be resolved (e.g. unauthenticated
// or no endpoint), to keep the doctor output clean for users who haven't
// opted into knowledge bubbles yet.
func runKBChecks(opts doctorOptions) []checkResult {
	// short-circuit: if we can't even resolve the root, there's nothing to
	// check. This is the "not logged in / no endpoint" case.
	if _, err := kbHookRoot()(); err != nil {
		return nil
	}

	results := make([]checkResult, 0, 7)
	for _, fn := range []struct {
		name string
		run  func() checkResult
	}{
		{"orphans", func() checkResult { return checkKBOrphans(opts.shouldFix(CheckSlugKBOrphans)) }},
		{"provisioning", func() checkResult { return checkKBFailedProvision(false) }},
		{"stale-sync", func() checkResult { return checkKBStaleSync(opts.shouldFix(CheckSlugKBStaleSync)) }},
		{"missing-clone", func() checkResult { return checkKBMissingClone(opts.shouldFix(CheckSlugKBMissingClone)) }},
		{"wedged", func() checkResult { return checkKBWedged(opts.shouldFix(CheckSlugKBWedged)) }},
		{"sparse-checkout", func() checkResult { return checkKBSparseCheckout(opts.shouldFix(CheckSlugKBSparseCheckout)) }},
		{"global-sync-owner", func() checkResult {
			return checkKBGlobalSyncNoOwner(opts.shouldFix(CheckSlugKBGlobalSyncNoOwner))
		}},
	} {
		// per-check recover so a panic in one doesn't silence the others.
		func(name string, run func() checkResult) {
			defer func() {
				if r := recover(); r != nil {
					kbHookLogger().Warn("kb_doctor check panicked", "check", name, "panic", r)
					results = append(results, FailedCheck(
						fmt.Sprintf("kb %s", name),
						"check panicked",
						fmt.Sprintf("%v", r)))
				}
			}()
			results = append(results, run())
		}(fn.name, fn.run)
	}
	return results
}
