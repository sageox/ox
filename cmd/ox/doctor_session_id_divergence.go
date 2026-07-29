package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionid"
)

// doctor_session_id_divergence.go detects a session whose durable identity
// (ses_<UUIDv7>) has desynced between its two on-disk carriers:
//
//  1. sessions/<name>/meta.json -> session_id: the ledger's committed record,
//     written by the 'uploaded' notify path AFTER the session finishes.
//  2. the first-line _meta/header of that session's raw.jsonl: the
//     crash-safe carrier, minted at recording START (ID-at-start shipped
//     2026-07-18, commit 85f59500) so a daemon orphan-finalize can recover
//     an identity even if .recording.json is already gone.
//
// The two are supposed to always agree. On a real wedged ledger they
// didn't: two writers minted different IDs for one session, and 26 of 33
// conflicting meta.json files in that ledger differed ONLY in session_id.
// Because the header ID is what already reaches the outside world during
// the session (SageOx-Session commit trailers, PR bodies, the server's
// notifySessionUploaded dedup key — see session_linkage_finalize.go) while
// meta.json is written after, a divergence silently desyncs the ledger's
// own record from every URL and trailer that already circulated.
// filterPRLinkMisses (session_linkage_finalize.go) exact-matches the
// trailer and discards repair work the moment the two disagree, with
// nothing anywhere flagging that it happened.
//
// This check closes that visibility gap. It does NOT repair anything —
// see the doc comment on checkSessionIDDivergence for why blind repair
// is unsafe here.

// CheckSlugSessionIDDivergence is the slug for this check.
const CheckSlugSessionIDDivergence = "session-id-divergence"

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:     CheckSlugSessionIDDivergence,
		Name:     "Session ID divergence",
		Category: "Sessions",
		FixLevel: FixLevelCheckOnly,
		Description: "Detects sessions whose meta.json session_id disagrees with the ID minted in " +
			"raw.jsonl's header — report-only, see doc comment for why auto-repair is unsafe",
		Run: func(fix bool) checkResult { return checkSessionIDDivergence() },
	})
}

// sessionIDDivergence records one session directory where the two identity
// carriers are both present, both populated, and disagree.
type sessionIDDivergence struct {
	Name     string
	MetaID   string
	HeaderID string
}

// sessionIDScanResult is the outcome of scanning a ledger's sessions/
// directory for meta.json vs raw.jsonl header session_id agreement.
type sessionIDScanResult struct {
	checked    int                   // sessions where both IDs were readable and comparable
	diverged   []sessionIDDivergence // comparable but disagree — the failure mode this check exists for
	unreadable []string              // "<name>: <reason>" — corrupt/unreadable; NOT silently treated as fine
}

// checkSessionIDDivergence walks <ledger>/sessions/* and compares the
// session_id committed to meta.json against the ID carried in that
// session's raw.jsonl header.
//
// # Why report-only, never auto-fix
//
// The header ID looks like the obvious "source of truth" to auto-repair
// meta.json from: it is minted first, it is the crash-safe carrier a
// daemon orphan-finalize recovers from when .recording.json is already
// gone, and in the common case it is what already leaked into circulated
// commit trailers, PR bodies, and the server's dedup key before meta.json
// is ever written (see notifySessionUploaded, session_linkage_finalize.go).
// For the wedge this check was built to catch, the header is usually
// right and meta.json is the stale copy.
//
// But "usually" is not "always," and this check has no local way to tell
// the difference:
//
//   - resolveOrphanSessionID (doctor_session_upload_retry.go) deliberately
//     lets a PRESERVED meta.json ID win over the header ID when a prior
//     retry already stamped meta.json before crashing on push. In that
//     shape meta.json is the later, correct value and the header is the
//     stale one — exactly backwards from "header always wins."
//   - Telling which of the two IDs already shipped externally (a commit
//     trailer, a PR body, the server's dedup key) requires an API round
//     trip this check does not make. Guessing wrong would silently
//     re-diverge the ledger from whatever already shipped — the same
//     failure class this check exists to surface, just self-inflicted.
//   - Rewriting a committed session_id changes the ledger's historical
//     record of identity. That is a judgment call about which ID is
//     "real," not a mechanical repair like recreating a .gitignore entry.
//
// A wrong auto-repair here is expensive and hard to notice afterward — the
// incident that motivated this check was exactly this shape (two writers,
// one ledger, most of the conflicts differing only in session_id). A
// detect-only check that names both IDs side by side is already a real
// improvement over the prior silence, and leaves the actual resolution
// (which ID matches what already circulated) to whoever can check the
// external record.
func checkSessionIDDivergence() checkResult {
	const name = "Session ID divergence"

	ledgerPath := getLedgerPath()
	if ledgerPath == "" {
		return SkippedCheck(name, "no ledger found", "")
	}

	sessionsDir := filepath.Join(ledgerPath, "sessions")
	result, err := scanSessionIDDivergence(sessionsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return SkippedCheck(name, "no sessions directory", "")
		}
		return WarningCheck(name, "scan failed", err.Error())
	}

	if len(result.diverged) == 0 && len(result.unreadable) == 0 {
		return PassedCheck(name, fmt.Sprintf("%d session(s) checked, all agree", result.checked))
	}

	return sessionIDDivergenceFailure(name, result)
}

