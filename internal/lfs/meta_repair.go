package lfs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// MetaRepairOutcome reports what RecoverEmptyTitleMeta did to a single
// session's meta.json. Designed to be cheap to produce on every call —
// the common path (nothing to repair) returns Skipped=true and the
// caller can ignore the rest.
type MetaRepairOutcome struct {
	SessionName       string
	Skipped           bool   // meta.json was already healthy or terminal
	RecoveredFromJSON bool   // pulled a clean title out of summary.json
	BumpedAttempts    bool   // no recovery available; SummaryAttempts incremented
	FlippedTerminal   bool   // hit MaxSummaryAttempts; status set to unrecoverable
	Error             string // non-fatal; meta.json was not modified
}

// RecoverEmptyTitleMeta inspects one session's meta.json for the
// post-Apr-27 empty-title failure shape (meta.title=="" with status !=
// unrecoverable). When summary.json carries a real title, promotes it
// back into meta and stamps SummaryStatus=ok. Otherwise increments
// SummaryAttempts and, at MaxSummaryAttempts, flips status to
// unrecoverable so future calls early-exit.
//
// This is the daemon-safe core of the empty-title repair flow. Both
// the CLI `ox session repair-meta-summary` tool and the autofix
// scheduler delegate to this so the on-disk behavior is identical.
//
// Idempotency contract: running this repeatedly on a healthy meta is a
// no-op (Skipped=true, no write). Running it repeatedly on an
// unrecoverable meta is also a no-op. Running it on a fixable meta
// applies the fix once and then early-exits on subsequent calls.
//
// dryRun=true returns the outcome without writing meta.json.
func RecoverEmptyTitleMeta(sessionDir string, dryRun bool) MetaRepairOutcome {
	name := filepath.Base(sessionDir)
	out := MetaRepairOutcome{SessionName: name}

	meta, err := ReadSessionMeta(sessionDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			out.Skipped = true
			return out
		}
		out.Error = err.Error()
		return out
	}

	// Healthy or terminal — nothing to do. We treat any non-empty
	// trimmed title as "healthy" because that's what the web UI will
	// render; we don't second-guess whether the title is good.
	if strings.TrimSpace(meta.Title) != "" {
		out.Skipped = true
		return out
	}
	// Terminal failure — daemon already exhausted MaxSummaryAttempts on
	// this session. Stop trying. The session is teammate-visible (LFS
	// upload happened) and the row title falls back to the session-name
	// slug in the UI.
	if meta.SummaryStatus == "unrecoverable" {
		out.Skipped = true
		return out
	}

	// Try to recover a clean title from summary.json. The daemon may
	// have written a failure-stub summary.json with title="" too — in
	// which case readSummaryJSONTitle returns "" and we fall through to
	// the bump-attempts path.
	if cleanTitle := readSummaryJSONTitle(sessionDir); cleanTitle != "" {
		meta.Title = cleanTitle
		// Mirror title into summary if summary.json doesn't carry a
		// distinct one. Keeps the two fields consistent for older
		// readers that prefer Summary over Title.
		if strings.TrimSpace(meta.Summary) == "" {
			meta.Summary = cleanTitle
		}
		meta.SummaryStatus = "ok"
		meta.ValidationError = ""
		meta.SummaryAttempts = 0
		out.RecoveredFromJSON = true
	} else {
		meta.SummaryAttempts++
		out.BumpedAttempts = true
		if meta.SummaryAttempts >= MaxSummaryAttempts {
			meta.SummaryStatus = "unrecoverable"
			out.FlippedTerminal = true
		}
	}

	if dryRun {
		return out
	}
	if err := WriteSessionMetaOnly(sessionDir, meta); err != nil {
		out.Error = "write meta.json: " + err.Error()
	}
	return out
}

