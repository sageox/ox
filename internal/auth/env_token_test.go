package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Note: these tests use t.Setenv which is incompatible with t.Parallel
// (the runtime panics if both are used). Env-token resolution is inherently
// process-global, so serial execution is the correct semantics.

// validSageOxTestToken mints a syntactically valid oxp_/oxt_ test token (a
// correct "_<crc>" suffix per verifyTokenChecksum) so tests that aren't
// specifically about the checksum gate don't trip over it. It reproduces the
// server's token shape without pulling in crypto/rand.
//
// It mints with crc32Base62 and its callers verify with verifyTokenChecksum, so
// any mutation to base62Encode moves BOTH sides identically and every test using
// this helper stays green. That is correct for a fixture — these tests are about
// env-token resolution, not the CRC mirror — but it means none of them count as
// coverage of the mirror itself. Only the pinned golden vectors and
// TestBase62Encode_LengthClasses do that.
func validSageOxTestToken(prefix, body string) string {
	withPrefix := prefix + body
	return withPrefix + "_" + crc32Base62(withPrefix)
}

// Failure prevented: read sync silently changes identities by accepting a PAT,
// using a disk login, or carrying a team token to a different endpoint.
func TestCurrentReadTokenRequiresSelectedTeamToken(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("SAGEOX_ENDPOINT", "https://sageox.ai")
	require.NoError(t, SaveTokenForEndpoint("https://sageox.ai", createTestTokenForTest(time.Hour)))
	for _, tc := range []struct {
		name     string
		token    string
		endpoint string
		want     string
	}{
		{"team token", "oxt_test_1ljPfr", "https://sageox.ai", "oxt_test_1ljPfr"},
		{"trailing slash", "oxt_test_1ljPfr", "https://sageox.ai/", "oxt_test_1ljPfr"},
		{"normalized token", " Bearer oxt_test_1ljPfr\n", "https://sageox.ai", "oxt_test_1ljPfr"},
		{"missing despite disk login", "", "https://sageox.ai", ""},
		{"personal token", "oxp_test_4bDZfN", "https://sageox.ai", ""},
		{"opaque token", "oauth-access-token", "https://sageox.ai", ""},
		{"bad checksum", "oxt_test_1ljPfX", "https://sageox.ai", ""},
		{"wrong endpoint", "oxt_test_1ljPfr", "https://staging.sageox.ai", ""},
		{"normalized sibling host", "oxt_test_1ljPfr", "https://api.sageox.ai", ""},
		{"embedded newline", validSageOxTestToken(TeamTokenPrefix, "test\ninjected"), "https://sageox.ai", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvVarToken, tc.token)
			token, err := CurrentReadToken(tc.endpoint)
			if tc.want == "" {
				require.ErrorIs(t, err, ErrReadTokenUnavailable)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.want, token)
		})
	}
}

// Failure prevented: a long-lived reader reuses a revoked credential after
// SAGEOX_TOKEN rotates, disappears, or switches to another deployment.
func TestCurrentReadTokenObservesRotation(t *testing.T) {
	t.Setenv("SAGEOX_ENDPOINT", "https://sageox.ai")
	for _, raw := range []string{"oxt_test_1ljPfr", "oxt_rotated_1lKvCA", ""} {
		t.Setenv(EnvVarToken, raw)
		token, err := CurrentReadToken("https://sageox.ai")
		if raw == "" {
			require.ErrorIs(t, err, ErrReadTokenUnavailable)
		} else {
			require.NoError(t, err)
		}
		require.Equal(t, raw, token)
	}
	t.Setenv(EnvVarToken, "oxt_test_1ljPfr")
	t.Setenv("SAGEOX_ENDPOINT", "https://staging.sageox.ai")
	token, err := CurrentReadToken("https://sageox.ai")
	require.ErrorIs(t, err, ErrReadTokenUnavailable)
	require.Empty(t, token)
	token, err = CurrentReadToken("https://staging.sageox.ai")
	require.NoError(t, err)
	require.Equal(t, "oxt_test_1ljPfr", token)
}