// scanSessionIDDivergence walks sessionsDir and classifies every session
// directory. A session is only comparable (counted in `checked`, eligible
// for `diverged`) when BOTH sides are present, readable, and populated:
//
//   - meta.json absent, or present with no session_id (pre-rollout legacy)
//     -> skipped. Missing/unreadable meta.json is session-upload-retry's
//     and session-ids' territory, not this check's; an empty SessionID
//     there is the expected pre-rollout state doctor_session_ids.go's
//     opt-in backfill already owns.
//   - raw.jsonl absent, or present only as a dehydrated LFS pointer stub
//     -> skipped. The header genuinely cannot be read locally; that is
//     not evidence of anything, let alone a divergence.
//   - raw.jsonl header well-formed but carries no session_id -> skipped.
//     Expected for recordings that predate ID-at-start (2026-07-18,
//     85f59500); reporting these as broken would be false-positive noise
//     on every pre-rollout session forever.
//   - either side corrupt/unreadable (invalid JSON, unrecognized shape,
//     I/O error) -> recorded in `unreadable`, never silently dropped.
//   - both sides present, populated, and disagree -> recorded in `diverged`.
func scanSessionIDDivergence(sessionsDir string) (sessionIDScanResult, error) {
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return sessionIDScanResult{}, err
	}

	var result sessionIDScanResult
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sessionName := e.Name()
		sessionDir := filepath.Join(sessionsDir, sessionName)

		// PreservedSessionID already distinguishes "absent or no field"
		// ("", nil) from "present but unreadable" ("", err) — reuse it
		// rather than re-parsing meta.json here.
		metaID, metaErr := lfs.PreservedSessionID(sessionDir)
		if metaErr != nil {
			result.unreadable = append(result.unreadable, fmt.Sprintf("%s: meta.json: %v", sessionName, metaErr))
			continue
		}

		rawPath := filepath.Join(sessionDir, "raw.jsonl")
		headerID, headerErr := readHeaderSessionIDStrict(rawPath)
		if headerErr != nil {
			if errors.Is(headerErr, errRawNotComparable) {
				continue // absent or dehydrated — nothing to compare, not a failure
			}
			result.unreadable = append(result.unreadable, fmt.Sprintf("%s: raw.jsonl: %v", sessionName, headerErr))
			continue
		}

		if metaID == "" || headerID == "" {
			// legacy on at least one side — expected during the rollout
			// window on either carrier independently, not a divergence.
			continue
		}

		result.checked++
		if metaID != headerID {
			result.diverged = append(result.diverged, sessionIDDivergence{
				Name: sessionName, MetaID: metaID, HeaderID: headerID,
			})
		}
	}

	sort.Slice(result.diverged, func(i, j int) bool { return result.diverged[i].Name < result.diverged[j].Name })
	sort.Strings(result.unreadable)
	return result, nil
}

// errRawNotComparable signals that raw.jsonl's header cannot be read
// locally for a benign reason (absent, or a dehydrated LFS pointer stub)
// — distinct from a parse/read failure, which callers must surface.
var errRawNotComparable = errors.New("raw.jsonl not locally comparable")

