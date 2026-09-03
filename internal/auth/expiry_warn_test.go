package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/ephemeral"
	"github.com/sageox/ox/internal/runtime"
)

// clearEphemeralForTest scrubs every signal that could collapse
// runtime.Caps().Browser. Tests calling CheckAndWarnExpiry must scrub
// these first or the no-Browser skip will mask the path under test.
// Also clears the sync.Once-cached capability probe so subsequent
// runtime.Caps() calls see the cleared env.
func clearEphemeralForTest(t *testing.T) {
	t.Helper()
	t.Setenv("OX_EPHEMERAL", "")
	t.Setenv("CLAUDE_CODE_REMOTE", "")
	t.Setenv("DEVIN_TASK_ID", "")
	t.Setenv("CODESPACES", "")
	t.Setenv("CI", "")
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("GITLAB_CI", "")
	t.Setenv("JENKINS_URL", "")
	t.Setenv("BUILDKITE", "")
	t.Setenv("CODEBUILD_BUILD_ID", "")
	t.Setenv("OX_BROWSER", "")
	t.Setenv("OX_PERSIST_DISK", "")
	t.Setenv("OX_NO_DAEMON", "")
	ephemeral.SetUserConfigPreference(nil)
	runtime.Reset()
	t.Cleanup(runtime.Reset)
}

// TestComputeExpiryThreshold_TableDriven covers the lifetime-pct math against
// realistic PAT lifetimes (7d / 30d / 90d / 365d). Failure prevented: the
// threshold collapses to the day-floor for long-lived tokens and we never warn
// until the last 24h, defeating the early-warning purpose.
func TestComputeExpiryThreshold_TableDriven(t *testing.T) {
	pct := 0.05
	minDays := 1
	cfg := &config.UserConfig{
		PATExpiryWarningThresholdPct: &pct,
		PATExpiryWarningMinDays:      &minDays,
	}

	now := time.Date(2026, 5, 27, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		lifetime  time.Duration
		wantFloor time.Duration // minimum acceptable threshold
		wantCeil  time.Duration // maximum acceptable threshold
	}{
		{"7d token", 7 * 24 * time.Hour, 24 * time.Hour, 24 * time.Hour},                   // 5% of 7d < 1d, floor wins
		{"30d token", 30 * 24 * time.Hour, 36 * time.Hour, 36*time.Hour + time.Minute},     // 5% of 30d = 36h
		{"90d token", 90 * 24 * time.Hour, 4*24*time.Hour + 12*time.Hour, 109 * time.Hour}, // 5% of 90d = 108h
		{"365d token", 365 * 24 * time.Hour, 18 * 24 * time.Hour, 19 * 24 * time.Hour},     // 5% of 365d ≈ 18.25d
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			created := now
			expires := now.Add(tc.lifetime)
			got := computeExpiryThreshold(expires, created, cfg)
			assert.GreaterOrEqual(t, got, tc.wantFloor, "threshold below expected floor")
			assert.LessOrEqual(t, got, tc.wantCeil, "threshold above expected ceiling")
		})
	}
}

// TestComputeExpiryThreshold_DisabledWhenAllZero — Failure prevented: a user
// who sets both knobs to 0 still sees the warning because the floor kicks in
// at 1d. Sentinel -1 lets the caller distinguish "disabled" from "tiny."
func TestComputeExpiryThreshold_DisabledWhenAllZero(t *testing.T) {
	pct := 0.0
	minDays := 0
	cfg := &config.UserConfig{
		PATExpiryWarningThresholdPct: &pct,
		PATExpiryWarningMinDays:      &minDays,
	}
	now := time.Now()
	got := computeExpiryThreshold(now.Add(24*time.Hour), now, cfg)
	assert.Equal(t, time.Duration(-1), got)
}

// TestComputeExpiryThreshold_DefaultsApply — Failure prevented: when the user
// has never set anything, we fall back to (0.05, 1d) and warn at the right
// time. If defaults silently became 0, no warning would ever fire.
func TestComputeExpiryThreshold_DefaultsApply(t *testing.T) {
	cfg := &config.UserConfig{}
	now := time.Now()
	// 30-day lifetime — 5% is 36h, which exceeds the 1d floor.
	got := computeExpiryThreshold(now.Add(30*24*time.Hour), now, cfg)
	assert.Greater(t, got, 24*time.Hour)
}