// TestTokenFromEnv_SageOxTokenSet — Failure prevented: SAGEOX_TOKEN env doesn't produce a
// usable StoredToken, breaking CI/CD and headless agents.
func TestTokenFromEnv_SageOxTokenSet(t *testing.T) {
	// a pinned golden vector (also asserted directly in
	// TestVerifyTokenChecksum_GoldenVectors) — using it here doubles as proof
	// that a real, validly-checksummed PAT is accepted end-to-end.
	const tok = "oxp_test_4bDZfN"
	t.Setenv(EnvVarToken, tok)

	got := tokenFromEnv("https://api.sageox.ai/")
	require.NotNil(t, got)
	assert.Equal(t, tok, got.AccessToken)
	assert.Equal(t, "Bearer", got.TokenType)
	assert.Equal(t, "*", got.Scope)
	assert.Empty(t, got.RefreshToken)
	assert.Empty(t, got.SessionToken)
	assert.True(t, got.ExpiresAt.After(time.Now()), "env token expiry must be in the future")
}

// TestTokenFromEnv_LegacyOxTokenIgnored — Failure prevented: removed OX_TOKEN
// support accidentally lingers and keeps a second customer-facing env var alive.
func TestTokenFromEnv_LegacyOxTokenIgnored(t *testing.T) {
	t.Setenv(EnvVarToken, "")
	t.Setenv("OX_TOKEN", "oxp_legacy")

	assert.Nil(t, tokenFromEnv("https://api.sageox.ai/"))
}

// TestTokenFromEnv_NeitherSet — Failure prevented: nil-return contract broken,
// callers that fall through to disk lookup are skipped.
func TestTokenFromEnv_NeitherSet(t *testing.T) {
	t.Setenv(EnvVarToken, "")
	t.Setenv("OX_TOKEN", "")

	assert.Nil(t, tokenFromEnv("https://api.sageox.ai/"))
}

// TestTokenFromEnv_MismatchedEndpointIgnored — Failure prevented: a single
// env token silently applies to every endpoint lookup and gets sent to the
// wrong host in multi-endpoint setups.
func TestTokenFromEnv_MismatchedEndpointIgnored(t *testing.T) {
	t.Setenv(EnvVarToken, "oxp_prod")
	t.Setenv("SAGEOX_ENDPOINT", "")

	assert.Nil(t, tokenFromEnv("https://staging.sageox.ai/"))
}

// TestTokenFromEnv_ExplicitEndpointSelection — Failure prevented: staging or
// self-hosted users set SAGEOX_TOKEN but, without binding it to the selected
// endpoint, either hit production implicitly or fan the token out everywhere.
func TestTokenFromEnv_ExplicitEndpointSelection(t *testing.T) {
	tok := validSageOxTestToken(PATPrefix, "stage")
	t.Setenv(EnvVarToken, tok)
	t.Setenv("SAGEOX_ENDPOINT", "https://staging.sageox.ai")

	got := tokenFromEnv("https://staging.sageox.ai/")
	require.NotNil(t, got)
	assert.Equal(t, tok, got.AccessToken)
	assert.Nil(t, tokenFromEnv("https://sageox.ai/"))
}

// TestGetTokenForEndpoint_EnvOverridesDisk — Failure prevented: env token
// ignored when disk has a token, defeating the override use case.
func TestGetTokenForEndpoint_EnvOverridesDisk(t *testing.T) {
	t.Setenv(EnvVarToken, "")
	t.Setenv("OX_TOKEN", "")

	client := NewTestClient(t)
	disk := createTestTokenForTest(1 * time.Hour)
	disk.AccessToken = "disk-token"
	require.NoError(t, client.SaveToken(disk))

	// without env, disk wins
	got, err := client.GetToken()
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "disk-token", got.AccessToken)

	// with env, env wins
	t.Setenv(EnvVarToken, "env-token")
	got, err = client.GetToken()
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "env-token", got.AccessToken)
	assert.Equal(t, "*", got.Scope)
}

