//go:build slow

// distill_history_e2e_fixtures_test.go — low-level fixture stagers, date
// helpers, and the 12 fixture recipes per
// docs/specs/distill-history-read-test-plan.md §3. The harness that wires up
// a per-test workspace is in distill_history_e2e_harness_test.go; the
// sanity test that exercises every recipe end-to-end is in
// distill_history_e2e_test.go.
//
// Every recipe here takes `(t, team, now)` (or `(t, primary, secondary,
// now)` for the multi-team case) and writes files relative to `now`.
// Recipes never call time.Now() — see plan §4.3 flakiness controls.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/facts"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Low-level stagers
// ---------------------------------------------------------------------------

// dailyFileSpec is the input to stageDailyFile. Callers fill in the
// filename (minus the .md extension), the mtime, the list of source
// files for the frontmatter, and the body bytes. The body is wrapped in
// a `# Daily Memory — <date>` heading and a `## Sources` footer to match
// what a real writer would produce (see cmd/ox/distill.go).
type dailyFileSpec struct {
	// FilenameStem is the part of the filename before ".md". For a valid
	// daily this starts with YYYY-MM-DD (e.g. "2026-04-12-019c8a3f").
	FilenameStem string

	// Date is the YYYY-MM-DD prefix the reader will surface on Entry.Date.
	// Must match the filename's prefix for normal files; the
	// MixedTzAndUtcLegacy recipe exercises the case where the encoded
	// UUID7 day differs from this prefix, which is exactly the contract
	// the reader is supposed to honor (spec §5.b trust-prefix).
	Date string

	// Mtime is applied via os.Chtimes after writing. The reader uses
	// mtime as the entry's CreatedAt field, so JR-* ordering cases pass
	// distinct mtimes to observe ordering behavior.
	Mtime time.Time

	// Sources populates the frontmatter sources: list. Each entry is
	// emitted verbatim as a "  - <src>" line.
	Sources []string

	// BodyPrefix is optional extra text to drop between the heading and
	// the Sources section. Recipes use this to differentiate otherwise
	// identical files.
	BodyPrefix string
}

// stageDailyFile writes one daily .md file under teamRoot/memory/daily,
// sets its mtime, and returns a stagedDaily receipt. Internally calls
// buildDailyBody so the content shape stays in one place.
func stageDailyFile(t *testing.T, team teamContext, spec dailyFileSpec) stagedDaily {
	t.Helper()

	rel := filepath.Join("memory", "daily", spec.FilenameStem+".md")
	abs := filepath.Join(team.path, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))

	body := buildDailyBody(spec)
	require.NoError(t, os.WriteFile(abs, []byte(body), 0o644))

	if !spec.Mtime.IsZero() {
		require.NoError(t, os.Chtimes(abs, spec.Mtime, spec.Mtime), "chtimes %s", abs)
	}

	return stagedDaily{
		Path:    abs,
		Rel:     filepath.ToSlash(rel),
		ID:      spec.FilenameStem,
		Date:    spec.Date,
		Team:    team.slug,
		Mtime:   spec.Mtime,
		Sources: spec.Sources,
	}
}