// ResetInlineSummaryEligible resets sessions that failed summarization due
// to the pre-0.7.2 file-read prompt bug. Targets sessions with
// summary_status "unrecoverable" or "failed_validation" whose
// validation_error contains "title too short".
//
// If client is non-nil and raw.jsonl is an LFS pointer, hydrates it to the
// ledger cache so the daemon can read actual content for summarization.
//
// Returns true if the session was reset, false if not eligible.
//
// # The reset is gated on the session actually being summarizable (GH #710)
//
// This used to clear the status/attempts/error FIRST and hydrate second,
// resetting even when hydration was impossible. Because a doctor autofix
// runs it daily, that re-armed the MaxSummaryAttempts cap every 24 hours
// — so a session whose raw.jsonl is an unhydratable content-store stub
// never settled into a terminal state. It burned three LLM calls a day,
// forever, each one emitting a fresh "finalize session" commit. That is
// how the #710 reporter accumulated 21 duplicate commits across 5
// sessions and, downstream, an unpushable ledger.
//
// Now: verify the transcript can actually be read before clearing the
// terminal state, and never write a .needs-summary marker pointing at a
// file that is still a pointer.
func ResetInlineSummaryEligible(sessionDir string, dryRun bool, client *Client, ledgerPath string) (reset bool) {
	meta, err := ReadSessionMeta(sessionDir)
	if err != nil {
		return false
	}

	eligible := (meta.SummaryStatus == "unrecoverable" || meta.SummaryStatus == "failed_validation") &&
		strings.Contains(meta.ValidationError, "title too short")
	if !eligible {
		return false
	}

	// Resolve a readable transcript BEFORE touching the meta. A session we
	// cannot read is a session we cannot summarize, and clearing its
	// terminal state would just re-enter the loop on the next daemon pass.
	rawPath := filepath.Join(sessionDir, "raw.jsonl")
	markerDir := sessionDir
	if IsPointerFile(rawPath) {
		sessionName := filepath.Base(sessionDir)
		cacheDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", sessionName)
		cachePath := filepath.Join(cacheDir, "raw.jsonl")

		if info, statErr := os.Stat(cachePath); statErr == nil && info.Size() > 0 {
			// already hydrated by an earlier pass
			rawPath, markerDir = cachePath, cacheDir
		} else {
			hydrated, hydrateErr := HydrateRawToCacheErr(client, sessionDir, ledgerPath)
			if hydrateErr != nil || hydrated == "" {
				// Leave the terminal status intact. Retryable failures get
				// another shot on the next pass because the meta still
				// matches the eligibility filter; ErrNoLFSManifest never
				// will, which is correct — that content is gone.
				return false
			}
			rawPath, markerDir = hydrated, cacheDir
		}
	}

	// Whatever path we landed on must actually be readable. A pointer that
	// hydrated gets here with the cache path; a real transcript gets here
	// unchanged; but a MISSING or empty raw.jsonl reaches here too —
	// IsPointerFile is false for it, so the branch above never ran.
	//
	// Without this, an absent transcript would clear the terminal state and
	// get a .needs-summary marker pointing at a file that does not exist,
	// sending the daemon straight back into the loop this function exists
	// to stop — the same failure as the pointer case, one branch over.
	if info, err := os.Stat(rawPath); err != nil || info.Size() == 0 {
		return false
	}

	if dryRun {
		return true
	}

	meta.SummaryStatus = ""
	meta.SummaryAttempts = 0
	meta.ValidationError = ""
	if err := WriteSessionMetaOnly(sessionDir, meta); err != nil {
		return false
	}

	writeNeedsSummaryMarker(markerDir, rawPath, sessionDir)
	return true
}

func writeNeedsSummaryMarker(dir, rawPath, ledgerSessionDir string) {
	marker := struct {
		CacheDir         string `json:"cache_dir"`
		RawPath          string `json:"raw_path"`
		LedgerSessionDir string `json:"ledger_session_dir"`
	}{dir, rawPath, ledgerSessionDir}
	data, err := json.Marshal(marker)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, ".needs-summary"), data, 0644)
}

// HydrateRawToCache downloads raw.jsonl from LFS to the session cache dir.
// Used by the doctor autofix to make pointer-stub sessions available for
// re-summarization. The daemon's Detect() scans the cache dir first, so
// placing hydrated content there allows re-summarization without modifying
// the git-tracked pointer file.
//
// Returns the cache path on success, empty string on failure.
func HydrateRawToCache(client *Client, sessionDir, ledgerPath string) string {
	path, _ := HydrateRawToCacheErr(client, sessionDir, ledgerPath)
	return path
}

// ErrNoLFSManifest reports that a session's meta.json carries no usable
// content-store reference for raw.jsonl — no "raw.jsonl" entry, or one
// with an empty OID.
//
// This is the ONE hydration failure that is permanent. It means the
// transcript was never uploaded, so no amount of retrying will produce
// it, and callers should mark the session terminal rather than loop.
//
// Every OTHER failure — auth, transport, 4xx/5xx, disk — is retryable and
// must NOT be treated as terminal. Getting this distinction backwards is
// costly in both directions: condemning on a transient 503 throws away a
// recoverable session, while retrying on ErrNoLFSManifest recreates the
// unbounded loop GH #710 was filed about.
var ErrNoLFSManifest = errors.New("session meta.json has no content-store reference for raw.jsonl")