// TestGetTokenForEndpoint_EnvEmptyFallsBackToDisk — Failure prevented:
// empty env var treated as a token, blanking out legitimate disk auth.
func TestGetTokenForEndpoint_EnvEmptyFallsBackToDisk(t *testing.T) {
	t.Setenv(EnvVarToken, "")
	t.Setenv("OX_TOKEN", "")

	client := NewTestClient(t)
	disk := createTestTokenForTest(1 * time.Hour)
	disk.AccessToken = "disk-only"
	require.NoError(t, client.SaveToken(disk))

	got, err := client.GetToken()
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "disk-only", got.AccessToken)
}

// TestGetTokenForEndpoint_MismatchedEnvFallsBackToDisk — Failure prevented:
// exporting SAGEOX_TOKEN for one endpoint hijacks token lookups for a different
// endpoint that already has valid disk credentials.
func TestGetTokenForEndpoint_MismatchedEnvFallsBackToDisk(t *testing.T) {
	t.Setenv(EnvVarToken, "env-prod")
	t.Setenv("SAGEOX_ENDPOINT", "")

	client := NewTestClient(t)
	disk := createTestTokenForTest(1 * time.Hour)
	disk.AccessToken = "disk-staging"
	require.NoError(t, client.SaveTokenForEndpoint("https://staging.sageox.ai", disk))

	got, err := client.GetTokenForEndpoint("https://staging.sageox.ai")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "disk-staging", got.AccessToken)
}

// TestPackageAndClientAgree — Failure prevented: package-level
// GetTokenForEndpoint and AuthClient.GetTokenForEndpoint diverge on env-token
// resolution, leading to inconsistent auth behavior across call sites.
func TestPackageAndClientAgree(t *testing.T) {
	tok := validSageOxTestToken(PATPrefix, "agree")
	t.Setenv(EnvVarToken, tok)

	ep := "https://api.sageox.ai/"

	pkgTok, err := GetTokenForEndpoint(ep)
	require.NoError(t, err)
	require.NotNil(t, pkgTok)

	client := NewTestClient(t)
	cliTok, err := client.GetTokenForEndpoint(ep)
	require.NoError(t, err)
	require.NotNil(t, cliTok)

	assert.Equal(t, pkgTok.AccessToken, cliTok.AccessToken)
	assert.Equal(t, tok, pkgTok.AccessToken)
	assert.Equal(t, pkgTok.Scope, cliTok.Scope)
	assert.Equal(t, pkgTok.TokenType, cliTok.TokenType)
}

// --- CRC/format precheck ---

// TestVerifyTokenChecksum_GoldenVectors pins known-good token strings. A
// mismatch here means base62Encode/crc32Base62/verifyTokenChecksum in
// env_token.go has drifted from the server's token package, and a legitimate
// server-minted token would be rejected locally.
func TestVerifyTokenChecksum_GoldenVectors(t *testing.T) {
	assert.True(t, verifyTokenChecksum("oxt_test_1ljPfr"), "pinned team-token vector must verify")
	assert.True(t, verifyTokenChecksum("oxp_test_4bDZfN"), "pinned personal-token vector must verify")

	// The CRC covers the family prefix, so a personal token's suffix must NOT
	// validate a team token: crc32Base62("oxt_test") is "1ljPfr", not "4bDZfN".
	// Beyond stating that property, this guards a plausible future "leniency"
	// edit that strips or normalizes the family prefix before hashing — which
	// would let anyone turn an oxp_ value into a valid-looking oxt_ one by
	// editing a single character.
	assert.False(t, verifyTokenChecksum("oxt_test_4bDZfN"), "a personal token's crc must not validate a team token")
}

