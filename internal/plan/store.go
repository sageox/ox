package plan

// Plan capture-to-ledger. Mirrors the session storage model (internal/session)
// but optimized for the plan artifact's read pattern: small canonical text
// (plan.md, annotations.json, meta.json) stays PLAIN git so a teammate's
// DEHYDRATED clone can list / read / search plans with zero LFS hydration.
// Only a heavy pre-rendered plan.html is size-gated into an LFS pointer
// (hydrate-on-demand), exactly how sessions keep summary.md plain but
// raw.jsonl in LFS.
//
// Layout: <ledger>/data/plans/YYYY-MM-DD-<2-4-word-slug>/
//
//	plan.md          # always plain git (Input.Raw)
//	annotations.json # always plain git (the Result — searchable badge data)
//	meta.json        # always plain git (topic, slug, authors, timestamps, …)
//	plan.html        # only if a render was passed in; plain git if small,
//	                 # LFS pointer (internal/lfs.WritePointerFile) if large.
//
// CLI WRITES files into the ledger working tree (daemon-git split): we never
// commit inline and never discard uncommitted changes — same as session
// capture, which writes meta.json + content and lets a later push carry it.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/paths"
)

const (
	planMDFile      = "plan.md"
	annotationsFile = "annotations.json"
	planMetaFile    = "meta.json"
	planHTMLFile    = "plan.html"

	// htmlLFSThreshold is the size above which a passed-in plan.html is stored
	// as an LFS pointer rather than committed plain. ~256KB: base64 imagery and
	// large rendered plans cross this; a normal text-only render stays well
	// under it and is kept plain so a dehydrated clone reads it directly.
	htmlLFSThreshold = 256 * 1024
)

// PlanStatus is the plan's own lifecycle, independent of the producing
// session. A plan is worth keeping even if never built — it is a decision
// record and prior-art seed — so status lets UI/search weight rather than
// discard. v1: a plain writable field; no CLI auto-detection of "implemented"
// (that correlation is inference and belongs in the cloud judge per ADR-021).
type PlanStatus string

const (
	PlanStatusDraft       PlanStatus = "draft"
	PlanStatusApproved    PlanStatus = "approved" // reviewer signed off via the review loop
	PlanStatusImplemented PlanStatus = "implemented"
	PlanStatusAbandoned   PlanStatus = "abandoned"
	PlanStatusSuperseded  PlanStatus = "superseded"
)

// Session-outcome values for Provenance.SessionOutcome. "" == unknown.
const (
	SessionOutcomeActive  = "active"
	SessionOutcomeStopped = "stopped"
	SessionOutcomeAborted = "aborted"
)

// Provenance ties a saved plan back to the session/agent/repo that produced
// it. It is DENORMALIZED on purpose: the join keys (SessionID/AgentID/RepoID)
// are the precise link, but a session can be aborted, never uploaded, or GC'd,
// so the snapshot fields (AgentType/Model/AuthorName) let a plan render fully
// without the session present. Duplication is the feature, not a smell.
type Provenance struct {
	// Join keys (may dangle if the session was aborted / never uploaded).
	//
	// Two-phase population, because the canonical ses_ SessionID is minted
	// fresh at session-STOP and is NOT knowable mid-recording:
	//   - SessionName is the durable identifier available at plan-save time
	//     (the recording's folder name, what `ox session view <name>` resolves).
	//     It is the primary join key and is always set when a recording is live.
	//   - SessionID (ses_<UUIDv7>) is BACKFILLED at session-stop, in the same
	//     reconciliation that sets SessionOutcome=stopped — we have the real id
	//     and the produced-plan slugs in hand there. Empty for aborted sessions
	//     (no stop) and for plans saved outside a recording.
	SessionName string `json:"session_name,omitempty"`
	SessionID   string `json:"session_id,omitempty"` // ses_<UUIDv7>, backfilled at stop
	AgentID     string `json:"agent_id,omitempty"`   // Ox#### stable agent instance
	RepoID      string `json:"repo_id,omitempty"`

	// Denormalized snapshot — renders without the session present.
	AgentType  string `json:"agent_type,omitempty"` // claude-code, codex, ...
	Model      string `json:"model,omitempty"`
	AuthorName string `json:"author_name,omitempty"` // privacy-safe display name at save time

	// SessionOutcome is RECONCILED SYSTEM STATE, not authored provenance:
	// "" (unknown) | "active" | "stopped" | "aborted". Written only by
	// session-stop / `ox doctor` through MutatePlanMeta, never by Save.
	SessionOutcome string `json:"session_outcome,omitempty"`
}

