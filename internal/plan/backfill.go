package plan

// backfill.go computes and applies the one-off repair pass for plans saved by
// the pre-ox-1tjj.8 planTopic (which read the first H2 heading, not the
// document's H1 — see title.go). It is deliberately split into a read-only
// COMPUTE half (ComputeBackfill, ComputeProducedPlansRewrite — safe to run
// any number of times, used directly by `ox plan backfill-titles --dry-run`)
// and an APPLY half (ApplyBackfillMeta, ApplyProducedPlansRewrite) that
// mutates one plan/session at a time. The cmd/ox layer owns git (mv/add/
// commit/push) and orchestrates: compute -> print -> (if not dry-run) apply
// each -> git mv -> one batch commit.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/planid"
)

// BackfillPlan is one plan directory's before/after retitle+rename, computed
// by ComputeBackfill. It is the reviewable unit `ox plan backfill-titles
// --dry-run` prints and the real run applies unchanged (the dry-run table IS
// the plan, not a preview of a separately-computed one).
type BackfillPlan struct {
	OldDir   string // absolute path, current
	NewDir   string // absolute path after rename; "" when no rename is needed
	OldSlug  string
	NewSlug  string
	OldTopic string
	NewTopic string
	// TopicChanged/SlugChanged are independent: a title can change without its
	// slug changing (rare — two different titles can collide on Slugify), and
	// in that case the plan's content (meta.json/plan.html/events.jsonl) is
	// still corrected even though the directory keeps its name.
	TopicChanged bool
	SlugChanged  bool
	// SkipReason is non-empty when this plan dir could not be read at all
	// (corrupt/missing meta.json or plan.md) — every other field is
	// meaningless in that case. A skip never aborts ComputeBackfill's scan.
	SkipReason string
}

// Changed reports whether this plan needs any write at all.
func (p BackfillPlan) Changed() bool {
	return p.SkipReason == "" && (p.TopicChanged || p.SlugChanged)
}

// ComputeBackfill scans plansDir (a resolved <ledger>/data/plans) and, for
// every plan directory, re-derives its topic/slug from plan.md via Title +
// Slugify — exactly what a fresh post-fix Save would compute — and reports
// whether that differs from what's currently on disk. Read-only: it never
// writes. A single unreadable/malformed plan dir is recorded with SkipReason
// and the scan continues rather than aborting the batch.
func ComputeBackfill(plansDir string) ([]BackfillPlan, error) {
	entries, err := os.ReadDir(plansDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read plans dir=%s: %w", plansDir, err)
	}

	var dirNames []string
	for _, e := range entries {
		if e.IsDir() {
			dirNames = append(dirNames, e.Name())
		}
	}
	// Sorted, deterministic order: collision-suffix assignment (nextAvailableName
	// below) depends on processing order, and a stable sort keeps repeated runs
	// (and dry-run vs the real run) computing the identical table.
	sort.Strings(dirNames)

	// taken seeds with every CURRENTLY-occupied name so a computed target never
	// collides with a plan that isn't being renamed; a plan's own current name
	// is freed the instant we decide to rename it (see below), so a later plan
	// in this same pass may legitimately claim it.
	taken := make(map[string]bool, len(dirNames))
	for _, n := range dirNames {
		taken[n] = true
	}

	out := make([]BackfillPlan, 0, len(dirNames))
	for _, name := range dirNames {
		dir := filepath.Join(plansDir, name)
		meta, merr := readMeta(dir)
		if merr != nil {
			slog.Warn("plan backfill: unreadable meta.json, skipping", "dir", dir, "error", merr)
			out = append(out, BackfillPlan{OldDir: dir, SkipReason: fmt.Sprintf("unreadable meta.json: %v", merr)})
			continue
		}
		mdBytes, rerr := os.ReadFile(filepath.Join(dir, planMDFile))
		if rerr != nil {
			slog.Warn("plan backfill: unreadable plan.md, skipping", "dir", dir, "error", rerr)
			out = append(out, BackfillPlan{OldDir: dir, SkipReason: fmt.Sprintf("unreadable plan.md: %v", rerr)})
			continue
		}

		newTopic := Title(Parse(string(mdBytes)))
		newSlug := Slugify(newTopic)
		oldSlug := slugFromDirName(name)
		datePrefix := datedDirRe.FindString(name) // "" for a dir name with no YYYY-MM-DD- prefix (tolerated, not expected)

		bp := BackfillPlan{
			OldDir:       dir,
			OldSlug:      oldSlug,
			NewSlug:      newSlug,
			OldTopic:     meta.Topic,
			NewTopic:     newTopic,
			TopicChanged: newTopic != meta.Topic,
			SlugChanged:  newSlug != oldSlug,
		}

		if bp.SlugChanged {
			delete(taken, name) // this dir is moving; its old name is up for grabs
			finalSlug, finalName := nextAvailableName(taken, datePrefix, newSlug)
			taken[finalName] = true
			bp.NewSlug = finalSlug
			bp.NewDir = filepath.Join(plansDir, finalName)
		}

		out = append(out, bp)
	}
	return out, nil
}

// nextAvailableName finds the first unclaimed "<datePrefix><slug>[-N]" name,
// suffixing -2, -3, ... on collision. Two distinctly-worded plans saved the
// same day CAN legitimately derive the same slug (Slugify truncates at
// slugWordCap), so a collision is a normal, expected case, not corruption.
func nextAvailableName(taken map[string]bool, datePrefix, slug string) (finalSlug, dirName string) {
	candidate := slug
	for n := 2; ; n++ {
		name := datePrefix + candidate
		if !taken[name] {
			return candidate, name
		}
		candidate = fmt.Sprintf("%s-%d", slug, n)
	}
}

