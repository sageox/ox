package auth

import (
	"os"
	"strings"
	"unicode/utf8"
)

// EnvVarNoDeviceLabel disables sending a device label with `ox login`.
//
// Set to any value other than "0"/"false"/"" to opt out. With the field's
// `omitempty` tag, opting out makes the request body byte-identical to
// what ox sent before device labels existed.
//
// Consumed by: deviceLabel()
//
// Customer-facing env vars are SAGEOX_*; a customer-facing OX_* var is an
// anti-pattern (see sageox-mono ADR-047, and the guard in
// env_naming_test.go).
const EnvVarNoDeviceLabel = "SAGEOX_NO_DEVICE_LABEL"

const (
	// maxDeviceLabelUser caps the username portion.
	maxDeviceLabelUser = 32
	// maxDeviceLabelHost is the DNS label maximum, so no legal hostname
	// is ever truncated.
	maxDeviceLabelHost = 63
)

// deviceLabel returns a short, human-readable identifier for this machine
// in the form "user@host", for display in the account's CLI-sessions list.
//
// # What this is for
//
// `/settings/security` lists every PAT minted by `ox login` with nothing
// but timestamps. Someone with a laptop, a desktop, a devcontainer and a
// CI runner cannot tell which row to revoke.
//
// # What it actually contains
//
// The local OS account name plus the short hostname. That identifies the
// machine AND the account on it — for a single-user laptop, a person. It
// is not anonymous. What makes it acceptable is that the device-flow
// exchange is already authenticated: the server knows who is logging in
// before the label arrives, so the label adds "which device" without
// adding any linkability it did not already have. The residual
// disclosure is the account name itself, which is what the opt-out and
// the first-DNS-label reduction exist to bound.
//
// # Privacy
//
// This is auth-session metadata attached to a credential the user is
// creating in that moment, stored against their own account and shown
// back to that same user. It is NOT telemetry, so the "we never collect
// machine identifiers" rule in docs/adr/008-privacy.md — which is scoped
// to aggregate cross-user analytics, where a stable machine id is
// dangerous precisely because it de-anonymizes — does not apply. The ADR's
// "easy opt-out" principle does, and EnvVarNoDeviceLabel satisfies it.
//
// Deliberately NOT used here:
//   - signature.GetMachineID() — it is the HMAC key for the local
//     signature cache, so transmitting it would leak a local secret, and
//     it embeds a random UUID that means nothing in a UI.
//   - identity.AttributionName — documented LOCAL ONLY.
//   - identity.AttributionUsername — resolves through git config, so the
//     label would depend on which directory `ox login` was run from.
//
// Returns "" when opted out or when nothing usable can be resolved; the
// caller's `omitempty` then drops the field entirely.
func deviceLabel() string {
	if optedOutOfDeviceLabel() {
		return ""
	}

	host, _ := os.Hostname()
	// first DNS label only: turns "Laptop.local" into "Laptop" and drops
	// corporate ".internal"/".lan" domains, which identify an employer far
	// more than they identify a machine.
	host, _, _ = strings.Cut(host, ".")
	host = sanitizeLabelPart(host, maxDeviceLabelHost)

	user := os.Getenv("USER")
	if user == "" {
		user = os.Getenv("USERNAME") // Windows
	}
	user = sanitizeLabelPart(user, maxDeviceLabelUser)

	switch {
	case user != "" && host != "":
		return user + "@" + host
	case host != "":
		return host
	default:
		return user
	}
}

// optedOutOfDeviceLabel reports whether the user disabled device labels.
// Set-but-falsey values ("0", "false") count as opted IN, so a scripted
// `SAGEOX_NO_DEVICE_LABEL=0` behaves the way it reads.
func optedOutOfDeviceLabel() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvVarNoDeviceLabel))) {
	case "", "0", "false", "no":
		return false
	default:
		return true
	}
}

// sanitizeLabelPart reduces s to [A-Za-z0-9._-], collapses runs of "-",
// trims leading/trailing separators, and truncates to maxRunes.
//
// Allowlist, not denylist. This character set is the intersection of what
// is safe in a GitLab PAT name, an HTTP header value, a JSON string and a
// URL path segment — so a hostname, which is attacker-influenceable on a
// shared box, cannot smuggle a CRLF or a quote anywhere downstream.
// Everything below 0x20, 0x7F, and all non-ASCII is excluded by
// construction rather than by enumeration.
func sanitizeLabelPart(s string, maxRunes int) string {
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
			lastDash = r == '-'
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}

	out := strings.Trim(b.String(), "-._")
	// truncate by rune even though the allowlist already leaves only
	// ASCII, so this stays correct if the allowlist ever widens.
	if utf8.RuneCountInString(out) > maxRunes {
		out = string([]rune(out)[:maxRunes])
		out = strings.Trim(out, "-._")
	}
	return out
}