// buildDailyBody renders the full on-disk text for a daily file. Shape:
//
//	---
//	sources:
//	  - <src1>
//	  - <src2>
//	---
//
//	# Daily Memory — <date>
//
//	<optional body prefix>
//
//	## Sources
//
//	1. [Source 1][1]
//
//	[1]: https://example.test/1
//
// The sources footer is emitted only when Sources is non-empty so empty
// recipes can stage blank bodies without triggering a dangling numbered
// list.
func buildDailyBody(spec dailyFileSpec) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("sources:\n")
	for _, src := range spec.Sources {
		fmt.Fprintf(&b, "  - %s\n", src)
	}
	b.WriteString("---\n")
	b.WriteString("\n")
	fmt.Fprintf(&b, "# Daily Memory — %s\n\n", spec.Date)
	if spec.BodyPrefix != "" {
		b.WriteString(spec.BodyPrefix)
		if !strings.HasSuffix(spec.BodyPrefix, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if len(spec.Sources) > 0 {
		b.WriteString("## Sources\n\n")
		for i, src := range spec.Sources {
			fmt.Fprintf(&b, "%d. [%s][%d]\n", i+1, filepath.Base(src), i+1)
		}
		b.WriteString("\n")
		for i, src := range spec.Sources {
			fmt.Fprintf(&b, "[%d]: https://example.test/%s\n", i+1, filepath.Base(src))
		}
	}
	return b.String()
}

// stageFactFile writes a JSONL fact file under one of the fact
// subdirectories using the real internal/facts.WriteFacts so we never
// drift from the on-disk schema. Returns a stagedFact receipt with the
// number of fact body lines written.
func stageFactFile(t *testing.T, team teamContext, factType, relName string, header facts.FileHeader, body []facts.Fact) stagedFact {
	t.Helper()

	rel := filepath.Join("memory", factType, relName)
	abs := filepath.Join(team.path, rel)
	require.NoError(t, facts.WriteFacts(abs, header, body), "write facts %s", rel)

	return stagedFact{
		Path:     abs,
		Rel:      filepath.ToSlash(rel),
		Team:     team.slug,
		FactType: factType,
		Facts:    len(body),
	}
}

// stageRawFile writes an arbitrary byte payload at teamRoot/<rel> and
// returns the absolute path. Used for malformed/bad-frontmatter siblings
// where a valid daily-file shape is exactly what we want to avoid.
func stageRawFile(t *testing.T, team teamContext, rel string, data []byte, mtime time.Time) string {
	t.Helper()
	abs := filepath.Join(team.path, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
	require.NoError(t, os.WriteFile(abs, data, 0o644))
	if !mtime.IsZero() {
		require.NoError(t, os.Chtimes(abs, mtime, mtime))
	}
	return abs
}

// ---------------------------------------------------------------------------
// Date / ID helpers used by recipes
// ---------------------------------------------------------------------------

// dayString returns the UTC YYYY-MM-DD string for t.
func dayString(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

// daysBack returns t shifted back by n UTC days.
func daysBack(t time.Time, n int) time.Time {
	return t.UTC().AddDate(0, 0, -n)
}

// dailyStem assembles a `<date>-<uuid7-prefix>[<extra>]` filename stem.
// `uuid7Suffix` is the 8-hex-char short form used throughout the fixture
// table (the reader treats it as opaque — see list.go:parseEntryFile).
func dailyStem(date, uuid7Prefix, extra string) string {
	stem := date + "-" + uuid7Prefix
	if extra != "" {
		stem = stem + extra
	}
	return stem
}

// defaultFactHeader returns a well-formed v2 fact-file header anchored
// on a specific instant. Used by EmptyMarkerOnly and any recipe that
// needs to reference a stable fact file from a daily-file frontmatter.
func defaultFactHeader(now time.Time) facts.FileHeader {
	return facts.FileHeader{
		Meta: facts.FileMeta{
			SchemaVersion: facts.SchemaVersion,
			SourceType:    facts.SourceGitHub,
			RecordedAt:    now.UTC().Format(time.RFC3339),
		},
	}
}

// ---------------------------------------------------------------------------
// Recipes — 12 per docs/specs/distill-history-read-test-plan.md §3
// ---------------------------------------------------------------------------

// recipeMinimalDaily — one valid daily file dated today(now) with a
// single fact-file source in its frontmatter. mtime = now - 1h. This is
// the baseline every "happy path" case inherits.
func recipeMinimalDaily(t *testing.T, team teamContext, now time.Time) stagedFixture {
	t.Helper()
	today := dayString(now)
	src := filepath.Join("memory", ".github-facts", today+"-019c8a0e-pr-487.jsonl")
	daily := stageDailyFile(t, team, dailyFileSpec{
		FilenameStem: dailyStem(today, "019c8a3f", ""),
		Date:         today,
		Mtime:        now.Add(-1 * time.Hour),
		Sources:      []string{src},
		BodyPrefix:   "Baseline daily entry used by the JR-01 happy path.",
	})
	return stagedFixture{Dailies: []stagedDaily{daily}}
}

// recipeMultiSnapshotSameDay — MinimalDaily plus a second daily for the
// same UTC day with a different UUID7 and a newer mtime. Exercises
// spec §5.c "multiple summary files per day is first-class state."
func recipeMultiSnapshotSameDay(t *testing.T, team teamContext, now time.Time) stagedFixture {
	t.Helper()
	today := dayString(now)

	older := stageDailyFile(t, team, dailyFileSpec{
		FilenameStem: dailyStem(today, "019c8a3f", ""),
		Date:         today,
		Mtime:        now.Add(-4 * time.Hour),
		Sources: []string{
			filepath.Join("memory", ".github-facts", today+"-019c8a0e-pr-487.jsonl"),
		},
		BodyPrefix: "Older snapshot for the day.",
	})
	newer := stageDailyFile(t, team, dailyFileSpec{
		FilenameStem: dailyStem(today, "019c87aa", ""),
		Date:         today,
		Mtime:        now.Add(-2 * time.Hour),
		Sources: []string{
			filepath.Join("memory", ".github-facts", today+"-019c8aff-pr-488.jsonl"),
		},
		BodyPrefix: "Newer snapshot for the day.",
	})
	return stagedFixture{Dailies: []stagedDaily{older, newer}}
}

// recipeThreeDays — three daily files across three consecutive UTC days
// (now-48h, now-24h, now-6h). Window boundary + since + ordering test.
func recipeThreeDays(t *testing.T, team teamContext, now time.Time) stagedFixture {
	t.Helper()

	specs := []struct {
		daysAgo int
		uuid    string
		mtime   time.Time
		prefix  string
	}{
		{2, "019c7000", now.Add(-48 * time.Hour), "Oldest daily, should be out of a 24h window."},
		{1, "019c8000", now.Add(-24 * time.Hour), "Middle daily, in-window for a 24h query."},
		{0, "019c9000", now.Add(-6 * time.Hour), "Newest daily, always in-window."},
	}

	out := make([]stagedDaily, 0, len(specs))
	for _, s := range specs {
		day := dayString(daysBack(now, s.daysAgo))
		d := stageDailyFile(t, team, dailyFileSpec{
			FilenameStem: dailyStem(day, s.uuid, ""),
			Date:         day,
			Mtime:        s.mtime,
			Sources: []string{
				filepath.Join("memory", ".github-facts", day+"-"+s.uuid+"-pr-001.jsonl"),
			},
			BodyPrefix: s.prefix,
		})
		out = append(out, d)
	}
	return stagedFixture{Dailies: out}
}

// recipeMixedTzAndUtcLegacy — one "legacy" daily whose filename prefix
// is yesterday(now) but whose UUID7 is narratively understood to encode
// a today(now) instant, plus one "new" daily on today(now) where the
// prefix and UUID7 agree. The reader is expected to trust the filename
// prefix (spec §5.b item a), which is exactly what JR-13 asserts.
//
// Note: UUID7 is treated as an opaque hex string by the reader — we do
// not compute a real timestamp-encoded UUID7 here because nothing the
// reader does parses the hex. The "narrative" lives in the test matrix.
func recipeMixedTzAndUtcLegacy(t *testing.T, team teamContext, now time.Time) stagedFixture {
	t.Helper()
	today := dayString(now)
	yesterday := dayString(daysBack(now, 1))

	legacy := stageDailyFile(t, team, dailyFileSpec{
		FilenameStem: dailyStem(yesterday, "019c8a3f", "-legacy"),
		Date:         yesterday, // reader trusts the prefix
		Mtime:        now.Add(-20 * time.Hour),
		Sources: []string{
			filepath.Join("memory", ".github-facts", yesterday+"-019c8a0e-pr-487.jsonl"),
		},
		BodyPrefix: "Legacy daily — UUID7 narratively encodes a different UTC day.",
	})
	current := stageDailyFile(t, team, dailyFileSpec{
		FilenameStem: dailyStem(today, "019c8b00", ""),
		Date:         today,
		Mtime:        now.Add(-2 * time.Hour),
		Sources: []string{
			filepath.Join("memory", ".github-facts", today+"-019c8b01-pr-489.jsonl"),
		},
		BodyPrefix: "Modern daily — prefix and UUID7 both UTC.",
	})
	return stagedFixture{Dailies: []stagedDaily{legacy, current}}
}

// recipeMalformedFilename — MinimalDaily plus a sibling file whose name
// does not match the YYYY-MM-DD-<uuid>.md pattern. The reader must warn
// and skip, not crash.
func recipeMalformedFilename(t *testing.T, team teamContext, now time.Time) stagedFixture {
	t.Helper()
	base := recipeMinimalDaily(t, team, now)

	rel := filepath.Join("memory", "daily", "not-a-date.md")
	abs := stageRawFile(t, team, rel, []byte("# not a valid daily file\n"), now.Add(-1*time.Hour))
	base.ExtraFiles = append(base.ExtraFiles, abs)
	return base
}

// recipeEmptyMarkerOnly — no daily files. One fact file with a valid v2
// header and zero fact body lines, staged at a github-facts path. The
// daily reader must not walk into the facts subtree nor surface this
// file as a daily entry.
func recipeEmptyMarkerOnly(t *testing.T, team teamContext, now time.Time) stagedFixture {
	t.Helper()
	today := dayString(now)
	relName := today + "-019c8a0e-pr-487.jsonl"
	f := stageFactFile(t, team, ".github-facts", relName, defaultFactHeader(now), nil)
	return stagedFixture{Facts: []stagedFact{f}}
}

// recipeMultiTeamList stages one daily for the primary team and one
// daily for a secondary team. Callers must have already called
// addSecondaryTeam — the recipe takes both team contexts directly
// rather than reaching into *distillHistoryE2E.
//
// Use case: JR-14 (default = single-team), JR-15 (--all-teams merge),
// JR-16 (--all-teams --format=content).
func recipeMultiTeamList(t *testing.T, primary, secondary teamContext, now time.Time) stagedFixture {
	t.Helper()
	today := dayString(now)

	d1 := stageDailyFile(t, primary, dailyFileSpec{
		FilenameStem: dailyStem(today, "019c8a3f", ""),
		Date:         today,
		Mtime:        now.Add(-3 * time.Hour),
		Sources: []string{
			filepath.Join("memory", ".github-facts", today+"-019c8a0e-pr-487.jsonl"),
		},
		BodyPrefix: "Primary team daily.",
	})
	d2 := stageDailyFile(t, secondary, dailyFileSpec{
		FilenameStem: dailyStem(today, "019c8bcd", ""),
		Date:         today,
		Mtime:        now.Add(-2 * time.Hour),
		Sources: []string{
			filepath.Join("memory", ".github-facts", today+"-019c8cde-pr-501.jsonl"),
		},
		BodyPrefix: "Secondary team daily.",
	})
	return stagedFixture{Dailies: []stagedDaily{d1, d2}}
}

// recipeAmbiguousPrefix — MultiSnapshotSameDay plus a third daily file
// whose UUID7 starts with the same 8-char prefix as one of the existing
// files. A short-id lookup of that prefix must fail with id_ambiguous.
func recipeAmbiguousPrefix(t *testing.T, team teamContext, now time.Time) stagedFixture {
	t.Helper()
	base := recipeMultiSnapshotSameDay(t, team, now)
	today := dayString(now)

	dup := stageDailyFile(t, team, dailyFileSpec{
		FilenameStem: dailyStem(today, "019c8a3f", "-dup"),
		Date:         today,
		Mtime:        now.Add(-1 * time.Hour),
		Sources: []string{
			filepath.Join("memory", ".github-facts", today+"-019c8a0e-pr-487.jsonl"),
		},
		BodyPrefix: "Duplicate short-prefix to force id_ambiguous.",
	})
	base.Dailies = append(base.Dailies, dup)
	return base
}

// recipeBadFrontmatterNeighbor — MinimalDaily plus a sibling .md file
// with broken YAML frontmatter. Used to prove that one malformed
// neighbor does not break `ox distill history show` for a different, well-formed
// ID in the same directory.
func recipeBadFrontmatterNeighbor(t *testing.T, team teamContext, now time.Time) stagedFixture {
	t.Helper()
	base := recipeMinimalDaily(t, team, now)
	today := dayString(now)

	badStem := dailyStem(today, "019cbad0", "")
	rel := filepath.Join("memory", "daily", badStem+".md")
	bad := "---\nsources:\n  - not closed properly\n# no terminating fence\n\n# Daily Memory — " + today + "\n\nBroken neighbor.\n"
	abs := stageRawFile(t, team, rel, []byte(bad), now.Add(-90*time.Minute))
	base.ExtraFiles = append(base.ExtraFiles, abs)
	return base
}

// recipeTwoConsecutiveUtcDays — two valid daily files whose filename
// prefixes are yesterday(now) and today(now). JR-21 uses this to drive
// a `--tz=America/Los_Angeles` window that spans a day boundary when
// converted from LA-local to UTC.
func recipeTwoConsecutiveUtcDays(t *testing.T, team teamContext, now time.Time) stagedFixture {
	t.Helper()
	today := dayString(now)
	yesterday := dayString(daysBack(now, 1))

	d1 := stageDailyFile(t, team, dailyFileSpec{
		FilenameStem: dailyStem(yesterday, "019c7abc", ""),
		Date:         yesterday,
		Mtime:        now.Add(-20 * time.Hour),
		Sources: []string{
			filepath.Join("memory", ".github-facts", yesterday+"-019c7abc-pr-100.jsonl"),
		},
		BodyPrefix: "Yesterday UTC entry.",
	})
	d2 := stageDailyFile(t, team, dailyFileSpec{
		FilenameStem: dailyStem(today, "019c8def", ""),
		Date:         today,
		Mtime:        now.Add(-2 * time.Hour),
		Sources: []string{
			filepath.Join("memory", ".github-facts", today+"-019c8def-pr-101.jsonl"),
		},
		BodyPrefix: "Today UTC entry.",
	})
	return stagedFixture{Dailies: []stagedDaily{d1, d2}}
}

// recipeStaleFilenameRecentMtime — the single most important semantic
// lock-in for §2.1 "Event day, not write time." One daily file whose
// filename prefix is 90 days ago but whose mtime is 1 hour ago. The
// reader must exclude it from a 24 h window.
func recipeStaleFilenameRecentMtime(t *testing.T, team teamContext, now time.Time) stagedFixture {
	t.Helper()
	staleDay := dayString(daysBack(now, 90))
	d := stageDailyFile(t, team, dailyFileSpec{
		FilenameStem: dailyStem(staleDay, "019c7000", ""),
		Date:         staleDay,
		Mtime:        now.Add(-1 * time.Hour),
		Sources: []string{
			filepath.Join("memory", ".github-facts", staleDay+"-019c7000-pr-001.jsonl"),
		},
		BodyPrefix: "Filename prefix is 90 days ago; mtime is fresh. Must be filtered by prefix.",
	})
	return stagedFixture{Dailies: []stagedDaily{d}}
}

// recipeYesterdayOnly — one valid daily file whose filename prefix is
// yesterday(now) UTC. JR-02 uses this to exercise the day-rounded
// empty-window case: a 1h window anchored at today must not include
// yesterday's entry.
func recipeYesterdayOnly(t *testing.T, team teamContext, now time.Time) stagedFixture {
	t.Helper()
	yesterday := dayString(daysBack(now, 1))
	d := stageDailyFile(t, team, dailyFileSpec{
		FilenameStem: dailyStem(yesterday, "019c7abc", ""),
		Date:         yesterday,
		Mtime:        now.Add(-23 * time.Hour),
		Sources: []string{
			filepath.Join("memory", ".github-facts", yesterday+"-019c7abc-pr-100.jsonl"),
		},
		BodyPrefix: "Yesterday-only entry for the empty-window case.",
	})
	return stagedFixture{Dailies: []stagedDaily{d}}
}