// ApplyBackfillMeta rewrites one plan's meta.json (topic+slug only — every
// other field, including provenance and collaboration signals, is left
// exactly as originally recorded, since this is a title repair, not a
// re-save), re-stamps its plan.html <head> tags when a render exists and
// isn't an LFS pointer, and appends a lifecycle event carrying the corrected
// topic. Operates on dir's CURRENT path; the caller renames the directory
// (git mv) afterward so the rename captures this mutated content in the same
// git operation.
func ApplyBackfillMeta(ctx context.Context, dir, newTopic, newSlug string) error {
	if err := MutatePlanMeta(ctx, dir, func(m *Meta) (*Meta, error) {
		if m == nil {
			return nil, fmt.Errorf("no meta.json in %s", dir)
		}
		m.Topic = newTopic
		m.Slug = newSlug
		return m, nil
	}); err != nil {
		return fmt.Errorf("update meta: %w", err)
	}

	planID, err := appendBackfillRevisedEvent(ctx, dir, newTopic)
	if err != nil {
		return fmt.Errorf("append revised event: %w", err)
	}

	htmlPath, _, isPointer, exists := PlanHTMLPath(dir)
	if exists && !isPointer {
		html, rerr := os.ReadFile(htmlPath)
		if rerr != nil {
			return fmt.Errorf("read plan.html: %w", rerr)
		}
		if err := os.WriteFile(htmlPath, StampHTMLMeta(html, planID, newSlug, newTopic), 0o644); err != nil {
			return fmt.Errorf("write plan.html: %w", err)
		}
	}
	return nil
}

// appendBackfillRevisedEvent appends a lifecycle event carrying the corrected
// topic, under the same events.jsonl flock Save/AppendEvent use. It mints a
// fresh pln_ id only for a plan dir with NO prior events at all (a legacy
// plan saved before the event-log foundation shipped) and records that as a
// `created` event, exactly mirroring Save()'s own firstSave/reuse decision —
// so a legacy plan ends up in the same state a fresh post-fix Save would have
// left it in. Returns the resolved (or minted) id for StampHTMLMeta to reuse.
func appendBackfillRevisedEvent(ctx context.Context, dir, newTopic string) (string, error) {
	eventsPath := filepath.Join(dir, eventsFile)
	var planID string
	err := fileutil.WithFileLock(ctx, eventsPath, func() error {
		existing, lerr := LoadEvents(dir)
		if lerr != nil {
			slog.Warn("plan backfill: load existing events failed, minting a fresh id", "error", lerr, "dir", dir)
			existing = nil
		}
		ev := Event{Kind: EventRevised, Topic: newTopic}
		if len(existing) == 0 {
			planID = planid.GeneratePlanID()
			ev.Kind = EventCreated
			ev.Status = PlanStatusDraft
		} else {
			planID = existing[0].PlanID
		}
		ev.PlanID = planID
		return appendEventLocked(eventsPath, ev)
	})
	return planID, err
}

// ProducedPlansRewrite is one sessions/*/meta.json whose produced_plans list
// names at least one old slug being renamed — the stale-reference repair
// reconcileProducedPlansAtStop (cmd/ox/agent_session.go) would otherwise
// silently fail on, since it resolves plans by slug.
type ProducedPlansRewrite struct {
	SessionDir string
	// OldSlugs are the entries in this session's produced_plans that will
	// change — a subset (a session can produce several plans; only the
	// renamed ones are listed here).
	OldSlugs []string
}

// ComputeProducedPlansRewrite scans sessionsDir (<ledger>/sessions) for every
// session whose produced_plans names an old slug present in renamed
// (old slug -> new slug). Read-only. A single unreadable session meta.json is
// logged and skipped, never aborts the scan.
func ComputeProducedPlansRewrite(sessionsDir string, renamed map[string]string) ([]ProducedPlansRewrite, error) {
	if len(renamed) == 0 {
		return nil, nil
	}
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read sessions dir=%s: %w", sessionsDir, err)
	}

	var out []ProducedPlansRewrite
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(sessionsDir, e.Name())
		meta, merr := lfs.ReadSessionMeta(dir)
		if merr != nil {
			// ReadSessionMeta wraps os.ReadFile's *PathError with %w, so the
			// not-exist check must unwrap via errors.Is — os.IsNotExist does NOT
			// see through a wrapped error (verified: it only inspects the
			// error's own concrete type, never calls Unwrap).
			if errors.Is(merr, fs.ErrNotExist) {
				continue // no meta.json (e.g. a stray dir) — nothing to rewrite
			}
			slog.Warn("plan backfill: unreadable session meta.json, skipping", "dir", dir, "error", merr)
			continue
		}
		var stale []string
		for _, slug := range meta.ProducedPlans {
			if _, ok := renamed[slug]; ok {
				stale = append(stale, slug)
			}
		}
		if len(stale) > 0 {
			out = append(out, ProducedPlansRewrite{SessionDir: dir, OldSlugs: stale})
		}
	}
	return out, nil
}

// ApplyProducedPlansRewrite rewrites one session's produced_plans entries
// in place (old slug -> new slug per renamed), under MutateSessionMeta's
// flock so the write serializes against a concurrent session-stop
// reconciliation. No-op (no write) if nothing in this session actually
// matches renamed.
func ApplyProducedPlansRewrite(ctx context.Context, sessionDir string, renamed map[string]string) error {
	return lfs.MutateSessionMeta(ctx, sessionDir, func(m *lfs.SessionMeta) (*lfs.SessionMeta, error) {
		if m == nil {
			return nil, nil
		}
		changed := false
		for i, slug := range m.ProducedPlans {
			if newSlug, ok := renamed[slug]; ok {
				m.ProducedPlans[i] = newSlug
				changed = true
			}
		}
		if !changed {
			return nil, nil
		}
		return m, nil
	})
}
