package tokenstrip

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ledgerSessionsDir returns the path to a session ledger's sessions/
// directory, resolved from the OX_TOKENSTRIP_AB_LEDGER env var. Empty
// return = skip the A/B measurement.
//
// Env var, not hardcoded XDG, for two reasons:
//
//  1. Package boundary. pkg/tokenstrip is a public package; it can't
//     import internal/config (where the canonical DefaultLedgerPath
//     helper lives). Hardcoding XDG defaults papers over that and
//     would silently skip on valid setups with non-default XDG config.
//
//  2. Intent. The A/B measurement is diagnostic, run by someone who
//     explicitly wants to sweep their ledger for tokenstrip regressions.
//     It's not a CI-required test; requiring an explicit env var makes
//     that intent portable (no assumption the test machine is a
//     particular developer's laptop).
//
// Set OX_TOKENSTRIP_AB_LEDGER to a directory containing <session>/raw.jsonl
// subdirectories — i.e., the `sessions/` directory under a ledger checkout.
// In non-test code that needs the same path, use:
//
//	filepath.Join(config.DefaultLedgerPath(repoID, endpointURL), "sessions")
func ledgerSessionsDir() string {
	return strings.TrimSpace(os.Getenv("OX_TOKENSTRIP_AB_LEDGER"))
}

// TestABCorpus_TokenStripReductionAndSacredRules is the A/B measurement
// the ox-dvvm bead called for before flipping tokenstrip default-on.
// Walks the ledger's session raw.jsonl files, runs each through
// Compress, and for every session asserts:
//
//  1. Token estimate went DOWN (or stayed flat for already-minimal sessions).
//     Negative reduction would indicate a bug.
//  2. Sacred rules held: user turns unmodified, tool_name/tool_input
//     bytes unmodified, header unmodified.
//  3. EntriesIn == EntriesOut (tokenstrip never drops entries — it
//     mutates content within assistant entries only).
//
// Skipped when the ledger isn't present (contributor without this
// machine's local ledger) — the test communicates with a Skip log line
// rather than silently succeeding.
func TestABCorpus_TokenStripReductionAndSacredRules(t *testing.T) {
	sessionsDir := ledgerSessionsDir()
	if sessionsDir == "" {
		t.Skip("A/B measurement skipped: set OX_TOKENSTRIP_AB_LEDGER to a sessions/ directory to enable")
	}
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		t.Skipf("sessions dir not present at %s (from OX_TOKENSTRIP_AB_LEDGER): %v", sessionsDir, err)
	}

	var measured, skipped int
	var totalIn, totalOut int64
	var tokensIn, tokensOut int64

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rawPath := filepath.Join(sessionsDir, e.Name(), "raw.jsonl")
		info, err := os.Stat(rawPath)
		if err != nil || info.Size() == 0 {
			continue
		}
		// LFS pointer (not hydrated) — skip; we'd be measuring ~140 bytes
		// of pointer metadata instead of the real session.
		if isLFSPointer(rawPath) {
			skipped++
			continue
		}

		data, err := os.ReadFile(rawPath)
		if err != nil {
			continue
		}
		measured++
		totalIn += int64(len(data))

		// Snapshot user turns + tool fields + header from the input for
		// the sacred-rules post-check.
		inputSnapshot := extractSacredLines(data)

		var out bytes.Buffer
		stats, err := Compress(bytes.NewReader(data), &out)
		if err != nil {
			t.Errorf("session %s: Compress failed: %v", e.Name(), err)
			continue
		}

		totalOut += stats.BytesOut
		tokensIn += stats.TokensInEstimate
		tokensOut += stats.TokensOutEstimate

		// Invariant 1: tokens shouldn't increase.
		if stats.TokensOutEstimate > stats.TokensInEstimate {
			t.Errorf("session %s: token count went UP (in=%d out=%d); tokenstrip must never inflate",
				e.Name(), stats.TokensInEstimate, stats.TokensOutEstimate)
		}

		// Invariant 2: no entries lost.
		if stats.EntriesOut != stats.EntriesIn {
			t.Errorf("session %s: entry count changed (in=%d out=%d); tokenstrip must preserve every entry",
				e.Name(), stats.EntriesIn, stats.EntriesOut)
		}

		// Invariant 3: sacred fields unchanged.
		outputSnapshot := extractSacredLines(out.Bytes())
		checkSacredLines(t, e.Name(), inputSnapshot, outputSnapshot)
	}

	if measured == 0 {
		t.Skipf("no hydrated sessions in %s (skipped %d LFS pointers); A/B measurement skipped",
			sessionsDir, skipped)
	}

	// Report aggregate reduction — useful signal even if every session
	// passed the assertions individually. A corpus-wide reduction of
	// 0% would suggest the tokenstrip stage is a no-op on real data,
	// which would merit investigation.
	bytesReduction := float64(totalIn-totalOut) / float64(totalIn) * 100
	tokReduction := 0.0
	if tokensIn > 0 {
		tokReduction = float64(tokensIn-tokensOut) / float64(tokensIn) * 100
	}
	t.Logf("A/B measurement over %d sessions (%d LFS pointers skipped):", measured, skipped)
	t.Logf("  bytes:  in=%d out=%d reduction=%.2f%%", totalIn, totalOut, bytesReduction)
	t.Logf("  tokens: in=%d out=%d reduction=%.2f%% (heuristic estimate)",
		tokensIn, tokensOut, tokReduction)

	// Loose floor — tokenstrip should produce SOME reduction across a
	// real corpus. Zero reduction would indicate the stage isn't doing
	// anything (broken regex, disabled transforms, etc.). The 0.01%
	// threshold below is deliberately far below anything realistic —
	// it only catches the "completely broken" case where the pipeline
	// produces byte-identical output. Historical measurement on this
	// repo's ledger: ~0.11% reduction.
	const minReductionPct = 0.01
	if tokReduction < minReductionPct && bytesReduction < minReductionPct {
		t.Errorf("A/B reduction is effectively zero over %d sessions (threshold %.2f%%); tokenstrip may not be running",
			measured, minReductionPct)
	}
}

