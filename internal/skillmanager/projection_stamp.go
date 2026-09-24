package skillmanager

import (
	"bytes"
	"crypto/sha256"
	"fmt"
)

// ProjectionStamp is the in-band ownership trailer ox appends to a Team Context
// projection.
//
// It answers one question no other mechanism can: "did ox write this file?" —
// WITHOUT a machine-local inventory. That matters because the inventory
// (.sageox/cache/skills-state.json) is disposable by design, while the projection
// it describes is gitignored and therefore invisible to git as well. Before the
// team suffix, a projection's reserved PREFIX answered the question instead, and
// the stamp existed only for Team Rules. "-team" is ordinary English and cannot
// carry that weight — someone may have hand-authored `notify-team` — so the stamp
// is now the ownership proof for team skills too.
//
// The trailer covers every byte before it, frontmatter included, so any local edit
// invalidates ownership and reconciliation preserves the file rather than
// silently restoring it.
//
// It lives beside the reserved-namespace contract in reserved.go because the two
// answer the same question by different means, and callers have to reason about
// them together.
type ProjectionStamp struct {
	Prefix string
	Suffix string
}

var (
	// TeamRuleStamp has been on every Team Rule projection since before the suffix.
	// Its exact bytes are load-bearing: changing them would orphan every rule
	// already on disk, which reconciliation would then report as a conflict.
	TeamRuleStamp = ProjectionStamp{
		Prefix: "<!-- ox-team-rule-sha256:",
		Suffix: "; managed by ox from Team Context; edit the source rule, not this projection. -->",
	}

	// TeamSkillStamp marks a Team Skill's SKILL.md. Only the manifest is stamped —
	// a skill's references and assets may be any format at all, including bytes a
	// trailing HTML comment would corrupt. The manifest is what claims the
	// DIRECTORY; the lockfile digests cover the files inside it.
	TeamSkillStamp = ProjectionStamp{
		Prefix: "<!-- ox-team-skill-sha256:",
		Suffix: "; managed by ox from Team Context; edit the source skill, not this projection. -->",
	}
)

// Apply returns content with the ownership trailer appended.
//
// Line endings are normalized first and a trailing newline is guaranteed, so the
// same source produces the same stamp on Windows and on Unix. Without that, a
// checkout with CRLF endings would compute a different hash from the one it wrote
// and declare its own file a conflict.
func (s ProjectionStamp) Apply(content []byte) []byte {
	content = bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))
	if len(content) == 0 || content[len(content)-1] != '\n' {
		content = append(content, '\n')
	}
	sum := sha256.Sum256(content)
	stamp := fmt.Sprintf("%s%x%s\n", s.Prefix, sum, s.Suffix)
	return append(append([]byte(nil), content...), []byte(stamp)...)
}

// Verifies reports whether content carries this stamp AND the stamp matches the
// bytes above it.
//
// Presence alone is never enough. A user who edits a projection and leaves the
// trailer in place would otherwise keep ox's ownership claim over content ox did
// not write — and ox would overwrite their edit on the next reconcile, which is
// exactly the silent data loss the digest exists to prevent.
func (s ProjectionStamp) Verifies(content []byte) bool {
	content = bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))
	marker := []byte("\n" + s.Prefix)
	markerOffset := bytes.LastIndex(content, marker)
	if markerOffset < 0 {
		return false
	}
	payload := content[:markerOffset+1]
	stamp := content[markerOffset+1:]
	wantLength := len(s.Prefix) + sha256.Size*2 + len(s.Suffix) + 1
	if len(stamp) != wantLength || !bytes.HasSuffix(stamp, []byte(s.Suffix+"\n")) {
		return false
	}
	wantHash := fmt.Sprintf("%x", sha256.Sum256(payload))
	gotHash := string(stamp[len(s.Prefix) : len(s.Prefix)+sha256.Size*2])
	return gotHash == wantHash
}
