package adapterstamp

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/agentx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stampedRule builds on-disk rule bytes the way agentx's buildContent does for a
// rule with a Description: YAML frontmatter, then the stamp line (covering only
// the body that follows), then the body. This is the exact shape these helpers
// must see past — the stamp is NOT on line 1.
func stampedRule(body string) []byte {
	stampLine := fmt.Sprintf("%s%s ver: %s -->\n",
		agentx.StampComment(agentx.DefaultStampPrefix),
		agentx.ContentHash([]byte(body)), "0.8.0")
	return []byte("---\ndescription: x\n---\n" + stampLine + body)
}

// --- A. ExtractStampAnywhere ---

// TestExtractStampAnywhere_BelowFrontmatter verifies the stamp is found when it
// sits after YAML frontmatter (not on line 1), and that the returned body is the
// content the hash covers.
// Failure prevented: a frontmatter'd rule reads as unstamped, so every drift
// check silently no-ops.
func TestExtractStampAnywhere_BelowFrontmatter(t *testing.T) {
	body := "real body content\n"
	data := stampedRule(body)

	hash, ver, gotBody := ExtractStampAnywhere(data, agentx.DefaultStampPrefix)

	require.NotEmpty(t, hash, "stamp below frontmatter must be found")
	assert.Equal(t, agentx.ContentHash([]byte(body)), hash, "hash must be the stamped body hash")
	assert.Equal(t, "0.8.0", ver, "version must be parsed from the stamp line")
	assert.Equal(t, body, string(gotBody), "body must be the content after the stamp line")
}

// TestExtractStampAnywhere_Unstamped verifies an unstamped (user-managed) file
// returns empty results so callers treat it as not-ours.
// Failure prevented: helpers touch/flag user-authored files that ox never wrote.
func TestExtractStampAnywhere_Unstamped(t *testing.T) {
	hash, ver, body := ExtractStampAnywhere([]byte("# user content, no stamp\n"), agentx.DefaultStampPrefix)
	assert.Empty(t, hash)
	assert.Empty(t, ver)
	assert.Nil(t, body)
}

// TestExtractStampAnywhere_PrefixMismatch verifies passing a different prefix
// than what's on disk does not match.
// Failure prevented: a command-stamped file is mistaken for a rule-stamped one.
func TestExtractStampAnywhere_PrefixMismatch(t *testing.T) {
	data := stampedRule("body\n") // stamped with DefaultStampPrefix
	hash, _, _ := ExtractStampAnywhere(data, "ox")
	assert.Empty(t, hash, "DefaultStampPrefix-stamped content must not match the ox prefix")
}

// --- B. LooksStamped ---

// TestLooksStamped verifies the boolean convenience wrapper agrees with
// ExtractStampAnywhere for both stamped and unstamped content.
// Failure prevented: uninstall skips ox files (or deletes user files) because
// stamp detection disagrees with extraction.
func TestLooksStamped(t *testing.T) {
	assert.True(t, LooksStamped(stampedRule("body\n")), "frontmatter'd stamped file must look stamped")
	assert.False(t, LooksStamped([]byte("no stamp here\n")), "unstamped file must not look stamped")
}

// --- C. RemoveTamperedRules ---

// TestRemoveTamperedRules_RemovesEditedBody verifies a stamped file whose body
// was hand-edited (so it no longer matches its own stamp) is removed so a
// subsequent Install rewrites it fresh.
// Failure prevented: `--fix` is a no-op on tampered files because agentx's
// matching-stamp short-circuit never fires the rewrite.
func TestRemoveTamperedRules_RemovesEditedBody(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ox.md")
	// stamp covers "original body" but the on-disk body is "tampered body".
	tampered := stampedRule("original body\n")
	tampered = append(tampered, []byte("tampered body\n")...)
	require.NoError(t, os.WriteFile(path, tampered, 0o644))

	RemoveTamperedRules(dir, []agentx.RuleFile{{Name: "ox.md"}})

	_, err := os.Stat(path)
	assert.True(t, os.IsNotExist(err), "tampered stamped file must be removed")
}