// TestCheckAndWarnExpiry_SkippedWhenEphemeral — Failure prevented: cloud
// agents spam stderr on every prime, polluting their structured outputs.
// The gate is now runtime.Caps().Browser=false; OX_EPHEMERAL=1 collapses
// it, so the historical OX_EPHEMERAL=1 invocation still suppresses.
func TestCheckAndWarnExpiry_SkippedWhenEphemeral(t *testing.T) {
	t.Setenv("OX_EPHEMERAL", "1")
	defer ephemeral.SetUserConfigPreference(nil)
	runtime.Reset()
	t.Cleanup(runtime.Reset)

	resetExpiryWarnedKeysForTest()
	var buf bytes.Buffer
	emitted := CheckAndWarnExpiry(context.Background(), "https://sageox.ai", &buf)
	assert.False(t, emitted)
	assert.Empty(t, buf.String())
}

// TestCheckAndWarnExpiry_SkippedForNonTTY — Failure prevented: piping
// `ox status` into another tool gets stderr noise that the consumer
// did not consent to.
func TestCheckAndWarnExpiry_SkippedForNonTTY(t *testing.T) {
	clearEphemeralForTest(t)
	resetExpiryWarnedKeysForTest()

	// bytes.Buffer is the canonical "not a TTY" target.
	var buf bytes.Buffer
	emitted := CheckAndWarnExpiry(context.Background(), "https://sageox.ai", &buf)
	assert.False(t, emitted)
	assert.Empty(t, buf.String())
}

// serveEnvTokenMeta binds a CRC-valid SAGEOX_TOKEN to a stub metadata server
// and returns the StoredToken the env layer produces for it. The endpoint env
// var must point at the same server or envTokenBoundValue refuses the token.
func serveEnvTokenMeta(t *testing.T, body map[string]any) (*httptest.Server, *StoredToken) {
	t.Helper()
	withTempCacheDir(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)

	t.Setenv(endpoint.EnvVar, srv.URL)
	t.Setenv(EnvVarToken, "oxp_test_4bDZfN")

	stored, state := EnvTokenFor(srv.URL)
	require.Equal(t, EnvTokenValid, state, "fixture token must pass the local CRC check")
	return srv, stored
}

// TestResolveTokenExpiry_NeverExpires — Failure prevented: a never-expires
// token (server returns expires_at: null) somehow triggers a warning,
// which would be permanently noisy and nonsensical.
func TestResolveTokenExpiry_NeverExpires(t *testing.T) {
	clearEphemeralForTest(t)
	srv, stored := serveEnvTokenMeta(t, map[string]any{"expires_at": nil, "name": "forever"})

	_, _, ok := resolveTokenExpiry(context.Background(), srv.URL, stored)
	assert.False(t, ok, "a null expires_at must be treated as never-expires")
}

// TestResolveTokenExpiry_EnvTokenStillWarns — Failure prevented: suppressing the
// disk-token false alarm goes too far and silences the one credential class the
// warning exists for. An env token has no refresh credential, so its expiry is
// terminal and a human must go rotate it.
func TestResolveTokenExpiry_EnvTokenStillWarns(t *testing.T) {
	clearEphemeralForTest(t)
	expires := time.Now().Add(48 * time.Hour).UTC()
	srv, stored := serveEnvTokenMeta(t, map[string]any{
		"expires_at": expires.Format(time.RFC3339),
		"name":       "ci-token",
	})

	got, created, ok := resolveTokenExpiry(context.Background(), srv.URL, stored)
	require.True(t, ok, "an expiring env token must still resolve an expiry")
	assert.WithinDuration(t, expires, got, time.Second)
	assert.False(t, created.IsZero(), "createdAt must fall back to the assumed lifetime")
}