// readHeaderSessionIDStrict classifies raw.jsonl's header for divergence
// detection. session.ReadHeaderSessionID deliberately collapses every
// "can't get an ID" case to "" for its own callers (which only ever want
// "do I have an ID or should I mint one"); this check needs those cases
// told apart, because collapsing a corrupt header into "legacy, no ID" is
// exactly the kind of silent swallow CLAUDE.md's doctor mandate forbids.
//
// Once the shape is confirmed to be a parseable, recognizable header, ID
// extraction itself is delegated to session.ReadHeaderSessionID so the
// two callers can never disagree about what counts as a valid ses_ ID.
func readHeaderSessionIDStrict(rawPath string) (string, error) {
	if lfs.IsPointerFile(rawPath) {
		return "", errRawNotComparable // dehydrated clone — content not local
	}

	f, err := os.Open(rawPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", errRawNotComparable
		}
		return "", fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("not a regular file")
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)
	if !scanner.Scan() {
		return "", fmt.Errorf("empty file")
	}

	var entry map[string]any
	if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
		return "", fmt.Errorf("invalid JSON on header line: %w", err)
	}
	// Mirror session.ReadHeaderSessionID's acceptance condition EXACTLY:
	// (metadata is an object AND type=="header") OR (_meta is an object).
	//
	// Bare key-presence is not enough, and the gap is not cosmetic. A first
	// line that merely HAS a "metadata" key — an ordinary entry, or a header
	// whose metadata is a string rather than an object — would pass a
	// presence check, then ReadHeaderSessionID (which does assert the type
	// and the "header" tag) returns "". The scan reads that empty ID as
	// "legacy session, predates ID-at-start" and skips it silently. That is
	// precisely the corruption-swallowed-as-fine outcome this check exists
	// to prevent, so a shape the reader would reject must surface as
	// unreadable here.
	metadataObj, metadataIsObj := entry["metadata"].(map[string]any)
	_, metaIsObj := entry["_meta"].(map[string]any)
	isNativeHeader := metadataIsObj && entry["type"] == "header"
	if !isNativeHeader && !metaIsObj {
		return "", fmt.Errorf("first line is not a recognizable session header")
	}

	// A native ox header carries session_id as the ses_ recording identity,
	// alongside a separate agent_id. ParseStoreMeta only admits ses_-prefixed
	// values into StoreMeta.SessionID, so a present-but-malformed one reads
	// back as "" — indistinguishable from a legacy header that never had the
	// field, and therefore skipped silently. Present-but-invalid is corruption
	// of the identity carrier itself; say so.
	//
	// Deliberately NOT applied to the _meta shape. That format overloads
	// session_id as an AGENT identifier (store.go's StoreMeta note and
	// ParseStoreMeta's agent_id fallback), and the documented import format
	// ships values like "manual". Rejecting non-ses_ values there would report
	// every imported and adapter-produced session as unreadable.
	if isNativeHeader {
		if raw, present := metadataObj["session_id"]; present {
			id, isString := raw.(string)
			if !isString || !sessionid.IsValidSessionID(id) {
				return "", fmt.Errorf("header session_id is present but not a valid ses_ ID")
			}
		}
	}

	return session.ReadHeaderSessionID(rawPath), nil
}

// sessionIDDivergenceFailure builds the WarningCheck for a non-clean scan.
// Extracted so the message/detail shape is unit-testable without standing
// up a real ledger directory.
//
// WarningCheck (not CriticalCheck): a divergence here does not block any
// git/sync operation the way an unmerged-paths or stuck-rebase wedge does
// (checkLedgerUnmergedPaths, checkLedgerStuckOperation) — it silently
// breaks a downstream feature (PR-link repair) rather than halting
// everyone's push. That is the same tier as the "Legacy session IDs"
// check (doctor_session_ids.go), its closest sibling.
func sessionIDDivergenceFailure(name string, result sessionIDScanResult) checkResult {
	const sampleCap = 5

	var parts []string
	if len(result.diverged) > 0 {
		parts = append(parts, fmt.Sprintf("%d diverged", len(result.diverged)))
	}
	if len(result.unreadable) > 0 {
		parts = append(parts, fmt.Sprintf("%d unreadable", len(result.unreadable)))
	}
	msg := strings.Join(parts, ", ")

	var detail []string
	if len(result.diverged) > 0 {
		sample := result.diverged
		more := ""
		if len(sample) > sampleCap {
			more = fmt.Sprintf(" (+%d more)", len(sample)-sampleCap)
			sample = sample[:sampleCap]
		}
		lines := make([]string, len(sample))
		for i, d := range sample {
			lines[i] = fmt.Sprintf("%s: meta=%s header=%s", d.Name, d.MetaID, d.HeaderID)
		}
		detail = append(detail, fmt.Sprintf("meta.json vs raw.jsonl header disagree%s:\n       %s",
			more, strings.Join(lines, "\n       ")))
	}
	if len(result.unreadable) > 0 {
		detail = append(detail, fmt.Sprintf("could not compare (unreadable/corrupt):\n       %s",
			strings.Join(result.unreadable, "\n       ")))
	}
	detail = append(detail, "No auto-fix: which ID already shipped externally (commit trailer, PR body, "+
		"server dedup key) cannot be determined locally — see the doc comment on checkSessionIDDivergence.")

	return WarningCheck(name, msg, strings.Join(detail, "\n"))
}