// TestVerifyTokenChecksum_CorruptedSuffixRejected proves the suffix comparison is
// exact — not a length check, and not case-insensitive.
//
// Each row was checked against a deliberately weakened verifyTokenChecksum, and
// the notes below record which weakening each row actually catches:
func TestVerifyTokenChecksum_CorruptedSuffixRejected(t *testing.T) {
	tests := []struct {
		name string
		tok  string
	}{
		// Same length and same case pattern as the valid suffix, so only a
		// content comparison rejects it. Catches a length-only comparison.
		{"single character substituted", "oxp_test_4bDZfX"},
		// The real-world shape: a copy/paste that dropped trailing characters.
		// Caught by either a length or a content comparison — its value is
		// covering the truncation case explicitly, and killing a comparison
		// weakened to a prefix match.
		{"suffix truncated", "oxp_test_4bDZf"},
		// The ONLY row here that catches a case-insensitive comparison — the
		// two rows above differ in content, so EqualFold still rejects them.
		// base62 uses upper and lower case as distinct digits, so folding case
		// would accept a token that was never minted.
		{"suffix case flipped", "oxp_test_4bdzfn"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.False(t, verifyTokenChecksum(tt.tok), "corrupted crc suffix must not verify")
		})
	}
}

// TestBase62Encode_LengthClasses pins base62Encode's output across every length
// class a 4-byte CRC can produce.
//
// Why this exists: base62Encode emits VARIABLE-length output with no
// left-padding, except that an all-zero input returns len(b) zeros — so zero
// encodes to "0000" while one encodes to "1". That is not a coherent padding
// scheme, and the two pinned golden vectors cannot detect the problem because
// both land in the maximal 6-character class and stay green under either padding
// mutation. If the server pads to a fixed width, or preserves leading zero bytes
// the way base58check does, then this client rejects every token whose CRC is
// below 62^5 — about 21% of the keyspace — as a "typo".
//
// PROVENANCE — read before trusting this table. These six expected values were
// computed locally, with an independent from-scratch CRC-32/IEEE + base62
// implementation (Python zlib), NOT by reading the Go implementation back to
// itself. That independence rules out transcription error, but it does not make
// them authoritative: out of the box this table proves only that the two
// implementations agree, i.e. self-consistency. Its real value is that it makes
// the padding contract explicit at every length class, so a padding change can
// no longer pass silently.
//
// A human must still confirm these six outputs against the server's own token
// package before the "mirror" claim in env_token.go is trustworthy. If the server
// disagrees on even one row, that is a live bug rejecting roughly 21% of validly
// minted tokens, not a test that needs updating.
func TestBase62Encode_LengthClasses(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"6 chars (pinned personal vector)", "oxp_test", "4bDZfN"},
		{"6 chars (pinned team vector)", "oxt_test", "1ljPfr"},
		{"5 chars", "oxp_a", "b6CVb"},
		{"4 chars", "oxp_bk", "oGjm"},
		{"3 chars", "oxp_tsa", "2hW"},
		{"2 chars", "oxp_cngh", "o8"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, crc32Base62(tt.input))
		})
	}

	// The all-zero branch is asserted on base62Encode directly because it is
	// effectively unreachable through crc32Base62 — an input whose CRC-32 is
	// exactly zero occurs at a rate of 2^-32, so no realistic token exercises it.
	// It is also the single inconsistency in the padding contract above.
	assert.Equal(t, "0000", base62Encode([]byte{0, 0, 0, 0}), "all-zero input returns one '0' per input byte, unlike every other value")
}

