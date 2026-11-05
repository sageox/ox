package teamskills

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"path"
	"sort"
	"strings"
	"testing"
)

func TestDigest_CanonicalInventory(t *testing.T) {
	s := Skill{Name: "deploy", Files: []File{
		{Path: "./z", Content: []byte{0, ':', '\n'}},
		{Path: `a\b`, Content: md("one")},
	}}
	// Pins the version tag, byte lengths, file count, and canonical ordering.
	const want = "sha256:31c0ee20f77c471526facbde1682a7ede7411604394f25daefce3cc50e5d62cb"
	if got := Digest(s); got != want {
		t.Fatalf("digest = %s, want %s", got, want)
	}
	if s.Files[0].Path != "./z" || s.Files[1].Path != `a\b` {
		t.Fatal("digest mutated the caller's inventory")
	}
	s.Files[0], s.Files[1] = s.Files[1], s.Files[0]
	if Digest(s) != want {
		t.Fatal("reordering files changed the digest")
	}
	s.Files[0].Path = "a/b"
	s.Files[1].Path = "z"
	if Digest(s) != want {
		t.Fatal("equivalent canonical paths changed the digest")
	}
}

func TestDigest_DelimiterCollisionNeedsFreshApproval(t *testing.T) {
	const script = "echo unreviewed\n"
	// This regular POSIX filename used to encode three file records: a, b.sh,
	// and c. The approved inventory has only an inert file with that name.
	oddPath := fmt.Sprintf("a:0\nfile:b.sh:%d\n%sfile:c", len(script), script)
	original := prose("deploy")
	original.Files = append(original.Files,
		File{Path: oddPath},
		File{Path: "scripts/run.sh", Content: md("echo reviewed\n")},
	)
	changed := prose("deploy")
	changed.Files = append(changed.Files,
		File{Path: "a"},
		File{Path: "b.sh", Content: md(script)},
		File{Path: "c"},
		File{Path: "scripts/run.sh", Content: md("echo reviewed\n")},
	)
	if !bytes.Equal(legacyDigestInput(original), legacyDigestInput(changed)) {
		t.Fatal("fixture does not reproduce the legacy framing collision")
	}
	before, after := Classify(original), Classify(changed)
	if before.Digest == after.Digest {
		t.Fatal("different inventories with delimiter-bearing paths share a digest")
	}
	store := &ApprovalStore{}
	store.Approve(original.Name, before, true)
	if !store.ScriptsExecutable(original.Name, before) {
		t.Fatal("the original script grant was not recorded")
	}
	if store.Decide(changed.Name, after) != DecisionNeedsApproval {
		t.Fatal("a different inventory inherited the prior approval")
	}
	if store.ScriptsExecutable(changed.Name, after) {
		t.Fatal("an unreviewed script inherited the prior script grant")
	}
	store.Approve(changed.Name, after, true)
	if store.Decide(changed.Name, after) != DecisionMaterialize || !store.ScriptsExecutable(changed.Name, after) {
		t.Fatal("a fresh script approval was not honored")
	}
}

func TestDigest_LegacyApprovalRequiresFreshGrant(t *testing.T) {
	s := executableSkill("deploy", "echo reviewed\n")
	v := Classify(s)
	store := &ApprovalStore{Approvals: []Approval{{
		Name: s.Name, Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(legacyDigestInput(s))), AllowScripts: true,
	}}}
	if store.Decide(s.Name, v) != DecisionNeedsApproval || store.ScriptsExecutable(s.Name, v) {
		t.Fatal("a legacy digest still authorizes executable content, even without an inventory change")
	}
	store.Approve(s.Name, v, false)
	if store.Decide(s.Name, v) != DecisionMaterialize || store.ScriptsExecutable(s.Name, v) {
		t.Fatal("a fresh manifest-only approval carried forward the legacy script grant")
	}
	store.Approve(s.Name, v, true)
	if !store.ScriptsExecutable(s.Name, v) {
		t.Fatal("a fresh explicit script grant was not honored")
	}
}

// legacyDigestInput reproduces the old framing only to prove the collision and
// that stored approvals from that format no longer match current verdicts.
func legacyDigestInput(s Skill) []byte {
	files := append([]File(nil), s.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	var input bytes.Buffer
	fmt.Fprintf(&input, "skill:%s\n", s.Name)
	for _, f := range files {
		fmt.Fprintf(&input, "file:%s:%d\n", path.Clean(strings.ReplaceAll(f.Path, "\\", "/")), len(f.Content))
		input.Write(f.Content)
	}
	return input.Bytes()
}

func TestDigest_CanonicalPathTiesAreOrderIndependent(t *testing.T) {
	a := Skill{Name: "s", Files: []File{{Path: "a/b", Content: []byte("one")}, {Path: "a\\b", Content: []byte("two")}}}
	b := Skill{Name: "s", Files: []File{a.Files[1], a.Files[0]}}
	if Digest(a) != Digest(b) {
		t.Fatal("reordering files that canonicalize alike must not change the digest")
	}
}
