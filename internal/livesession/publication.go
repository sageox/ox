package livesession

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/sageox/ox/internal/secretguard"
)

// Bytes sent to the live API are a publication, so they pass the same secret
// scan as Ledger and LFS publication (internal/lfs prepareUploads,
// internal/gitutil ValidateSessionPublication): the bundled catalog, then the
// rules current upstream adds. Capture-time redaction only knows the bundled
// catalog, so it cannot stand in for this check.
//
// Unlike Ledger publication, a stream never waits on the network for the
// current rules (F2). It scans with the bundled catalog plus the delta from the
// last upstream fetch that succeeded, which the daemon refreshes in the
// background (RefreshSecretRules). A stream publishes on every tool call;
// waiting on raw.githubusercontent.com there meant a network that blocks it
// streamed nothing, and an upstream outage stalled every live session at once.

// scanName is the content name handed to the guard. Path-only rules match it,
// so it is a name no sensitive-file rule names: the one Ledger publication uses.
const scanName = "sessions/content"

// currentRules loads the current upstream catalog; tests replace it.
var currentRules = secretguard.LatestRules

// lastDelta holds the rules the current upstream catalog adds to the bundled
// one, from the last refresh that succeeded. Nil until one has, and in any
// process that never refreshes (a CLI stop): the bundled catalog alone then.
var lastDelta atomic.Pointer[secretguard.RuleSet]

// RefreshSecretRules fetches the current upstream catalog and keeps what it
// adds to the bundled one for every later publication check in this process.
// A failure keeps the last good delta. It waits on the network, so only a
// background loop may call it, never a drain.
func RefreshSecretRules(ctx context.Context) error {
	rules, err := currentRules(ctx)
	if err != nil {
		return fmt.Errorf("refresh secret rules: %w", err)
	}
	if rules == nil || rules.RuleCount() == 0 {
		// a delta of an empty catalog refuses everything as "missing catalog";
		// keeping it would stall every drain behind an error no retry can fix
		return errors.New("refresh secret rules: current catalog has no rules")
	}
	lastDelta.Store(rules.Delta(secretguard.Bundled()))
	return nil
}

// checkPublication refuses content that publication would refuse. It does no
// I/O. A *secretguard.RefusedError means these bytes are refused every time.
func checkPublication(content []byte) error {
	if err := secretguard.ValidateContent(scanName, content); err != nil {
		return fmt.Errorf("live upload refused: %w", err)
	}
	if delta := lastDelta.Load(); delta != nil {
		if err := delta.ValidateContent(scanName, content); err != nil {
			return fmt.Errorf("live upload refused by current rules: %w", err)
		}
	}
	return nil
}

// secretWithheldPrefix opens every gap line withholdRefused writes. The viewer
// keys its "Withheld by SageOx" marker off the reason and the byte count, so
// the line says nothing about what matched.
const secretWithheldPrefix = `{"type":"gap","reason":"secret_withheld",`

// secretWithheldGap stands for n withheld bytes, the newline it keeps not
// counted, as the spool's oversized_line and invalid_line gaps count.
func secretWithheldGap(n int) []byte {
	return fmt.Appendf(nil, secretWithheldPrefix+`"bytes":%d}`+"\n", n)
}

// withheldBatch is a batch the publication scan refused and what replaces it.
type withheldBatch struct {
	size  int64  // bytes of the refused batch
	next  []byte // the batch with its refused lines replaced by gap lines
	lines int    // gap lines in next
	bytes int64  // bytes those gap lines stand for
}

// withholdRefused replaces every line of a refused batch that the scan refuses
// on its own with a secret_withheld gap line; the other lines still stream.
// A match that spans lines refuses no line on its own, so then the whole batch
// becomes one gap. The drain scans the result again before sending any of it,
// so lines refused only together with others are withheld on that pass.
func withholdRefused(batch []byte) (withheldBatch, error) {
	w := withheldBatch{size: int64(len(batch))}
	for line := range bytes.Lines(batch) {
		if checkPublication(line) == nil {
			w.next = append(w.next, line...)
			continue
		}
		if bytes.HasPrefix(line, []byte(secretWithheldPrefix)) {
			// replacing a gap line with another one could go on forever
			return w, errors.New("the secret scan refused a secret_withheld gap line")
		}
		n := len(bytes.TrimSuffix(line, []byte("\n")))
		w.next = append(w.next, secretWithheldGap(n)...)
		w.lines++
		w.bytes += int64(n)
	}
	if w.lines == 0 {
		n := len(bytes.TrimSuffix(batch, []byte("\n")))
		w.next, w.lines, w.bytes = secretWithheldGap(n), 1, int64(n)
	}
	return w, nil
}

// withhold writes s.withheld into the spool and the stream goes on (F1). The
// same bytes are refused every time, and the stream cannot skip them while they
// sit in the spool, so the spool itself changes: gap lines stream in their
// place and declare their size. raw.jsonl keeps them on this machine.
func (s *session) withhold(ctx context.Context) error {
	w := s.withheld
	s.withheld = withheldBatch{}
	if !s.st.SecretWithheld {
		// before the spool changes: a crash in between must still leave stop a
		// reason to tell the person, and an extra notice is harmless
		s.st.SecretWithheld = true
		if err := s.persist(); err != nil {
			return err
		}
	}
	if err := replaceSpoolRange(ctx, s.dir, s.st.Acked, w.size, w.next); err != nil {
		return fmt.Errorf("withhold %d refused bytes at offset %d: %w", w.bytes, s.st.Acked, err)
	}
	// ids and sizes only: the refusal's text and the bytes stay out of every log
	s.log().WarnContext(ctx, "live_session_secret_withheld", "offset", s.st.Acked, "lines", w.lines, "bytes", w.bytes)
	return nil
}

// publishableSummary returns summary if publication would send it, else "".
// A refused summary is dropped, and the session then has no summary: nothing
// on the server writes one.
func (s *session) publishableSummary(ctx context.Context, summary string) string {
	if summary == "" {
		return ""
	}
	if err := checkPublication([]byte(summary)); err != nil {
		s.log().WarnContext(ctx, "live_session_summary_withheld", "error", err)
		return ""
	}
	return summary
}
