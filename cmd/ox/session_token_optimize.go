package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sageox/ox/pkg/tokenopt"
	"github.com/spf13/cobra"
)

var sessionTokenOptimizeCmd = &cobra.Command{
	Use:   "token-optimize <session-name>",
	Short: "Compress a session raw.jsonl for summarization (streaming, deterministic)",
	Long: `Read a session's raw.jsonl, apply deterministic streaming transforms
(strip ANSI, collapse progress bars, elide base64 images, truncate large Read
bodies, dedupe system-reminders and repeated tool results), and emit the
compressed jsonl.

Default: writes compressed stream to stdout, stats to stderr.
Use --out to write to a file instead.
Use --stats to include detailed per-transform counts on stderr.

The original raw.jsonl is never modified — this is a read-only transform.`,
	Args: cobra.ExactArgs(1),
	RunE: runSessionTokenOptimize,
}

func init() {
	sessionTokenOptimizeCmd.Flags().StringP("out", "o", "", "write compressed jsonl to this file (default: stdout)")
	sessionTokenOptimizeCmd.Flags().Bool("stats", false, "emit detailed per-transform counts on stderr")
	sessionTokenOptimizeCmd.Flags().String("mode", "conversation", "compression mode: conversation (default, keeps user+assistant+tool_marks) or lossless (content-preserving)")
	sessionCmd.AddCommand(sessionTokenOptimizeCmd)
}

func runSessionTokenOptimize(cmd *cobra.Command, args []string) error {
	sessionName := args[0]

	rawPath, err := resolveRawJSONL(sessionName)
	if err != nil {
		return err
	}

	in, err := os.Open(rawPath)
	if err != nil {
		return fmt.Errorf("open raw.jsonl: %w", err)
	}
	defer in.Close()

	// Note: pointer-file guard removed. resolveRawJSONL routes through
	// openSessionContent, which guarantees rawPath is real content
	// (cache or in-place full content), never a pointer stub.

	var out io.Writer = os.Stdout
	outPath, _ := cmd.Flags().GetString("out")
	if outPath != "" {
		// Refuse to let --out clobber the source raw.jsonl. os.Create truncates
		// the target before the compressor finishes reading, which would yield
		// an empty/corrupt file and destroy the session.
		if rawAbs, err1 := filepath.Abs(rawPath); err1 == nil {
			if outAbs, err2 := filepath.Abs(outPath); err2 == nil && rawAbs == outAbs {
				return fmt.Errorf("--out must differ from the session raw.jsonl path (%s)", rawPath)
			}
		}
		f, err := os.Create(outPath)
		if err != nil {
			return fmt.Errorf("create output: %w", err)
		}
		defer f.Close()
		out = f
	}

	modeStr, _ := cmd.Flags().GetString("mode")
	var mode tokenopt.Mode
	switch modeStr {
	case "conversation":
		mode = tokenopt.ModeConversationOnly
	case "lossless":
		mode = tokenopt.ModeLossless
	default:
		return fmt.Errorf("invalid --mode %q (expected conversation or lossless)", modeStr)
	}

	stats, err := tokenopt.CompressWith(in, out, tokenopt.Options{Mode: mode})
	if err != nil {
		return fmt.Errorf("compress: %w", err)
	}

	wantStats, _ := cmd.Flags().GetBool("stats")
	saved, pct := stats.Reduction()
	if wantStats {
		fmt.Fprintf(os.Stderr, "session=%s mode=%s entries_in=%d entries_out=%d bytes_in=%d bytes_out=%d saved=%d reduction_pct=%.1f tools_marked=%d system_dropped=%d ansi_stripped=%d progress_collapsed=%d images_elided=%d large_reads_elided=%d reminders_deduped=%d tool_results_refd=%d\n",
			sessionName, modeStr, stats.EntriesIn, stats.EntriesOut, stats.BytesIn, stats.BytesOut, saved, pct,
			stats.ToolsMarked, stats.SystemDropped,
			stats.ANSIStripped, stats.ProgressCollapsed, stats.ImagesElided,
			stats.LargeReadsElided, stats.RemindersDeduped, stats.ToolResultsRefd)
	} else {
		fmt.Fprintf(os.Stderr, "token_optimize: mode=%s %d→%d entries, %d→%d bytes (%.1f%% saved)\n",
			modeStr, stats.EntriesIn, stats.EntriesOut, stats.BytesIn, stats.BytesOut, pct)
	}
	return nil
}

// resolveRawJSONL locates a session's hydrated raw.jsonl in the local ledger,
// auto-hydrating into the cache if the in-place file is an LFS pointer.
//
// Cache-only — see openSessionContent for the load-bearing invariant. We must
// not let token-optimize overwrite the in-place LFS pointer with hydrated
// content; the resolver returns the cache path when in-place is a stub.
func resolveRawJSONL(sessionName string) (string, error) {
	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		return "", fmt.Errorf("resolve ledger: %w", err)
	}
	projectRoot, err := requireProjectRoot()
	if err != nil {
		return "", err
	}
	return openSessionContent(projectRoot, ledgerPath, sessionName, "raw.jsonl")
}
