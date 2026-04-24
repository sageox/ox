package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/sageox/ox/internal/telemetry"
	"github.com/sageox/ox/pkg/sessionpipeline"
	"github.com/sageox/ox/pkg/tokenopt"
	"github.com/sageox/ox/pkg/tokenstrip"
)

// tokenoptStage adapts pkg/tokenopt to the sessionpipeline.Stage shape.
// First stage in the pipeline: drops system entries, collapses tool
// entries to compact tool_mark markers, keeps user+assistant verbatim.
type tokenoptStage struct{}

func (tokenoptStage) Name() string { return "tokenopt" }

func (tokenoptStage) Apply(ctx context.Context, r io.Reader, w io.Writer) (sessionpipeline.Stats, error) {
	// Context is accepted for interface conformance; tokenopt.Compress is
	// short-lived and I/O-bound, so we don't plumb ctx through today. If a
	// future stage needs cancellation mid-stream, wrap r in a ctx-aware
	// reader at this boundary rather than pushing ctx into pkg/tokenopt.
	_ = ctx
	stats, err := tokenopt.Compress(r, w)
	// tokenopt.Stats already implements slog.LogValuer, so it satisfies
	// sessionpipeline.Stats directly.
	return stats, err
}

// tokenstripStage adapts pkg/tokenstrip to the sessionpipeline.Stage shape.
// Runs AFTER tokenopt: operates on the compressed stream tokenopt produced,
// applying token-aware transforms (NFC normalization, zero-width strip,
// whitespace canonicalization on assistant content; stop-word removal
// + optional synonym substitution strictly inside <thinking> blocks).
//
// Enabled by default; opt-out via OX_SUMMARY_INPUT_TOKEN_STRIP=off. See
// summaryInputTokenStripDisabled() for the gate.
type tokenstripStage struct{}

func (tokenstripStage) Name() string { return "tokenstrip" }

func (tokenstripStage) Apply(ctx context.Context, r io.Reader, w io.Writer) (sessionpipeline.Stats, error) {
	_ = ctx
	stats, err := tokenstrip.Compress(r, w)
	return stats, err
}

// Cache-budget knobs. Exceeding either triggers LRU pruning by mtime.
// Conservative defaults: a healthy long-running install averages <1 MiB
// per optimized session, so 500 MiB = ~500 sessions of headroom with a
// hard file-count backstop.
const (
	summaryInputCacheMaxBytes int64 = 500 * 1024 * 1024
	summaryInputCacheMaxFiles       = 200
)

// summaryInputOptimizeEnvVar is the opt-out switch users (or the
// `--no-optimize` CLI flag) can set to force the summarizer to read raw.jsonl
// directly. Intended as an escape hatch during early rollout; a bug fix that
// ships as a z-release is usually preferable to leaving users stranded.
//
// Values: "off", "0", "false", "no" → disable optimization. Anything else
// (including unset) → enable.
const summaryInputOptimizeEnvVar = "OX_SUMMARY_INPUT_OPTIMIZE"

// summaryInputOptimizeDisabled reports whether the env opt-out is engaged.
func summaryInputOptimizeDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(summaryInputOptimizeEnvVar))) {
	case "off", "0", "false", "no":
		return true
	}
	return false
}

// summaryInputTokenStripEnvVar is the opt-OUT knob for the tokenstrip
// stage (NFC normalization, zero-width stripping, whitespace canon on
// assistant content globally; stop-word + synonym substitution inside
// <thinking> blocks only).
//
// tokenstrip is ENABLED by default — operators who want to disable it
// set OX_SUMMARY_INPUT_TOKEN_STRIP=off. The sacred rules (user turns
// and tool fields untouched; assistant prose outside <thinking> gets
// only lossless transforms) are test-enforced, so shipping default-on
// is a bounded-risk bet: any regression in summary quality can be
// toggled off per operator without a rebuild.
//
// Values: "off", "0", "false", "no" → disable tokenstrip. Anything else
// (including unset) → enable.
const summaryInputTokenStripEnvVar = "OX_SUMMARY_INPUT_TOKEN_STRIP"

// summaryInputTokenStripDisabled reports whether tokenstrip should be
// skipped. Defaults to false (tokenstrip runs) unless the operator sets
// the env var to an opt-out value.
func summaryInputTokenStripDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(summaryInputTokenStripEnvVar))) {
	case "off", "0", "false", "no":
		return true
	}
	return false
}

