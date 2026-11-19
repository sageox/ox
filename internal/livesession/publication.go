package livesession

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/sageox/ox/internal/secretguard"
)

// Bytes sent to the live API are a publication, so they pass the same secret
// scan as Ledger and LFS publication (internal/lfs prepareUploads,
// internal/gitutil ValidateSessionPublication): the bundled catalog, then the
// rules current upstream adds, refusing on any finding and waiting when the
// current rules cannot be loaded. Capture-time redaction only knows the bundled
// catalog, so it cannot stand in for this check.

// scanName is the content name handed to the guard. Path-only rules match it,
// so it is a name no sensitive-file rule names: the one Ledger publication uses.
const scanName = "sessions/content"

// secretRefusedReason is DisabledReason when the scan withheld a batch.
const secretRefusedReason = "secret_refused"

// currentRulesMaxAge bounds how stale the current-rules check may be. Ledger
// publication fetches the catalog on every attempt, but a stream publishes on
// every tool call and every daemon tick; fetching that often would trip
// GitHub's raw-content rate limits and recompile the whole catalog each time.
// Fifteen minutes keeps a new upstream detector at most that far behind
// publication. An expired entry is never reused when the refresh fails: the
// stream waits, as publication does.
const currentRulesMaxAge = 15 * time.Minute

// currentRules loads the current upstream catalog; tests replace it.
var currentRules = secretguard.LatestRules

var rulesCache struct {
	mu      sync.Mutex
	delta   *secretguard.RuleSet // current rules minus the bundled ones
	fetched time.Time
}

// currentDelta returns the rules current upstream has that the bundled catalog
// lacks, refreshing them once they are currentRulesMaxAge old.
func currentDelta(ctx context.Context) (*secretguard.RuleSet, error) {
	rulesCache.mu.Lock()
	delta, fetched := rulesCache.delta, rulesCache.fetched
	rulesCache.mu.Unlock()
	if delta != nil && time.Since(fetched) < currentRulesMaxAge {
		return delta, nil
	}
	// fetched without the lock: a concurrent caller with a shorter budget must
	// not queue behind this one's network wait. A duplicate fetch is harmless.
	rules, err := currentRules(ctx)
	if err != nil {
		return nil, err
	}
	delta = rules.Delta(secretguard.Bundled())
	rulesCache.mu.Lock()
	rulesCache.delta, rulesCache.fetched = delta, time.Now()
	rulesCache.mu.Unlock()
	return delta, nil
}

// checkPublication refuses content that publication would refuse. A
// *secretguard.RefusedError means these bytes are refused every time; any
// other error means the check could not run yet.
func checkPublication(ctx context.Context, content []byte) error {
	if err := secretguard.ValidateContent(scanName, content); err != nil {
		return fmt.Errorf("live upload refused: %w", err)
	}
	delta, err := currentDelta(ctx)
	if err != nil {
		return fmt.Errorf("live upload pending, current secret rules unavailable: %w", err)
	}
	if err := delta.ValidateContent(scanName, content); err != nil {
		return fmt.Errorf("live upload refused by current rules: %w", err)
	}
	return nil
}

// onWithheld acts on a failed publication check before an append. Refused
// bytes are withheld the way publication withholds them: they stay in the local
// spool and raw.jsonl, and this stream sends nothing more, because resending
// the same bytes is refused every time and the stream cannot skip over them.
// A check that could not run leaves the spool for a later retry.
func (s *session) onWithheld(ctx context.Context, err error) (verdict, error) {
	var refused *secretguard.RefusedError
	if !errors.As(err, &refused) {
		return vStop, err
	}
	s.disable(ctx, secretRefusedReason, "offset", s.st.Acked, "error", err)
	return vStop, nil
}

// publishableSummary returns summary if publication would send it, else "".
// The close does not wait on the current rules for it: a summary that cannot
// be proven clean is withheld and the server writes its own.
func (s *session) publishableSummary(ctx context.Context, summary string) string {
	if summary == "" {
		return ""
	}
	if err := checkPublication(ctx, []byte(summary)); err != nil {
		s.log().WarnContext(ctx, "live_session_summary_withheld", "error", err)
		return ""
	}
	return summary
}