// TestTokenFromEnv_BadChecksumRejectedLocally — Failure prevented: a typo'd
// or truncated SAGEOX_TOKEN reaching the server and coming back as a
// confusing generic 401, instead of failing locally with a clear reason.
// This is the red-first proof for the CRC precheck: before it existed,
// tokenFromEnv returned this malformed value as-is.
func TestTokenFromEnv_BadChecksumRejectedLocally(t *testing.T) {
	// oxp_ prefix (recognized family) but the crc suffix doesn't match —
	// exactly the shape of a truncated copy/paste.
	t.Setenv(EnvVarToken, "oxp_test_4bDZfX")

	assert.Nil(t, tokenFromEnv("https://api.sageox.ai/"), "malformed checksum must be rejected locally, not sent to the server")
}

// TestTokenFromEnv_NonSageOxFormatBypassesChecksum — Failure prevented: the
// checksum gate rejecting opaque OAuth tokens / JWTs / other non-SageOx
// bearer credentials that were never minted with our CRC grammar in the
// first place — those have no checksum to verify, so they must fall through
// to server-side validation unchanged.
func TestTokenFromEnv_NonSageOxFormatBypassesChecksum(t *testing.T) {
	t.Setenv(EnvVarToken, "header.payload.signature")

	got := tokenFromEnv("https://api.sageox.ai/")
	require.NotNil(t, got)
	assert.Equal(t, "header.payload.signature", got.AccessToken)
}

// TestTokenFromEnv_ValidTeamTokenAccepted proves an oxt_ token with a
// correct checksum passes the precheck exactly like an oxp_ one.
func TestTokenFromEnv_ValidTeamTokenAccepted(t *testing.T) {
	tok := validSageOxTestToken(TeamTokenPrefix, "svc")
	t.Setenv(EnvVarToken, tok)

	got := tokenFromEnv("https://api.sageox.ai/")
	require.NotNil(t, got)
	assert.Equal(t, tok, got.AccessToken)
}

// --- env value normalization ---

// TestTokenFromEnv_SurroundingWhitespaceTolerated — Failure prevented: a token
// delivered with a trailing newline (the default from `export
// SAGEOX_TOKEN=$(cat secret)`, a file-mounted Kubernetes secret, or a YAML block
// scalar) fails the CRC check and gets reported to the user as "likely a typo",
// sending them to hunt for a corruption that does not exist.
//
// The trimmed value must also be what lands in AccessToken, so a clean
// Authorization header goes on the wire — a bare newline inside a header value is
// itself a protocol error.
func TestTokenFromEnv_SurroundingWhitespaceTolerated(t *testing.T) {
	const clean = "oxp_test_4bDZfN"

	tests := []struct {
		name string
		raw  string
	}{
		{"trailing newline", clean + "\n"},
		{"trailing space", clean + " "},
		{"leading tab and crlf", "\t" + clean + "\r\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvVarToken, tt.raw)

			got := tokenFromEnv("https://api.sageox.ai/")
			require.NotNil(t, got, "whitespace-padded token must not be rejected as malformed")
			assert.Equal(t, clean, got.AccessToken, "the trimmed value must go on the wire")
		})
	}
}

// TestTokenFromEnv_LeadingCorruptionNotSilentlyAccepted — Failure prevented:
// corruption at the HEAD of the value slips past the format gate entirely. Because
// isSageOxTokenFormat matches on a prefix, `"oxp_..."` and `Bearer oxp_...` do not
// look like SageOx-family tokens at all, so the CRC precheck never runs, the raw
// junk goes on the wire, and the user gets a generic 401 with no hint that the
// value itself is the problem. The quoted form is the `docker --env-file` footgun
// (it does not strip quotes); the Bearer form is copied out of HTTP examples.
func TestTokenFromEnv_LeadingCorruptionNotSilentlyAccepted(t *testing.T) {
	const clean = "oxp_test_4bDZfN"

	tests := []struct {
		name string
		raw  string
	}{
		{"double quoted", `"` + clean + `"`},
		{"bearer prefixed", "Bearer " + clean},
		{"leading space", " " + clean},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvVarToken, tt.raw)

			got := tokenFromEnv("https://api.sageox.ai/")
			require.NotNil(t, got)
			assert.Equal(t, clean, got.AccessToken, "the cleaned value must go on the wire, never the raw one")
		})
	}
}