// writeOptimizedJSONLForSummary compresses raw.jsonl into a summarizer-ready
// stream at <ledger>/.sageox/cache/summary-input/<session>.jsonl. Per
// .claude/rules/ledger-cache.md this is the canonical location for local-only
// derived data: gitignored, per-machine, persists across worktrees, never
// synced or committed. The directory name describes the file's *purpose*
// (input prepared for the session summarizer), not which package produced it.
// See internal/lfs.ContentFiles — summary-input files are deliberately NOT on
// that allowlist, so they never become LFS blobs.
//
// Safety contract — returns "" (and the caller falls back to rawPath) when:
//   - any precondition is missing (rawPath/ledgerPath/sessionName empty)
//   - the OX_SUMMARY_INPUT_OPTIMIZE env opt-out is set to off
//   - compression errors
//   - the compressed file fails the post-compression sanity gate (e.g.
//     zero bytes, zero entries, or drops more entries than SystemDropped
//     would account for — which would indicate a bug silently eating
//     user or assistant turns).
//
// The original raw.jsonl is never modified; this is purely additive.
func writeOptimizedJSONLForSummary(rawPath, ledgerPath, sessionName string) string {
	if rawPath == "" || ledgerPath == "" || sessionName == "" {
		return ""
	}
	if summaryInputOptimizeDisabled() {
		slog.Info("tokenopt: disabled via env, using raw.jsonl", "env", summaryInputOptimizeEnvVar)
		emitTokenoptTelemetry(rawPath, tokenopt.Stats{}, "disabled")
		return ""
	}
	in, err := os.Open(rawPath)
	if err != nil {
		slog.Debug("tokenopt: open raw.jsonl failed", "path", rawPath, "error", err)
		return ""
	}
	defer in.Close()

	cacheDir := filepath.Join(ledgerPath, ".sageox", "cache", "summary-input")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		slog.Debug("tokenopt: mkdir cache dir failed", "path", cacheDir, "error", err)
		return ""
	}
	optPath := filepath.Join(cacheDir, sessionName+".jsonl")

	out, err := os.Create(optPath)
	if err != nil {
		slog.Debug("tokenopt: create optimized file failed", "path", optPath, "error", err)
		emitTokenoptTelemetry(rawPath, tokenopt.Stats{}, "create_failed")
		return ""
	}

	// Pipeline composition. tokenopt always runs first (it drops system
	// entries and collapses tools to compact markers). tokenstrip runs
	// second when enabled — operates on tokenopt's output, applying
	// token-aware transforms. Order matters: stripping stop words inside
	// <thinking> blocks is cheaper AFTER tokenopt has already dropped
	// all the tool output that doesn't have thinking blocks to begin
	// with. Also planned: REDACT.md-driven LLM redactor (ox-xtwf) would
	// slot between these or after tokenstrip depending on whether it
	// needs token-clean input or assistant-only input.
	stages := []sessionpipeline.Stage{tokenoptStage{}}
	if !summaryInputTokenStripDisabled() {
		stages = append(stages, tokenstripStage{})
	}
	stageResults, runErr := sessionpipeline.Run(context.Background(), in, out, stages)
	if cerr := out.Close(); cerr != nil && runErr == nil {
		runErr = cerr
	}

	// Recover tokenopt.Stats from the first stage result for the sanity
	// gate and telemetry. The sanity gate checks invariants produced by
	// tokenopt (EntriesIn - SystemDropped == EntriesOut); subsequent
	// stages don't modify those invariants, so scoring from stage[0] is
	// sufficient.
	var stats tokenopt.Stats
	if len(stageResults) > 0 && stageResults[0].Stats != nil {
		if ts, ok := stageResults[0].Stats.(tokenopt.Stats); ok {
			stats = ts
		}
	}

	if runErr != nil {
		slog.Warn("tokenopt: compress failed, summary will use raw.jsonl", "error", runErr)
		_ = os.Remove(optPath)
		emitTokenoptTelemetry(rawPath, stats, "compress_failed")
		return ""
	}

	// Post-compression sanity gate. Refuse to hand the summarizer a file that
	// looks obviously wrong — the tokenopt library's contract says every non-
	// system entry survives, so enforce that invariant at the use site.
	if reason := sanityCheckOptimized(optPath, stats); reason != "" {
		slog.Warn("tokenopt: sanity gate rejected optimized file, falling back to raw.jsonl",
			"reason", reason, "path", optPath, "stats", stats)
		_ = os.Remove(optPath)
		emitTokenoptTelemetry(rawPath, stats, "sanity_rejected:"+reason)
		return ""
	}

	// Cap unbounded cache growth. LRU-prune by mtime if we exceeded either
	// budget with this new write. Failures are non-fatal — cache bloat is
	// recoverable, but breaking session stop over it would not be.
	if err := pruneSummaryInputCache(cacheDir, summaryInputCacheMaxBytes, summaryInputCacheMaxFiles); err != nil {
		slog.Debug("tokenopt: cache prune failed (non-fatal)", "error", err)
	}

	slog.Info("tokenopt", "path", optPath, "stats", stats)
	emitTokenoptTelemetry(rawPath, stats, "ok")
	_, pct := stats.Reduction()
	fmt.Fprintf(os.Stderr, "session summary input optimized: %d→%d entries, %d→%d bytes (%.1f%% smaller)\n",
		stats.EntriesIn, stats.EntriesOut, stats.BytesIn, stats.BytesOut, pct)
	return optPath
}

