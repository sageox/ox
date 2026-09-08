package auth

import (
	"encoding/binary"
	"hash/crc32"
	"log/slog"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
)

// EnvVarToken is the environment variable for supplying a SageOx access token
// out-of-band (CI/CD, headless agents, ephemeral containers). When set, it takes
// precedence over any token stored on disk.
const EnvVarToken = "SAGEOX_TOKEN"

var ErrReadTokenUnavailable = gitserver.ErrReadTokenUnavailable

// CurrentReadToken never consults a disk login or GitLab credential store.
// Re-read it for every operation so a reused client observes token rotation.
// Callers supply the selected canonical origin; normalizing a requested sibling
// host here would broaden the authority allowed to receive this credential.
func CurrentReadToken(endpointURL string) (string, error) {
	if strings.TrimSuffix(endpointURL, "/") != strings.TrimSuffix(EnvTokenEndpoint(), "/") {
		return "", ErrReadTokenUnavailable
	}
	tok, state := EnvTokenFor(endpointURL)
	if state != EnvTokenValid || tok == nil || !strings.HasPrefix(tok.AccessToken, TeamTokenPrefix) || strings.ContainsAny(tok.AccessToken, "\r\n") {
		return "", ErrReadTokenUnavailable
	}
	return tok.AccessToken, nil
}

// envTokenTTL is the synthetic rolling expiry stamped on env-sourced tokens.
// Env tokens have no refresh credential — the server returning 401 is the source
// of truth for invalidation. A 24h rolling TTL keeps IsExpired() honest without
// triggering refresh paths.
const envTokenTTL = 24 * time.Hour

// IsEnvTokenEndpoint reports whether the credential in play for ep came from
// SAGEOX_TOKEN rather than auth.json. Display code needs this because an
// env-sourced token carries no UserInfo — there was no login to learn a name or
// email from — so a renderer that assumes those fields are populated prints a
// blank identity next to a green check.
func IsEnvTokenEndpoint(ep string) bool {
	return tokenFromEnv(ep) != nil
}

// isEnvToken reports whether the given token was sourced from the environment.
// Env tokens have no refresh credential and the server returning 401 is the
// source of truth for invalidation — callers must never attempt to refresh one.
func isEnvToken(ep string, token *StoredToken) bool {
	if token == nil {
		return false
	}
	if token.RefreshToken != "" || token.SessionToken != "" {
		return false
	}
	envTok := tokenFromEnv(ep)
	if envTok == nil {
		return false
	}
	return token.AccessToken == envTok.AccessToken
}

// envTokenEndpoint returns the single endpoint an env-supplied token is allowed
// to target in this process. SAGEOX_TOKEN is not self-describing client-side, so we
// bind it to the explicit endpoint selection surface only:
//   - SAGEOX_ENDPOINT when set
//   - production by default
//
// We intentionally do NOT inherit endpoint.Get() here because that function can
// fall back to "the only logged-in endpoint", which would let a prod SAGEOX_TOKEN
// silently ride along to a different host if disk auth happened to be sparse.
func envTokenEndpoint() string {
	if ep := os.Getenv(endpoint.EnvVar); ep != "" {
		return endpoint.NormalizeEndpoint(ep)
	}
	return endpoint.Default
}

// EnvTokenEndpoint is the exported view of envTokenEndpoint, for display code that
// must ask about SAGEOX_TOKEN's state before it has an endpoint to ask about.
//
// GetLoggedInEndpoints lists endpoints with a USABLE credential, so a malformed
// SAGEOX_TOKEN contributes nothing to it — and with no disk login behind it, the
// endpoint disappears from that list entirely. A renderer that infers env-token
// state from the list would therefore report a truncated token as a plain "not
// logged in", which is the diagnostic gap the local format check exists to close.
// Ask EnvTokenFor(EnvTokenEndpoint()) directly instead.
func EnvTokenEndpoint() string {
	return envTokenEndpoint()
}

// EnvTokenState describes what SAGEOX_TOKEN holds for a given endpoint.
type EnvTokenState int

const (
	// EnvTokenAbsent: SAGEOX_TOKEN is unset, or is bound to a different endpoint.
	EnvTokenAbsent EnvTokenState = iota
	// EnvTokenValid: present, bound to this endpoint, and passes the local format check.
	EnvTokenValid
	// EnvTokenMalformed: present and bound to this endpoint, but it is a SageOx-family
	// value (oxp_/oxt_) whose CRC suffix does not match. The operator declared an
	// intent to use THIS credential; callers must fail closed rather than silently
	// falling back to a different one.
	EnvTokenMalformed
)