// --- env token state classification ---

// TestEnvTokenFor_ReportsMalformedDistinctlyFromAbsent — Failure prevented:
// callers cannot tell "no env token" from "the operator named a credential we
// refused". Conflating them makes a malformed SAGEOX_TOKEN fall through to a disk
// token, silently changing which principal every API call, ledger write, and
// murmur is attributed to.
//
// The endpoint-mismatch row is Absent on purpose and the distinction matters: a
// token bound to a different endpoint was never meant for this one, so falling
// back to disk is the correct behavior there — unlike malformed.
func TestEnvTokenFor_ReportsMalformedDistinctlyFromAbsent(t *testing.T) {
	const ep = "https://api.sageox.ai/"

	t.Run("malformed when bound to this endpoint", func(t *testing.T) {
		// oxp_/oxt_ family, so the CRC grammar applies — but the suffix is wrong.
		t.Setenv(EnvVarToken, "oxt_test_4bDZfX")

		tok, state := EnvTokenFor(ep)
		assert.Equal(t, EnvTokenMalformed, state, "a CRC-failing family token must be reported as malformed")
		assert.Nil(t, tok, "no usable token accompanies a malformed state")
	})

	t.Run("absent when unset", func(t *testing.T) {
		t.Setenv(EnvVarToken, "")

		tok, state := EnvTokenFor(ep)
		assert.Equal(t, EnvTokenAbsent, state)
		assert.Nil(t, tok)
	})

	t.Run("absent when bound to a different endpoint", func(t *testing.T) {
		// Malformed for its OWN endpoint, but it is not bound to the one asked
		// about — so callers must be free to fall back, not fail closed.
		t.Setenv(EnvVarToken, "oxt_test_4bDZfX")
		t.Setenv("SAGEOX_ENDPOINT", "https://staging.sageox.ai")

		tok, state := EnvTokenFor(ep)
		assert.Equal(t, EnvTokenAbsent, state, "a token bound elsewhere was never meant for this endpoint")
		assert.Nil(t, tok)
	})

	t.Run("valid when bound and well formed", func(t *testing.T) {
		t.Setenv(EnvVarToken, "oxp_test_4bDZfN")

		tok, state := EnvTokenFor(ep)
		assert.Equal(t, EnvTokenValid, state)
		require.NotNil(t, tok)
		assert.Equal(t, "oxp_test_4bDZfN", tok.AccessToken)
	})
}

// TestEnvTokenIsTeamFamily — Failure prevented: status rendering picks
// personal-token guidance ("run ox login") for a refused TEAM token, which cannot
// be minted by ox login at all. The family must be readable even when the value
// is malformed, because a malformed value never produces a StoredToken to inspect.
func TestEnvTokenIsTeamFamily(t *testing.T) {
	const ep = "https://api.sageox.ai/"

	t.Run("malformed team token still reports team family", func(t *testing.T) {
		t.Setenv(EnvVarToken, "oxt_test_4bDZfX")
		assert.True(t, EnvTokenIsTeamFamily(ep))
	})

	t.Run("personal token is not team family", func(t *testing.T) {
		t.Setenv(EnvVarToken, "oxp_test_4bDZfN")
		assert.False(t, EnvTokenIsTeamFamily(ep))
	})

	t.Run("unset is not team family", func(t *testing.T) {
		t.Setenv(EnvVarToken, "")
		assert.False(t, EnvTokenIsTeamFamily(ep))
	})

	t.Run("team token bound to a different endpoint is not team family here", func(t *testing.T) {
		t.Setenv(EnvVarToken, validSageOxTestToken(TeamTokenPrefix, "svc"))
		t.Setenv("SAGEOX_ENDPOINT", "https://staging.sageox.ai")
		assert.False(t, EnvTokenIsTeamFamily(ep))
	})
}
