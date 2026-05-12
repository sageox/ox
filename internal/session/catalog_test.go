package session

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCatalogIdentity_DeterministicAndStableAcrossDeclarationOrder
// verifies that ScanForSecrets / RedactString consumers can rely on the
// (version, hash) pair to mean "same effective ruleset" regardless of
// the order in which pattern blocks happen to appear in DefaultPatterns.
//
// Failure prevented: a refactor that reorders patterns silently bumps
// the catalog hash and forces unnecessary re-redaction of every session.
func TestCatalogIdentity_DeterministicAndStableAcrossDeclarationOrder(t *testing.T) {
	r1 := NewRedactor()
	r2 := NewRedactor()
	v1, h1 := r1.CatalogVersion(), r1.CatalogHash()
	v2, h2 := r2.CatalogVersion(), r2.CatalogHash()
	assert.Equal(t, v1, v2, "two fresh Redactors must share a catalog version")
	assert.Equal(t, h1, h2, "two fresh Redactors must share a catalog hash")

	// Reorder the patterns slice manually; identity must NOT change.
	patterns := DefaultPatterns()
	reversed := make([]SecretPattern, len(patterns))
	for i, p := range patterns {
		reversed[len(patterns)-1-i] = p
	}
	r3 := NewRedactorWithPatterns(reversed)
	assert.Equal(t, h1, r3.CatalogHash(),
		"catalog hash MUST be order-independent across the pattern slice")
}

// TestCatalogIdentity_ChangesWhenPatternAdded ensures that adding a
// detector through the documented AddPattern path bumps both the
// version and the hash. Without this, ox-8bfh's audit replay can't
// know whether a session was scanned with the new detector.
func TestCatalogIdentity_ChangesWhenPatternAdded(t *testing.T) {
	r := NewRedactor()
	beforeV, beforeH := r.CatalogVersion(), r.CatalogHash()

	r.AddPattern(SecretPattern{
		Name:     "test_extra_pattern",
		Pattern:  regexp.MustCompile(`extra-[A-Za-z0-9]{8}`),
		Redact:   "[REDACTED_EXTRA]",
		Keywords: []string{"extra-"},
	})

	assert.NotEqual(t, beforeH, r.CatalogHash(),
		"adding a pattern must change the catalog hash")
	assert.NotEqual(t, beforeV, r.CatalogVersion(),
		"adding a pattern must change the catalog version (count or hash prefix bumps)")
}

// TestCatalogIdentity_HashSensitiveToRuleTampering proves that swapping
// the regex of a single detector (without changing names or count)
// still bumps the hash. This is the key property that makes the version
// alone insufficient — an in-process modification that "loosens" a
// detector must be detectable by a downstream auditor.
func TestCatalogIdentity_HashSensitiveToRuleTampering(t *testing.T) {
	pristine := DefaultPatterns()
	tampered := make([]SecretPattern, len(pristine))
	copy(tampered, pristine)
	// Find heroku_key and remove its Keywords guard — the exact failure
	// mode ox-zukx had to fix. Tampering should be detectable.
	for i := range tampered {
		if tampered[i].Name == "heroku_key" {
			tampered[i].Keywords = nil
			break
		}
	}

	rOK := NewRedactorWithPatterns(pristine)
	rTampered := NewRedactorWithPatterns(tampered)

	require.NotEqual(t, rOK.CatalogHash(), rTampered.CatalogHash(),
		"removing a Keywords guard must change the catalog hash")
}

// TestCatalogIdentity_VersionFormatIsGreppable locks the version string
// shape so log search / commit messages can find it later. Format:
// "ox-secrets-N<count>-<8 hex chars>".
func TestCatalogIdentity_VersionFormatIsGreppable(t *testing.T) {
	r := NewRedactor()
	v := r.CatalogVersion()
	assert.Regexp(t, `^ox-secrets-N\d+-[0-9a-f]{8}$`, v)
}
