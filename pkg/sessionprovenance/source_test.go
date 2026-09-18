package sessionprovenance

import "testing"

// TestSourcePathIsAgentNamespacedAndTraversalSafe pins the contract's one
// agent-dependent behavior. Codex paths and the Path wrapper stay
// byte-identical, so no existing caller moves; any other well-formed agent
// gets its own sibling directory rather than a rejection; and because the
// agent becomes a path element, anything that is not one flat segment is
// refused -- the sole traversal surface, the native ID being a canonical UUID.
// Failure prevented: a shared contract only Codex can use, or one that lets an
// agent value escape data/session-sources/.
func TestSourcePathIsAgentNamespacedAndTraversalSafe(t *testing.T) {
	id := "0197d3f4-2c88-7a15-a9b0-4b5c6d7e8f04"
	codex, err := SessionSourcePath("codex", id)
	if err != nil || codex != "data/session-sources/codex/"+id+".json" {
		t.Fatalf("codex path changed: %q, %v", codex, err)
	}
	if legacy, err := Path(id); err != nil || legacy != codex {
		t.Fatalf("Path wrapper must stay byte-identical to the codex path: %q, %v", legacy, err)
	}
	if claude, err := SessionSourcePath("claude-code", id); err != nil || claude != "data/session-sources/claude-code/"+id+".json" {
		t.Fatalf("another agent must get its own namespace, not a rejection: %q, %v", claude, err)
	}
	for _, agent := range []string{"", ".", "..", "../../etc", "a/b", "a\\b"} {
		if _, err := SessionSourcePath(agent, id); err == nil {
			t.Fatalf("agent %q must be rejected as a path element", agent)
		}
	}
	if _, err := SessionSourcePath("codex", "../../other"); err == nil {
		t.Fatal("unsafe native path accepted")
	}
	if err := (&Record{Version: 1, Agent: "claude-code", NativeSessionID: id}).Validate(); err != nil {
		t.Fatalf("record for another agent must validate: %v", err)
	}
	if err := (&Record{Version: 1, Agent: "../x", NativeSessionID: id}).Validate(); err == nil {
		t.Fatal("record with a path-unsafe agent must be rejected")
	}
}