// TestRemoveTamperedRules_KeepsCleanAndUserFiles verifies a file whose body still
// matches its stamp, and a user-managed (unstamped) file, are both left in place.
// Failure prevented: removing files that are clean (churn) or user-authored (data
// loss).
func TestRemoveTamperedRules_KeepsCleanAndUserFiles(t *testing.T) {
	dir := t.TempDir()
	clean := filepath.Join(dir, "clean.md")
	user := filepath.Join(dir, "user.md")
	require.NoError(t, os.WriteFile(clean, stampedRule("body\n"), 0o644))
	require.NoError(t, os.WriteFile(user, []byte("# user content\n"), 0o644))

	RemoveTamperedRules(dir, []agentx.RuleFile{{Name: "clean.md"}, {Name: "user.md"}})

	_, err := os.Stat(clean)
	assert.NoError(t, err, "clean stamped file must be preserved")
	_, err = os.Stat(user)
	assert.NoError(t, err, "user-managed unstamped file must be preserved")
}

// --- D. AppendFrontmatterStale ---

// TestAppendFrontmatterStale_BodyTampered verifies a frontmatter'd rule whose
// body drifted from its own stamp is reported stale.
// Failure prevented: a tampered rule passes doctor silently (Bug 2).
func TestAppendFrontmatterStale_BodyTampered(t *testing.T) {
	dir := t.TempDir()
	tampered := stampedRule("original\n")
	tampered = append(tampered, []byte("drift\n")...)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ox.md"), tampered, 0o644))

	stale := AppendFrontmatterStale(dir,
		[]agentx.RuleFile{{Name: "ox.md", Content: []byte("original\n"), Version: "0.8.0"}},
		nil, nil)

	assert.Contains(t, stale, "ox.md", "tampered body must be reported stale")
}

// TestAppendFrontmatterStale_BinaryDrift verifies a rule whose stamp matches its
// on-disk body but differs from the live binary's content is reported stale.
// Failure prevented: an upgraded binary never re-installs drifted Layer-2 guidance.
func TestAppendFrontmatterStale_BinaryDrift(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ox.md"), stampedRule("old body\n"), 0o644))

	stale := AppendFrontmatterStale(dir,
		[]agentx.RuleFile{{Name: "ox.md", Content: []byte("new body\n"), Version: "0.9.0"}},
		nil, nil)

	assert.Contains(t, stale, "ox.md", "binary drift must be reported stale")
}

// TestAppendFrontmatterStale_DowngradeGuard verifies a rule installed by a NEWER
// binary is not reported stale by an OLDER one (version-downgrade guard).
// Failure prevented: an older binary clobbers newer guidance on every check.
func TestAppendFrontmatterStale_DowngradeGuard(t *testing.T) {
	dir := t.TempDir()
	// on-disk stamp says ver 0.9.0; live binary is older (0.8.0).
	body := "installed by newer binary\n"
	stampLine := fmt.Sprintf("%s%s ver: %s -->\n",
		agentx.StampComment(agentx.DefaultStampPrefix), agentx.ContentHash([]byte(body)), "0.9.0")
	onDisk := []byte("---\ndescription: x\n---\n" + stampLine + body)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ox.md"), onDisk, 0o644))

	stale := AppendFrontmatterStale(dir,
		[]agentx.RuleFile{{Name: "ox.md", Content: []byte("older content\n"), Version: "0.8.0"}},
		nil, nil)

	assert.NotContains(t, stale, "ox.md", "rule installed by newer binary must not be stale for an older one")
}

// TestAppendFrontmatterStale_SkipsUserManaged verifies an unstamped file is never
// flagged stale.
// Failure prevented: doctor loops forever flagging a user-owned file it can't fix.
func TestAppendFrontmatterStale_SkipsUserManaged(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ox.md"), []byte("# user content\n"), 0o644))

	stale := AppendFrontmatterStale(dir,
		[]agentx.RuleFile{{Name: "ox.md", Content: []byte("anything\n"), Version: "0.8.0"}},
		nil, nil)

	assert.NotContains(t, stale, "ox.md", "unstamped user-managed file must never be flagged stale")
}

// TestAppendFrontmatterStale_SkipsAlreadyFlagged verifies a rule already in the
// missing/stale lists is not duplicated.
// Failure prevented: a rule appears twice in the stale list, inflating doctor output.
func TestAppendFrontmatterStale_SkipsAlreadyFlagged(t *testing.T) {
	dir := t.TempDir()
	tampered := stampedRule("original\n")
	tampered = append(tampered, []byte("drift\n")...)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ox.md"), tampered, 0o644))

	stale := AppendFrontmatterStale(dir,
		[]agentx.RuleFile{{Name: "ox.md", Content: []byte("original\n"), Version: "0.8.0"}},
		nil, []string{"ox.md"})

	// "ox.md" was already stale; it must appear exactly once.
	count := 0
	for _, n := range stale {
		if n == "ox.md" {
			count++
		}
	}
	assert.Equal(t, 1, count, "already-flagged rule must not be appended again")
}