// TestResolveTokenExpiry_DiskTokenNeverWarns — Failure prevented: `ox status`
// prints "✓ auto-refresh enabled" and then, on the next line, tells the same
// user their token expires in minutes and to mint a new one at /settings/tokens.
// device_flow.go and refresh.go are the only writers of auth.json, so a disk
// StoredToken.ExpiresAt is always the OAuth access-token stamp — hours away by
// construction, and always inside the warning threshold.
func TestResolveTokenExpiry_DiskTokenNeverWarns(t *testing.T) {
	t.Setenv(EnvVarToken, "") // no env credential — force the disk path

	tests := []struct {
		name  string
		token *StoredToken
	}{
		{"auto-refreshing session", &StoredToken{
			AccessToken:  "jwt.access",
			RefreshToken: "refresh-present",
			ExpiresAt:    time.Now().Add(11 * time.Minute),
		}},
		{"better-auth session token as refresh credential", &StoredToken{
			AccessToken:  "jwt.access",
			SessionToken: "session-present",
			ExpiresAt:    time.Now().Add(2 * time.Hour),
		}},
		// Re-login IS required here, but `ox login` is the remedy — not the new
		// PAT this warning would advertise. Login and `ox status` already say so.
		{"no refresh credential at all", &StoredToken{
			AccessToken: "jwt.access",
			ExpiresAt:   time.Now().Add(23 * time.Hour),
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, ok := resolveTokenExpiry(context.Background(), "https://sageox.ai", tc.token)
			assert.False(t, ok, "a disk-stored token must never produce an expiry warning")
		})
	}
}

// TestEmitExpiryWarning_Format — Failure prevented: warning format drifts
// from the single-line key=value contract. Log scrapers and humans both
// depend on this shape.
func TestEmitExpiryWarning_Format(t *testing.T) {
	var buf bytes.Buffer
	emitExpiryWarning(&buf, "https://sageox.ai", "env", 4*24*time.Hour+3*time.Hour)
	out := buf.String()
	assert.True(t, strings.HasPrefix(out, "warn=ox_token_expiring"), "missing warn= prefix")
	assert.Contains(t, out, "source=env")
	assert.Contains(t, out, "expires_in=4d3h")
	assert.Contains(t, out, "create_new=https://sageox.ai/settings/tokens")
	assert.True(t, strings.HasSuffix(out, "\n"), "must be single line terminated by \\n")
	assert.Equal(t, 1, strings.Count(out, "\n"), "must be a single line")
}

// TestHumanizeDuration covers the rendered shapes used in warnings.
// Failure prevented: ambiguous durations like "1500m" leak into user-
// facing output, hurting trust.
func TestHumanizeDuration(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{4*24*time.Hour + 3*time.Hour, "4d3h"},
		{2 * time.Hour, "2h0m"},
		{45 * time.Minute, "45m"},
		{0, "0s"},
		{-1 * time.Hour, "0s"},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, humanizeDuration(tc.in), "in=%s", tc.in)
	}
}

// TestExpiryWarn_DedupeWithinProcess — Failure prevented: a CLI command that
// transitively calls multiple wired-in commands (prime then status) warns
// twice in a row, reading as nagging.
func TestExpiryWarn_DedupeWithinProcess(t *testing.T) {
	// Direct test of the dedupe map: simulate two emissions for the same
	// token hash. The integration path that calls CheckAndWarnExpiry is
	// covered by the other tests; here we prove the dedupe primitive.
	resetExpiryWarnedKeysForTest()

	key := tokenHashKey("oxp_dup")

	expiryWarnedKeysMu.Lock()
	already := expiryWarnedKeys[key]
	if !already {
		expiryWarnedKeys[key] = true
	}
	expiryWarnedKeysMu.Unlock()
	require.False(t, already, "first call must see unset")

	expiryWarnedKeysMu.Lock()
	already2 := expiryWarnedKeys[key]
	expiryWarnedKeysMu.Unlock()
	require.True(t, already2, "second call must see set")
}

// TestEmitExpiryWarning_UsesEndpointHost — Failure prevented: non-production
// endpoints tell the user to create a token on sageox.ai instead of the active
// host that actually issued the credential.
func TestEmitExpiryWarning_UsesEndpointHost(t *testing.T) {
	var buf bytes.Buffer
	emitExpiryWarning(&buf, "https://staging.sageox.ai", "env", 2*time.Hour)
	out := buf.String()
	assert.Contains(t, out, "create_new=https://staging.sageox.ai/settings/tokens")
}