// normalizeEnvToken cleans an environment-supplied token value before any
// classification runs.
//
// Env vars are a lossy transport for secrets. `export SAGEOX_TOKEN=$(cat secret)`,
// Kubernetes secrets mounted from files, YAML block scalars, `docker --env-file`,
// and most `.env` parsers all routinely deliver a trailing newline, a surrounding
// pair of quotes, or an accidental `Bearer ` prefix copied out of an HTTP example.
// Before normalization existed, tail corruption was reported to the user as
// "likely a typo" and head corruption bypassed the format gate entirely and then
// 401'd at the server with no hint that the value itself was the problem.
//
// This is a client-side ergonomic decision only. It does NOT touch the CRC
// algorithm and does not weaken the format check: the cleaned value must still
// pass the same checksum every server-minted token satisfies.
func normalizeEnvToken(raw string) string {
	val := strings.TrimSpace(raw)

	// One layer of matched surrounding quotes only — a quote that appears on just
	// one side is real corruption, not shell quoting, and must stay visible.
	if len(val) >= 2 {
		if q := val[0]; (q == '"' || q == '\'') && val[len(val)-1] == q {
			val = strings.TrimSpace(val[1 : len(val)-1])
		}
	}

	const bearer = "Bearer "
	if len(val) >= len(bearer) && strings.EqualFold(val[:len(bearer)], bearer) {
		val = strings.TrimSpace(val[len(bearer):])
	}

	return val
}

// envTokenBoundValue returns the normalized SAGEOX_TOKEN value when it is bound to
// ep, or "" when the var is unset, empty after normalization, or bound elsewhere.
//
// The env token is bound to exactly one endpoint. A normalized-empty endpoint
// (e.g. malformed input like "api.") has no anchor to match against, so it is
// treated as a non-match rather than handing out the "*"-scoped token to an
// unverifiable target.
func envTokenBoundValue(ep string) string {
	val := normalizeEnvToken(os.Getenv(EnvVarToken))
	if val == "" {
		return ""
	}
	if requested := endpoint.NormalizeEndpoint(ep); requested == "" || requested != envTokenEndpoint() {
		return ""
	}
	return val
}

// EnvTokenFor reports both the usable env token (nil unless EnvTokenValid) and why.
//
// Distinguishing malformed from absent is the point: an absent token means "fall
// back to disk", while a malformed one means the operator named a specific
// credential that we cannot use — silently authenticating as somebody else is the
// wrong failure direction.
func EnvTokenFor(ep string) (*StoredToken, EnvTokenState) {
	val := envTokenBoundValue(ep)
	if val == "" {
		return nil, EnvTokenAbsent
	}
	// A SageOx-family token (oxp_/oxt_) carries a CRC suffix; a mismatch means
	// a typo or a truncated copy/paste, and sending it to the server would
	// only earn a generic 401 with no hint that the token itself is the
	// problem. Catch it here instead. Opaque OAuth tokens and JWTs have no
	// such grammar — the server is the only source of truth for those, so
	// they skip this check entirely and fall through to normal validation.
	if isSageOxTokenFormat(val) && !verifyTokenChecksum(val) {
		slog.Warn("SAGEOX_TOKEN failed local checksum check — likely a typo or truncated copy/paste",
			"family", tokenFamily(val),
			"length_class", tokenLengthClass(val),
		)
		return nil, EnvTokenMalformed
	}
	return &StoredToken{
		// Store the NORMALIZED value so a clean credential goes on the wire —
		// a trailing newline in an Authorization header is itself a protocol error.
		AccessToken:  val,
		RefreshToken: "",
		SessionToken: "",
		ExpiresAt:    time.Now().Add(envTokenTTL),
		TokenType:    "Bearer",
		Scope:        "*",
		// UserInfo is zero-valued — filled lazily on first server response that
		// includes claims.
	}, EnvTokenValid
}

// EnvTokenIsTeamFamily reports whether the raw SAGEOX_TOKEN value bound to ep carries
// the team prefix. Used by status rendering to pick family-aware guidance WITHOUT
// requiring a parsed StoredToken (a malformed token never produces one).
func EnvTokenIsTeamFamily(ep string) bool {
	return strings.HasPrefix(envTokenBoundValue(ep), TeamTokenPrefix)
}

