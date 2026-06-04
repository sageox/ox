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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/config"
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

// Meta is the git-tracked descriptor written as meta.json alongside a captured
// plan. It carries the searchable, hydration-free facts about the plan: who
// authored it, when, and where it came from.
type Meta struct {
	// SchemaVersion stamps the meta.json shape (set to SchemaVersion on write)
	// so a future reader can detect and migrate an older layout.
	SchemaVersion  string    `json:"schema_version,omitempty"`
	Topic          string    `json:"topic"`
	Slug           string    `json:"slug"`
	Authors        []string  `json:"authors,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	SourcePlanPath string    `json:"source_plan_path,omitempty"`
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

	// meta.json — plain git descriptor.
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal plan meta: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, planMetaFile), metaBytes, 0o644); err != nil {
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