// sacredLines captures the raw-byte form of entries we MUST NOT modify
// so the post-check is a byte-identity comparison, not a decode-and-
// recompare. A decoded-content comparison misses:
//   - JSON escape-form changes (e.g., & vs & in identical semantics)
//   - Unknown top-level fields getting stripped or added during re-marshal
//   - Ordering / whitespace differences from encoding/json's defaults
//   - Any other lossy round-trip that happens to preserve the fields we
//     chose to decode while mutating bytes we didn't inspect
//
// Raw-line comparison is the contract tokenstrip advertises for user
// turns, tool metadata, and header entries: bytes in = bytes out. Any
// deviation — even semantically-equivalent — violates the guarantee and
// should fail the test.
type sacredLines struct {
	// indexed by line number in the original stream; value is raw line bytes
	// OR empty string if the line isn't sacred for its type (e.g. assistant).
	byIndex []string
}

func extractSacredLines(data []byte) sacredLines {
	var out sacredLines
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) == 0 {
			out.byIndex = append(out.byIndex, "")
			continue
		}
		var hdr struct {
			Type string `json:"type"`
		}
		// Default: preserve raw bytes of unparseable lines (they should
		// pass through byte-for-byte).
		rawLine := string(line)
		if err := json.Unmarshal(line, &hdr); err == nil {
			switch hdr.Type {
			case "user", "header", "tool":
				// Sacred — store raw bytes for byte-identity check.
				out.byIndex = append(out.byIndex, rawLine)
				continue
			default:
				// Assistant / tool_mark / other — mutation permitted.
				out.byIndex = append(out.byIndex, "")
				continue
			}
		}
		// Unparseable — tokenstrip passes these through unchanged; treat
		// as sacred for byte-identity.
		out.byIndex = append(out.byIndex, rawLine)
	}
	return out
}

func checkSacredLines(t *testing.T, sessionName string, in, out sacredLines) {
	t.Helper()
	if len(in.byIndex) != len(out.byIndex) {
		t.Errorf("session %s: line count changed (in=%d out=%d)",
			sessionName, len(in.byIndex), len(out.byIndex))
		return
	}
	for i, want := range in.byIndex {
		if want == "" {
			continue // non-sacred line; mutation permitted
		}
		got := out.byIndex[i]
		if got != want {
			t.Errorf("session %s: sacred line #%d mutated (byte identity required)\n  in:  %q\n  out: %q",
				sessionName, i, truncateForErr(want, 120), truncateForErr(got, 120))
			return
		}
	}
}

func truncateForErr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func isLFSPointer(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var head [50]byte
	n, _ := f.Read(head[:])
	return strings.HasPrefix(string(head[:n]), "version https://git-lfs.github.com")
}