// tokenFromEnv returns a StoredToken populated from SAGEOX_TOKEN when the
// requested endpoint matches envTokenEndpoint(). Returns nil when the env var
// is unset, when the request is for a different endpoint, or when the value
// fails the local checksum precheck below.
func tokenFromEnv(ep string) *StoredToken {
	tok, _ := EnvTokenFor(ep)
	return tok
}

// isSageOxTokenFormat reports whether tok carries one of our own bearer-token
// prefixes (oxp_ personal, oxt_ team) — i.e. whether verifyTokenChecksum's CRC
// grammar even applies to it.
func isSageOxTokenFormat(tok string) bool {
	return strings.HasPrefix(tok, PATPrefix) || strings.HasPrefix(tok, TeamTokenPrefix)
}

// tokenFamily returns ONLY the family prefix of tok (oxp_/oxt_), never any of
// its body.
//
// An earlier version of this diagnostic emitted eight characters — the prefix
// plus four body characters. That was a mistake worth naming, because the
// reasoning behind it was seductive: a value that failed its own checksum is
// probably not a working credential, so why protect it? Because the most
// common way to reach this branch is a TRUNCATED PASTE of a real one, and the
// leading body characters of a truncated real token are leading body
// characters of a real token. Paired with an exact byte length (see
// tokenLengthClass) that is a materially narrowed search space, written to
// stderr and retained by CI log storage indefinitely.
//
// The family alone is what the operator actually needs here: it tells them
// whether to go re-copy a personal token or a team token, which is the only
// decision this message drives.
func tokenFamily(tok string) string {
	switch {
	case strings.HasPrefix(tok, PATPrefix):
		return PATPrefix
	case strings.HasPrefix(tok, TeamTokenPrefix):
		return TeamTokenPrefix
	default:
		return "unknown"
	}
}

// tokenLengthClass buckets tok's length so the diagnostic can say "this looks
// truncated" without publishing an exact byte count an attacker could use to
// bound a brute force.
func tokenLengthClass(tok string) string {
	switch n := len(tok); {
	case n <= 20:
		return "short"
	case n <= 80:
		return "expected"
	default:
		return "long"
	}
}

// --- CRC format precheck ---
//
// base62Encode, crc32Base62, and verifyTokenChecksum are intended to reproduce
// the server's token package: the same algorithm, alphabet, and big-endian
// CRC-32 (IEEE 802.3) encoding, so a token minted server-side always checksums
// correctly here. The golden vectors and length-class table in env_token_test.go
// pin the behavior this side of the wire; see those tests for exactly how much
// they do and do not prove about the server's side of it.
//
// This is a format-integrity check (typo/truncation detection) only, NEVER a
// security control — never gate real access on it.

const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// base62Encode treats b as a big-endian unsigned integer and emits base62 digits.
//
// Note the padding contract, which is the fragile part of this mirror: output is
// VARIABLE length and is not left-padded, so a CRC below 62^5 encodes to fewer
// than 6 characters and leading zero bytes are simply absorbed by the big-integer
// conversion. The sole exception is an all-zero input, which returns len(b)
// zeros. If the server instead pads to a fixed width, or preserves leading zero
// bytes the way base58check does, this function rejects every token whose CRC is
// below 62^5 — about 21% of the keyspace — as a "typo". TestBase62Encode_LengthClasses
// makes that contract explicit across every length class.
func base62Encode(b []byte) string {
	n := new(big.Int).SetBytes(b)
	if n.Sign() == 0 {
		if len(b) == 0 {
			return "0"
		}
		return strings.Repeat("0", len(b))
	}
	base := big.NewInt(62)
	mod := new(big.Int)
	buf := make([]byte, 0, len(b)*2)
	for n.Sign() > 0 {
		n.DivMod(n, base, mod)
		buf = append(buf, base62Alphabet[mod.Int64()])
	}
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}

// crc32Base62 is CRC-32 (IEEE 802.3) of the input, big-endian, base62-encoded.
func crc32Base62(input string) string {
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, crc32.ChecksumIEEE([]byte(input)))
	return base62Encode(buf)
}

// verifyTokenChecksum reports whether tok's trailing "_<crc>" suffix matches
// the CRC of everything before it.
func verifyTokenChecksum(tok string) bool {
	i := strings.LastIndex(tok, "_")
	if i <= 0 || i == len(tok)-1 {
		return false
	}
	withPrefix, crc := tok[:i], tok[i+1:]
	return crc32Base62(withPrefix) == crc
}
