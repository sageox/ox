package session

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// Catalog identity for the redactor's currently-loaded pattern set.
//
// "Catalog" here means the live SecretPattern slice held by a Redactor —
// `internal/session/secrets.DefaultPatterns()` plus any AddPattern
// extensions stacked on top. Two pieces of identity are exposed:
//
//   - CatalogVersion is a stable, human-readable label tied to the
//     count and name set of patterns. Bumps when the production
//     catalog gains or loses a detector. NOT a security mechanism —
//     it can be coincidentally equal across mis-aligned builds; the
//     hash is what proves alignment.
//
//   - CatalogHash is a sha256 over the canonicalized pattern set:
//     name + regex source + sorted SkipIf + sorted Keywords for each
//     pattern, then sorted across patterns so declaration order in
//     source doesn't drift the hash. Tampering with the rule set
//     in-process (AddPattern, removing the heroku Keywords guard,
//     etc.) changes the hash even when the count and names match.
//
// The pair is recorded in lfs.RedactionPass on every redact-history
// run so future audits can answer "what rules actually scanned this
// session?" without trusting an unsigned version string in isolation.
//
// Identity is computed eagerly during Redactor construction and
// recomputed under the existing write lock whenever AddPattern
// mutates the set. Read-side accessors take the read lock and return
// the cached values — no compute on the hot path.

// canonicalPattern is a stable text representation of one SecretPattern,
// used as input to the catalog hash. Empty Pattern is skipped at the
// caller; SkipIf and Keywords are sorted so insertion order doesn't
// drift the hash.
func canonicalPattern(p SecretPattern) string {
	skip := append([]string(nil), p.SkipIf...)
	sort.Strings(skip)
	kws := append([]string(nil), p.Keywords...)
	sort.Strings(kws)
	regex := ""
	if p.Pattern != nil {
		regex = p.Pattern.String()
	}
	return fmt.Sprintf("name=%s\nregex=%s\nskip=%s\nkeywords=%s\n",
		p.Name, regex, strings.Join(skip, ","), strings.Join(kws, ","))
}

// computeCatalogIdentity returns (version, hash) for the given pattern
// set. Deterministic — same inputs always produce the same outputs,
// regardless of pattern declaration order in source.
func computeCatalogIdentity(patterns []SecretPattern) (string, string) {
	canon := make([]string, 0, len(patterns))
	for _, p := range patterns {
		if p.Pattern == nil {
			continue
		}
		canon = append(canon, canonicalPattern(p))
	}
	sort.Strings(canon) // hash is order-independent
	h := sha256.New()
	for _, c := range canon {
		h.Write([]byte(c))
		h.Write([]byte{0}) // delimiter so concatenation can't collide
	}
	hash := hex.EncodeToString(h.Sum(nil))

	// Version: pattern count + 8-char hash prefix. Bumps automatically
	// whenever the set changes, but stays stable across rebuilds with
	// identical rules. Format chosen to be greppable in commit history
	// and short enough to print in a status header.
	version := fmt.Sprintf("ox-secrets-N%d-%s", len(canon), hash[:8])
	return version, hash
}

// CatalogVersion returns the human-readable catalog identifier for the
// Redactor's current pattern set. Bumps automatically when patterns are
// added or removed. See package doc above for the trust model.
func (r *Redactor) CatalogVersion() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.catalogVersion
}

// CatalogHash returns a sha256 hex string fingerprint of the Redactor's
// current pattern set. Combined with CatalogVersion this lets persisted
// records prove which detectors were in effect at scan time.
func (r *Redactor) CatalogHash() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.catalogHash
}
