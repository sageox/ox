package auth

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/mattn/go-isatty"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/runtime"
)

// envTokenAssumedLifetime is the fallback lifetime used for env-supplied
// tokens when the server response did not include a CreatedAt. Mirrors
// the default SageOx PAT TTL — generous enough that the warning still
// fires roughly when expected, conservative enough that it never warns
// at unreasonably wide windows.
const envTokenAssumedLifetime = 90 * 24 * time.Hour

// expiryWarnedKeys deduplicates warning emissions within a single process
// run, keyed by SHA-256 of the token. Without this, a single CLI command
// that calls multiple subcommands (or that runs through prime+status
// chains) would warn twice in a row, which reads as nagging.
var (
	expiryWarnedKeysMu sync.Mutex
	expiryWarnedKeys   = map[string]bool{}
)

// resetExpiryWarnedKeysForTest clears the dedupe map. Test-only — public
// callers must not rely on this.
func resetExpiryWarnedKeysForTest() {
	expiryWarnedKeysMu.Lock()
	defer expiryWarnedKeysMu.Unlock()
	expiryWarnedKeys = map[string]bool{}
}

// CheckAndWarnExpiry emits a single-line stderr warning when the PAT for
// the given endpoint is within max(1d, lifetime*pct) of expiry. Returns
// true if a warning was emitted; false otherwise (including all skip
// cases — ephemeral, never-expires, dedup, non-TTY, no auth).
//
// Skipped silently when:
//   - runtime.Caps().Browser is false (no human at keyboard — sandbox,
//     headless CI runner, OX_BROWSER=0). The warning targets a human
//     operator; an unwatched stderr just creates noise in agent logs.
//   - the writer is not a TTY (unless the *os.File path resolves to stderr
//     and the caller has already decided to force; today the policy is
//     "TTY only" with no override flag — keep it simple)
//   - the token has never-expires semantics (ExpiresAt == nil)
//   - the same token hash already warned this process
//   - no token is configured for the endpoint (nothing to warn about)
//
// IMPORTANT: gated on Browser specifically, not the composite
// ephemeral.IsEphemeral(). Browser is the honest capability question
// — "is there a human watching stderr?" — and it captures the same
// cases (CI, cloud agents, OX_EPHEMERAL=1) without dragging in disk /
// daemon concerns that have nothing to do with warning visibility.
func CheckAndWarnExpiry(ctx context.Context, ep string, w io.Writer) bool {
	if !runtime.Caps().Browser {
		return false
	}
	if !writerIsTTY(w) {
		return false
	}

	ep = endpoint.NormalizeEndpoint(ep)
	token, err := GetTokenForEndpoint(ep)
	if err != nil || token == nil || token.AccessToken == "" {
		return false
	}

	// dedupe across multiple calls in the same process — the cheap path.
	key := tokenHashKey(token.AccessToken)
	expiryWarnedKeysMu.Lock()
	already := expiryWarnedKeys[key]
	expiryWarnedKeysMu.Unlock()
	if already {
		return false
	}

	expiresAt, createdAt, label, ok := resolveTokenExpiry(ctx, ep, token)
	if !ok {
		return false
	}

	cfg, _ := config.LoadUserConfig()
	threshold := computeExpiryThreshold(expiresAt, createdAt, cfg)
	if threshold < 0 {
		// warnings disabled entirely (both pct=0 and min_days=0)
		return false
	}

	remaining := time.Until(expiresAt)
	if remaining > threshold {
		return false
	}

	// dedupe: claim the key BEFORE writing so racing goroutines pick one
	// winner. The check above is a fast-path; this is the authoritative
	// gate.
	expiryWarnedKeysMu.Lock()
	if expiryWarnedKeys[key] {
		expiryWarnedKeysMu.Unlock()
		return false
	}
	expiryWarnedKeys[key] = true
	expiryWarnedKeysMu.Unlock()

	emitExpiryWarning(w, ep, label, remaining)
	return true
}