// CollabSignals are deterministic, locally-counted facts about the human↔agent
// collaboration that produced the plan — effort proxies, NOT a score. Scoring
// (a rigor judgment) is authored by the agent now / a cloud judge later, per
// ADR-021. Signal COUNTS (collisions/prior-art/expert-routes) deliberately
// live in annotations.json (Result.Signals), not here, to avoid duplication.
type CollabSignals struct {
	UserPrompts     int `json:"user_prompts"`     // distinct human turns before the plan
	AgentQuestions  int `json:"agent_questions"`  // AskUserQuestion / clarifying tool calls
	ToolCalls       int `json:"tool_calls"`       // exploration-depth proxy
	DurationSeconds int `json:"duration_seconds"` // first user prompt → plan finalized
}

// Meta is the git-tracked descriptor written as meta.json alongside a captured
// plan. It carries the searchable, hydration-free facts about the plan: who
// authored it, when, where it came from, which session/agent produced it, and
// how thoughtful the collaboration was.
type Meta struct {
	// SchemaVersion stamps the meta.json shape (set to SchemaVersion on write)
	// so a future reader can detect and migrate an older layout.
	SchemaVersion  string    `json:"schema_version,omitempty"`
	Topic          string    `json:"topic"`
	Slug           string    `json:"slug"`
	Authors        []string  `json:"authors,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	SourcePlanPath string    `json:"source_plan_path,omitempty"`

	// Status is the plan's lifecycle. Missing == "draft" for legacy plans.
	Status PlanStatus `json:"status,omitempty"`
	// Provenance links the plan to its producing session/agent/repo (forward).
	Provenance *Provenance `json:"provenance,omitempty"`
	// Collaboration holds the deterministic collaboration-effort counts.
	Collaboration *CollabSignals `json:"collaboration,omitempty"`
}

// PlanInfo is the listing-level view of a captured plan, assembled from
// meta.json. Dir is the absolute path to the plan folder.
type PlanInfo struct {
	Slug      string
	Topic     string
	Dir       string
	CreatedAt time.Time
	Authors   []string
	HasHTML   bool
}

// Save writes a captured plan into the ledger under data/plans/<dated-slug>/.
// It writes plan.md (from in.Raw), annotations.json (res), and meta.json as
// plain git-tracked text. plan.html is written ONLY when html != nil: plain
// when small, as an LFS pointer when it exceeds htmlLFSThreshold. Save never
// renders HTML and never commits — it only materializes files in the working
// tree. Returns the absolute plan directory.
//
// gitRoot is the producing project's git root; the ledger path is resolved
// from it via ProjectContext. Returns an error if no ledger is configured
// (the caller decides whether that is fatal — the porcelain path treats it as
// "nothing to save").
func Save(gitRoot string, in Input, res Result, html []byte, meta Meta) (string, error) {
	ledger := ledgerPathFor(gitRoot)
	if ledger == "" {
		return "", fmt.Errorf("no ledger configured for %q: cannot save plan", gitRoot)
	}

	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = time.Now().UTC()
	}
	if meta.Slug == "" {
		meta.Slug = Slugify(meta.Topic)
	}

	// Stamp the current schema version onto both long-lived artifacts so a
	// future reader can detect and migrate an older on-disk layout.
	meta.SchemaVersion = SchemaVersion
	res.SchemaVersion = SchemaVersion

	dirName := fmt.Sprintf("%s-%s", meta.CreatedAt.UTC().Format("2006-01-02"), meta.Slug)
	dir := filepath.Join(paths.LedgerPlansDir(ledger), dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create plan dir=%s: %w", dir, err)
	}

	// plan.md — plain git, diffable, hydrated-by-default.
	if err := os.WriteFile(filepath.Join(dir, planMDFile), []byte(in.Raw), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", planMDFile, err)
	}

	// annotations.json — the searchable badge data (Result).
	annotations, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal annotations: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, annotationsFile), annotations, 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", annotationsFile, err)
	}

	// meta.json — plain git descriptor, written under the flock with a
	// READ-MERGE so a re-save never resets lifecycle. Re-running `ox plan` on
	// the same dated-slug dir (e.g. the hook draft, then the skill's full save)
	// must preserve a manually-set Status, the system-reconciled SessionOutcome,
	// and the original CreatedAt, while refreshing the rest. Routing through
	// MutatePlanMeta also serializes against a concurrent session-stop/doctor
	// outcome write to the same file.
	if err := MutatePlanMeta(context.Background(), dir, func(existing *Meta) (*Meta, error) {
		merged := meta
		if existing != nil {
			if existing.Status != "" {
				merged.Status = existing.Status
			}
			if !existing.CreatedAt.IsZero() {
				merged.CreatedAt = existing.CreatedAt
			}
			// SessionID and SessionOutcome are system-managed (backfilled /
			// reconciled at stop), never set by a save-time caller — preserve
			// them across a re-save even when the caller supplies fresh
			// provenance for the other (snapshot) fields.
			if existing.Provenance != nil {
				if existing.Provenance.SessionID != "" || existing.Provenance.SessionOutcome != "" {
					if merged.Provenance == nil {
						merged.Provenance = &Provenance{}
					}
					if existing.Provenance.SessionID != "" {
						merged.Provenance.SessionID = existing.Provenance.SessionID
					}
					if existing.Provenance.SessionOutcome != "" {
						merged.Provenance.SessionOutcome = existing.Provenance.SessionOutcome
					}
				}
			}
		}
		if merged.Status == "" {
			merged.Status = PlanStatusDraft
		}
		merged.SchemaVersion = SchemaVersion
		return &merged, nil
	}); err != nil {
		return "", fmt.Errorf("write %s: %w", planMetaFile, err)
	}

	// plan.html — only when a render was already produced. Size-gated: small
	// renders stay plain so dehydrated clones read them directly; large ones
	// become LFS pointers (pure-Go pointer write, never the git-lfs binary).
	if html != nil {
		htmlPath := filepath.Join(dir, planHTMLFile)
		if int64(len(html)) > htmlLFSThreshold {
			ref := lfs.NewFileRef(html)
			if err := lfs.WritePointerFile(htmlPath, ref); err != nil {
				return "", fmt.Errorf("write plan.html LFS pointer: %w", err)
			}
		} else if err := os.WriteFile(htmlPath, html, 0o644); err != nil {
			return "", fmt.Errorf("write %s: %w", planHTMLFile, err)
		}
	}

	return dir, nil
}

// List enumerates captured plans under <ledger>/data/plans/, parsing each
// meta.json. Fail-open: an unconfigured or missing ledger yields an empty
// slice (not an error), matching the detectors' fail-open contract. Results
// are sorted newest-first by CreatedAt.
func List(gitRoot string) ([]PlanInfo, error) {
	ledger := ledgerPathFor(gitRoot)
	plansDir := paths.LedgerPlansDir(ledger)
	if plansDir == "" {
		return nil, nil
	}

	entries, err := os.ReadDir(plansDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read plans dir=%s: %w", plansDir, err)
	}

	var infos []PlanInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(plansDir, entry.Name())
		meta, err := readMeta(dir)
		if err != nil {
			continue // skip malformed/partial plan dirs (fail-open)
		}
		infos = append(infos, planInfoFrom(dir, entry.Name(), meta))
	}

	sort.Slice(infos, func(i, j int) bool {
		return infos[i].CreatedAt.After(infos[j].CreatedAt)
	})
	return infos, nil
}

// Load reads a captured plan by slug. The slug matches either the meta.json
// slug or the directory's trailing slug segment (the YYYY-MM-DD- prefix is
// optional in the lookup). Returns the raw plan markdown, the stored Result,
// and the listing info.
func Load(gitRoot, slug string) (string, Result, PlanInfo, error) {
	var res Result
	ledger := ledgerPathFor(gitRoot)
	plansDir := paths.LedgerPlansDir(ledger)
	if plansDir == "" {
		return "", res, PlanInfo{}, fmt.Errorf("no ledger configured for %q", gitRoot)
	}

	dir, name, err := resolvePlanDir(plansDir, slug)
	if err != nil {
		return "", res, PlanInfo{}, err
	}

	meta, err := readMeta(dir)
	if err != nil {
		return "", res, PlanInfo{}, fmt.Errorf("read plan meta for %q: %w", slug, err)
	}

	mdBytes, err := os.ReadFile(filepath.Join(dir, planMDFile))
	if err != nil {
		return "", res, PlanInfo{}, fmt.Errorf("read %s for %q: %w", planMDFile, slug, err)
	}

	// annotations.json is the canonical Result; a missing/partial file is
	// non-fatal — the markdown is the primary artifact.
	if annBytes, readErr := os.ReadFile(filepath.Join(dir, annotationsFile)); readErr == nil {
		_ = json.Unmarshal(annBytes, &res)
	}

	return string(mdBytes), res, planInfoFrom(dir, name, meta), nil
}

// PlanHTMLPath returns the absolute path to a captured plan's plan.html, the
// referenced FileRef when it is an LFS pointer, and whether the file exists.
// The view path uses this to decide between opening a plain HTML file and
// hydrating a pointer first.
func PlanHTMLPath(dir string) (path string, ref lfs.FileRef, isPointer, exists bool) {
	path = filepath.Join(dir, planHTMLFile)
	if _, err := os.Stat(path); err != nil {
		return path, lfs.FileRef{}, false, false
	}
	if lfs.IsPointerFile(path) {
		if r, err := lfs.ReadPointerFile(path); err == nil {
			return path, r, true, true
		}
		return path, lfs.FileRef{}, true, true
	}
	return path, lfs.FileRef{}, false, true
}

// MutatePlanMeta runs an exclusive read-modify-write under an advisory flock on
// a plan's meta.json — the direct mirror of lfs.MutateSessionMeta. Every write
// to meta.json AFTER the initial Save (status changes, session-outcome
// reconciliation) MUST go through this so a re-save and a concurrent
// session-stop/doctor write serialize at the filesystem level instead of
// clobbering each other.
//
// The mutator receives the on-disk Meta (nil if the file is missing) and
// returns the Meta to write, or nil to leave the file untouched (an "only if
// exists" guard). Returning an error aborts the write.
func MutatePlanMeta(ctx context.Context, planDir string, mutate func(*Meta) (*Meta, error)) error {
	metaPath := filepath.Join(planDir, planMetaFile)
	return fileutil.WithFileLock(ctx, metaPath, func() error {
		var arg *Meta
		if data, err := os.ReadFile(metaPath); err == nil {
			var m Meta
			if jerr := json.Unmarshal(data, &m); jerr != nil {
				return fmt.Errorf("parse plan meta=%s: %w", planDir, jerr)
			}
			arg = &m
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("read plan meta=%s: %w", planDir, err)
		}

		out, mErr := mutate(arg)
		if mErr != nil {
			return mErr
		}
		if out == nil {
			return nil // mutator chose not to write
		}

		data, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal plan meta: %w", err)
		}
		return fileutil.AtomicWriteBytes(metaPath, data, 0o644)
	})
}

// SetStatus updates a saved plan's lifecycle status (by slug) under the meta
// flock. No-op if the plan dir has no meta.json. Use the PlanStatus* constants.
func SetStatus(gitRoot, slug string, status PlanStatus) error {
	dir, err := resolvePlanDirForSlug(gitRoot, slug)
	if err != nil {
		return err
	}
	return MutatePlanMeta(context.Background(), dir, func(m *Meta) (*Meta, error) {
		if m == nil {
			return nil, nil
		}
		m.Status = status
		return m, nil
	})
}

// SetSessionOutcome reconciles the producing session's lifecycle onto the plan
// (by slug) under the meta flock — written only by session-stop / `ox doctor`,
// never by Save. Use the SessionOutcome* constants. No-op if no meta.json.
func SetSessionOutcome(gitRoot, slug, outcome string) error {
	dir, err := resolvePlanDirForSlug(gitRoot, slug)
	if err != nil {
		return err
	}
	return MutatePlanMeta(context.Background(), dir, func(m *Meta) (*Meta, error) {
		if m == nil {
			return nil, nil
		}
		if m.Provenance == nil {
			m.Provenance = &Provenance{}
		}
		m.Provenance.SessionOutcome = outcome
		return m, nil
	})
}

// ReconcileSessionOutcome backfills the producing session's canonical id and
// final outcome onto a plan (by slug) at session-stop, where both are known.
// sessionID is the ses_<UUIDv7> minted at stop (skipped when ""); outcome is a
// SessionOutcome* constant. Single flocked write so it can't race a re-save.
func ReconcileSessionOutcome(gitRoot, slug, sessionID, outcome string) error {
	dir, err := resolvePlanDirForSlug(gitRoot, slug)
	if err != nil {
		return err
	}
	return MutatePlanMeta(context.Background(), dir, func(m *Meta) (*Meta, error) {
		if m == nil {
			return nil, nil
		}
		if m.Provenance == nil {
			m.Provenance = &Provenance{}
		}
		if sessionID != "" {
			m.Provenance.SessionID = sessionID
		}
		m.Provenance.SessionOutcome = outcome
		return m, nil
	})
}

// ReadPlanMeta returns the stored Meta for a saved plan (by slug), including
// provenance, collaboration signals, and status. Used by the view path to
// surface the link without re-deriving it.
func ReadPlanMeta(gitRoot, slug string) (Meta, error) {
	dir, err := resolvePlanDirForSlug(gitRoot, slug)
	if err != nil {
		return Meta{}, err
	}
	return readMeta(dir)
}

// resolvePlanDirForSlug resolves a slug to its plan directory under the ledger.
func resolvePlanDirForSlug(gitRoot, slug string) (string, error) {
	plansDir := paths.LedgerPlansDir(ledgerPathFor(gitRoot))
	if plansDir == "" {
		return "", fmt.Errorf("no ledger configured for %q", gitRoot)
	}
	dir, _, err := resolvePlanDir(plansDir, slug)
	return dir, err
}

// --- helpers ---

// slugWordRe splits on any run of non-alphanumeric characters.
var slugWordRe = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify derives a 2-4 word kebab-case slug from a topic/title. Lowercases,
// strips punctuation, and keeps the first 2-4 meaningful words. An empty or
// punctuation-only input yields "untitled-plan" so a directory name is always
// well-formed.
func Slugify(topic string) string {
	words := slugWordRe.Split(strings.ToLower(strings.TrimSpace(topic)), -1)

	var kept []string
	for _, w := range words {
		if w == "" {
			continue
		}
		kept = append(kept, w)
		if len(kept) == 4 {
			break
		}
	}

	if len(kept) == 0 {
		return "untitled-plan"
	}
	return strings.Join(kept, "-")
}

// ledgerResolver maps a producing-repo git root to its ledger checkout root.
// It is a package var so tests can point Save/List/Load at a temp ledger
// without standing up a full ProjectContext. Production code uses the default
// (canonical ProjectContext) resolver.
var ledgerResolver = defaultLedgerResolver

// defaultLedgerResolver resolves the ledger checkout root for the producing
// repo via the canonical ProjectContext helper. Returns "" when uninitialized
// so callers can fail-open. Unlike collision.go's resolveLedgerPath, this does
// NOT require the directory to already exist — Save must be able to create the
// plans dir on first capture.
func defaultLedgerResolver(gitRoot string) string {
	if gitRoot == "" {
		return ""
	}
	ctx, err := config.LoadProjectContext(gitRoot)
	if err != nil || ctx == nil {
		return ""
	}
	return ctx.DefaultLedgerPath()
}

func ledgerPathFor(gitRoot string) string {
	return ledgerResolver(gitRoot)
}

func readMeta(dir string) (Meta, error) {
	var meta Meta
	data, err := os.ReadFile(filepath.Join(dir, planMetaFile))
	if err != nil {
		return meta, err
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return meta, fmt.Errorf("parse plan meta=%s: %w", dir, err)
	}
	return meta, nil
}

func planInfoFrom(dir, dirName string, meta Meta) PlanInfo {
	slug := meta.Slug
	if slug == "" {
		slug = slugFromDirName(dirName)
	}
	_, _, _, hasHTML := PlanHTMLPath(dir)
	return PlanInfo{
		Slug:      slug,
		Topic:     meta.Topic,
		Dir:       dir,
		CreatedAt: meta.CreatedAt,
		Authors:   meta.Authors,
		HasHTML:   hasHTML,
	}
}

// resolvePlanDir finds the plan directory matching slug. It accepts an exact
// directory name, a meta.json slug, or the trailing slug segment of a dated
// directory name. Returns the absolute dir and its base name.
func resolvePlanDir(plansDir, slug string) (string, string, error) {
	// exact directory match (e.g. "2026-06-03-my-plan")
	exact := filepath.Join(plansDir, slug)
	if info, err := os.Stat(exact); err == nil && info.IsDir() {
		return exact, slug, nil
	}

	entries, err := os.ReadDir(plansDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", fmt.Errorf("plan %q not found: no plans saved", slug)
		}
		return "", "", fmt.Errorf("read plans dir=%s: %w", plansDir, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		dir := filepath.Join(plansDir, name)
		if slugFromDirName(name) == slug {
			return dir, name, nil
		}
		if meta, err := readMeta(dir); err == nil && meta.Slug == slug {
			return dir, name, nil
		}
	}
	return "", "", fmt.Errorf("plan %q not found", slug)
}

// datedDirRe matches the YYYY-MM-DD- prefix of a plan directory name.
var datedDirRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-`)

// slugFromDirName strips the optional YYYY-MM-DD- prefix from a directory name.
func slugFromDirName(name string) string {
	return datedDirRe.ReplaceAllString(name, "")
}