// HydrateRawToCacheErr is HydrateRawToCache with a diagnosable failure.
//
// The string-only version returns "" on nine distinct failure paths, all
// indistinguishable, which is why the daemon could neither report why a
// session would not hydrate nor decide whether retrying was worthwhile.
// New callers should use this; HydrateRawToCache remains as a thin
// wrapper so existing ones keep compiling.
func HydrateRawToCacheErr(client *Client, sessionDir, ledgerPath string) (string, error) {
	sessionName := filepath.Base(sessionDir)
	cacheDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", sessionName)
	cachePath := filepath.Join(cacheDir, "raw.jsonl")

	// Already cached — checked FIRST, before touching meta.json or
	// requiring a client. An earlier pass may have hydrated this, and
	// demanding credentials to hand back a file already on disk would
	// fail offline for no reason.
	if info, err := os.Stat(cachePath); err == nil && info.Size() > 0 {
		return cachePath, nil
	}

	meta, err := ReadSessionMeta(sessionDir)
	if err != nil {
		return "", fmt.Errorf("read meta.json: %w", err)
	}

	rawRef, ok := meta.Files["raw.jsonl"]
	if !ok || rawRef.OID == "" {
		// The manifest is not the only place the OID lives — the on-disk
		// pointer file carries it too, and the two can disagree (a partial
		// write, or a meta.json rebuilt by one of the pre-#710 builder
		// paths that dropped Files). Fall back to the pointer before
		// declaring the content gone.
		//
		// This matters because ErrNoLFSManifest is the ONE error callers
		// treat as terminal: a false positive here permanently condemns a
		// session whose transcript is sitting in the content store,
		// perfectly recoverable.
		if ref, perr := ReadPointerFile(filepath.Join(sessionDir, "raw.jsonl")); perr == nil && ref.OID != "" {
			rawRef = ref
		} else {
			return "", ErrNoLFSManifest
		}
	}

	if client == nil {
		return "", errors.New("no content-store client (not authenticated, or ledger has no remote)")
	}

	bareOID := rawRef.BareOID()
	resp, err := client.BatchDownload([]BatchObject{{OID: bareOID, Size: rawRef.Size}})
	if err != nil {
		return "", fmt.Errorf("LFS batch download: %w", err)
	}
	if len(resp.Objects) == 0 {
		return "", fmt.Errorf("LFS batch returned no objects for %s", bareOID)
	}
	obj := resp.Objects[0]
	if obj.Error != nil {
		return "", fmt.Errorf("LFS object error for %s: %s", bareOID, obj.Error.Message)
	}
	if obj.Actions == nil || obj.Actions.Download == nil {
		return "", fmt.Errorf("LFS batch returned no download action for %s", bareOID)
	}

	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("create cache dir: %w", err)
	}

	tmpPath := cachePath + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}

	if err := DownloadToFile(obj.Actions.Download, f, true, bareOID); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("download %s: %w", bareOID, err)
	}
	// Check Close: a write buffered by the OS can fail at flush time (ENOSPC,
	// EIO). Ignoring it would rename a silently truncated transcript into the
	// cache, where it reads as legitimate content and produces a wrong summary
	// — far worse than reporting a retryable failure.
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("flush cached copy: %w", err)
	}

	if err := os.Rename(tmpPath, cachePath); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("install cached copy: %w", err)
	}

	return cachePath, nil
}

// readSummaryJSONTitle returns a trimmed, non-leaky title from
// summary.json or "" if none can be safely recovered. Mirrors the
// shape of cmd/ox/session_repair_meta_summary.go's
// readSummaryJSONForRepair so the two callers can converge once the
// CLI tool migrates to delegate fully to RecoverEmptyTitleMeta.
func readSummaryJSONTitle(sessionDir string) string {
	data, err := os.ReadFile(filepath.Join(sessionDir, "summary.json"))
	if err != nil {
		return ""
	}
	var sj struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(data, &sj); err != nil {
		return ""
	}
	t := strings.TrimSpace(sj.Title)
	if t == "" {
		return ""
	}
	if IsLeakySummaryString(t) {
		return ""
	}
	return t
}