// resolveTokenExpiry returns the authoritative expiry + creation time for
// the token. For env-supplied tokens it consults the token-meta cache
// (calling /api/v1/auth/me when stale); for disk-stored tokens it trusts
// StoredToken.ExpiresAt directly. The third return value is a short
// human label ("env" or "disk") for the warning's diagnostic key.
//
// Returns ok=false when the token is never-expires (ExpiresAt nil or
// zero-valued) or when no expiry information is available.
func resolveTokenExpiry(ctx context.Context, ep string, token *StoredToken) (expiresAt time.Time, createdAt time.Time, label string, ok bool) {
	if isEnvToken(ep, token) {
		meta, err := FetchTokenMetaCached(ctx, ep, token.AccessToken)
		if err != nil || meta == nil {
			return time.Time{}, time.Time{}, "", false
		}
		if meta.ExpiresAt == nil || meta.ExpiresAt.IsZero() {
			// never-expires token: no warning, ever.
			return time.Time{}, time.Time{}, "", false
		}
		created := meta.CreatedAt
		if created.IsZero() {
			created = meta.ExpiresAt.Add(-envTokenAssumedLifetime)
		}
		return *meta.ExpiresAt, created, "env", true
	}

	// disk-stored token — ExpiresAt is set by the OAuth flow.
	if token.ExpiresAt.IsZero() {
		return time.Time{}, time.Time{}, "", false
	}
	// CreatedAt isn't tracked on StoredToken — approximate via fixed
	// assumed lifetime. The threshold math still floors at 1 day so this
	// can't pathologically over-warn.
	created := token.ExpiresAt.Add(-envTokenAssumedLifetime)
	return token.ExpiresAt, created, "disk", true
}

// computeExpiryThreshold returns the duration before expiry at which the
// warning should fire: max(minDays, lifetime*pct). Returns -1 when both
// minDays and pct are zero — interpreted by the caller as "warnings
// disabled."
func computeExpiryThreshold(expiresAt, createdAt time.Time, cfg *config.UserConfig) time.Duration {
	pct := cfg.GetPATExpiryWarningThresholdPct()
	minDays := cfg.GetPATExpiryWarningMinDays()
	if pct <= 0 && minDays <= 0 {
		return -1
	}

	floor := time.Duration(minDays) * 24 * time.Hour

	var pctThreshold time.Duration
	if pct > 0 && !createdAt.IsZero() && expiresAt.After(createdAt) {
		lifetime := expiresAt.Sub(createdAt)
		pctThreshold = time.Duration(float64(lifetime) * pct)
	}

	if pctThreshold > floor {
		return pctThreshold
	}
	return floor
}

// tokenSettingsURL returns the token-management page for the active endpoint.
// Production uses the canonical sageox.ai host; self-hosted or staging hosts
// stay on their own origin.
func tokenSettingsURL(ep string) string {
	ep = endpoint.NormalizeEndpoint(ep)
	if ep == "" {
		ep = endpoint.Default
	}
	return ep + "/settings/tokens"
}

// emitExpiryWarning writes the single-line key=value warning to the caller's
// writer. Format follows the CLAUDE.md logging convention so it greps cleanly
// alongside other ox logs.
func emitExpiryWarning(w io.Writer, ep, source string, remaining time.Duration) {
	humanRemaining := humanizeDuration(remaining)
	fmt.Fprintf(w,
		"warn=ox_token_expiring source=%s expires_in=%s create_new=%s\n",
		source, humanRemaining, tokenSettingsURL(ep),
	)
}

// humanizeDuration renders a duration as a compact "<N>d<H>h" or "<N>h<M>m"
// string suitable for the warning. Negative durations render as "0s" — the
// warning still fires (the token is already expired) but the format stays
// human-readable.
func humanizeDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	days := int(d / (24 * time.Hour))
	hours := int((d % (24 * time.Hour)) / time.Hour)
	if days > 0 {
		return fmt.Sprintf("%dd%dh", days, hours)
	}
	minutes := int((d % time.Hour) / time.Minute)
	if hours > 0 {
		return fmt.Sprintf("%dh%dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}

// writerIsTTY returns true when w is an *os.File pointing at a TTY. Any
// other writer (bytes.Buffer in tests, pipes, files) returns false — we
// never spam non-interactive consumers.
func writerIsTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}