// emitTokenoptTelemetry sends a telemetry event summarizing a single tokenopt
// run. Outcome is one of: "ok" | "disabled" | "create_failed" |
// "compress_failed" | "sanity_rejected:<reason>". Aggregation server-side
// gives us compression-rate distribution, error rate, and fallback rate
// across the user base. Best-effort: no-op when telemetry client not available
// (offline, opted-out, etc.).
func emitTokenoptTelemetry(rawPath string, stats tokenopt.Stats, outcome string) {
	if cliCtx == nil || cliCtx.TelemetryClient == nil {
		return
	}
	_, pct := stats.Reduction()
	success := outcome == "ok"
	meta := map[string]string{
		"outcome":            outcome,
		"entries_in":         strconv.Itoa(stats.EntriesIn),
		"entries_out":        strconv.Itoa(stats.EntriesOut),
		"bytes_in":           strconv.FormatInt(stats.BytesIn, 10),
		"bytes_out":          strconv.FormatInt(stats.BytesOut, 10),
		"reduction_pct":      strconv.FormatFloat(pct, 'f', 1, 64),
		"tools_marked":       strconv.Itoa(stats.ToolsMarked),
		"system_dropped":     strconv.Itoa(stats.SystemDropped),
		"ansi_stripped":      strconv.Itoa(stats.ANSIStripped),
		"images_elided":      strconv.Itoa(stats.ImagesElided),
		"large_reads_elided": strconv.Itoa(stats.LargeReadsElided),
		"reminders_deduped":  strconv.Itoa(stats.RemindersDeduped),
		"tool_results_refd":  strconv.Itoa(stats.ToolResultsRefd),
	}
	cliCtx.TelemetryClient.TrackAsync(telemetry.Event{
		Type:     telemetry.EventTokenoptRun,
		Command:  "session_stop",
		Success:  success,
		Metadata: meta,
	})
}

// sanityCheckOptimized returns a non-empty reason string when the compressed
// file looks wrong and should not be trusted. Empty string = pass.
//
// Invariants checked:
//   - file exists and is non-empty
//   - entries were actually emitted (non-zero EntriesOut)
//   - EntriesOut matches EntriesIn - SystemDropped. ConversationOnly drops
//     only system entries; anything else disappearing means a regression is
//     silently eating user or assistant turns.
func sanityCheckOptimized(path string, stats tokenopt.Stats) string {
	if stats.EntriesIn == 0 {
		// Empty input → empty output is legitimate.
		return ""
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Sprintf("stat failed: %v", err)
	}
	if info.Size() == 0 {
		return "compressed file is empty"
	}
	if stats.EntriesOut == 0 {
		return "zero entries emitted from a non-empty input"
	}
	expected := stats.EntriesIn - stats.SystemDropped
	if stats.EntriesOut != expected {
		return fmt.Sprintf("entry-count mismatch: got %d, want %d (EntriesIn=%d - SystemDropped=%d)",
			stats.EntriesOut, expected, stats.EntriesIn, stats.SystemDropped)
	}
	return ""
}

// pruneSummaryInputCache LRU-evicts files from the cache dir until both the
// total-bytes and file-count budgets are satisfied. Oldest-mtime first.
// Only touches files in the given dir; subdirectories are ignored.
func pruneSummaryInputCache(dir string, maxBytes int64, maxFiles int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	type entryInfo struct {
		path  string
		size  int64
		mtime int64
	}
	files := make([]entryInfo, 0, len(entries))
	var totalBytes int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, entryInfo{
			path:  filepath.Join(dir, e.Name()),
			size:  info.Size(),
			mtime: info.ModTime().UnixNano(),
		})
		totalBytes += info.Size()
	}

	remaining := len(files)
	if remaining <= maxFiles && totalBytes <= maxBytes {
		return nil
	}

	// Oldest mtime first.
	sort.Slice(files, func(i, j int) bool { return files[i].mtime < files[j].mtime })

	for _, f := range files {
		if remaining <= maxFiles && totalBytes <= maxBytes {
			break
		}
		if err := os.Remove(f.path); err != nil {
			slog.Debug("tokenopt: cache evict failed (non-fatal)", "path", f.path, "error", err)
			continue
		}
		totalBytes -= f.size
		remaining--
	}
	return nil
}
