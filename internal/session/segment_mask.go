package session

import "sort"

// segment_mask.go applies pause/resume lifecycle events to a slice of session
// entries by filtering out entries that fall in any [pause_seq, resume_seq)
// range. See ADR-019 (session entity lifecycle) and ADR-020 (pause/resume).
//
// Pause IS redaction with temporal scope. The text-pattern redactor in
// secrets.go / redact_rules.go scrubs entry CONTENT; the segment mask
// EXCLUDES entries entirely by sequence range. Both run at upload time
// (cmd/ox/session_stop.go) and compose without interference: mask drops
// entries first, then redactor scrubs content of remaining entries.
//
// Seq convention (see RecordingState.EntryCount):
//   - "current EntryCount" = number of entries written so far.
//   - pause LifecycleEvent records seq = EntryCount at the moment of pause.
//     Entry (seq+1) is the FIRST excluded entry.
//   - resume LifecycleEvent records seq = EntryCount at the moment of resume.
//     Entry (seq+1) is the FIRST included entry.
//   - In a 0-indexed slice of post-header raw.jsonl entries, the excluded
//     range is slice indices [pause_seq, resume_seq).

// SegmentRange is a half-open [Start, End) exclusion over 0-indexed entry
// positions in raw.jsonl (excluding the metadata header line).
type SegmentRange struct {
	Start int // inclusive lower bound (== pause seq)
	End   int // exclusive upper bound (== resume seq; math.MaxInt for open-ended)
}

const segmentOpenEnded = int(^uint(0) >> 1) // math.MaxInt without importing math

// BuildSegmentRanges walks the lifecycle timeline and returns the set of
// excluded entry-index ranges. Multiple pause/resume cycles produce multiple
// ranges; an open-ended pause (no matching resume) produces a range that
// extends to the end of stream.
//
// Malformed timelines are tolerated:
//   - resume without prior pause: ignored.
//   - pause directly followed by another pause: the second pause is ignored
//     (the first pause's range stays open until the next resume).
//   - overlapping or duplicate ranges: produced as-is; ApplySegmentMask
//     handles overlaps by unioning during filtering.
func BuildSegmentRanges(lifecycle []LifecycleEvent) []SegmentRange {
	var ranges []SegmentRange
	openStart := -1

	for _, ev := range lifecycle {
		switch ev.Action {
		case LifecycleActionPause:
			if openStart < 0 {
				openStart = ev.Seq
			}
			// nested pause while already paused: keep the outer open
		case LifecycleActionResume:
			if openStart < 0 {
				continue // resume without prior pause: ignore
			}
			ranges = append(ranges, SegmentRange{Start: openStart, End: ev.Seq})
			openStart = -1
		case LifecycleActionStop, LifecycleActionClearFinalize, LifecycleActionExpired:
			// terminal events close any open pause at the action's seq.
			if openStart >= 0 {
				ranges = append(ranges, SegmentRange{Start: openStart, End: ev.Seq})
				openStart = -1
			}
		}
	}

	if openStart >= 0 {
		// open-ended pause (no matching resume, no terminal): exclude to EOS.
		ranges = append(ranges, SegmentRange{Start: openStart, End: segmentOpenEnded})
	}

	return ranges
}

// IsSeqExcluded reports whether the 0-indexed entry position falls in any
// of the exclusion ranges.
func IsSeqExcluded(seq int, ranges []SegmentRange) bool {
	for _, r := range ranges {
		if seq >= r.Start && seq < r.End {
			return true
		}
	}
	return false
}

// ApplySegmentMask returns a new slice containing only entries whose
// 0-indexed position is NOT in any exclusion range. The input slice is
// not modified.
//
// When lifecycle is empty, the returned slice is a shallow copy of
// entries (same underlying data, new slice header).
func ApplySegmentMask(entries []SessionEntry, lifecycle []LifecycleEvent) []SessionEntry {
	ranges := BuildSegmentRanges(lifecycle)
	if len(ranges) == 0 {
		out := make([]SessionEntry, len(entries))
		copy(out, entries)
		return out
	}

	out := make([]SessionEntry, 0, len(entries))
	for i, e := range entries {
		if IsSeqExcluded(i, ranges) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// CountMaskedEntries returns how many entries in `entries` fall inside any
// exclusion range. Useful for telemetry and the upload-time validator.
func CountMaskedEntries(entries []SessionEntry, lifecycle []LifecycleEvent) int {
	return CountMaskedEntriesByTotal(len(entries), lifecycle)
}

// CountMaskedEntriesByTotal returns how many 0-indexed entry positions in
// [0, total) fall inside any exclusion range built from `lifecycle`.
// Identical semantics to CountMaskedEntries (overlaps are unioned, not
// double-counted) but does not require a slice — callers like the upload-
// path validator only know the entry count and shouldn't have to allocate
// len(entries) SessionEntry values just to drive a counting loop.
func CountMaskedEntriesByTotal(total int, lifecycle []LifecycleEvent) int {
	if total <= 0 {
		return 0
	}
	ranges := BuildSegmentRanges(lifecycle)
	if len(ranges) == 0 {
		return 0
	}

	// Project each range onto [0, total) and union overlapping ones so we
	// don't double-count entries covered by two ranges.
	type clipped struct{ start, end int }
	clamped := make([]clipped, 0, len(ranges))
	for _, r := range ranges {
		start := r.Start
		if start < 0 {
			start = 0
		}
		end := r.End
		if end > total {
			end = total
		}
		if end > start {
			clamped = append(clamped, clipped{start, end})
		}
	}
	if len(clamped) == 0 {
		return 0
	}
	// Sort by start so we can sweep + union in one pass.
	sort.Slice(clamped, func(i, j int) bool { return clamped[i].start < clamped[j].start })

	count := 0
	curStart, curEnd := clamped[0].start, clamped[0].end
	for _, c := range clamped[1:] {
		if c.start <= curEnd {
			// overlap or touch — extend the current union
			if c.end > curEnd {
				curEnd = c.end
			}
			continue
		}
		count += curEnd - curStart
		curStart, curEnd = c.start, c.end
	}
	count += curEnd - curStart
	return count
}
