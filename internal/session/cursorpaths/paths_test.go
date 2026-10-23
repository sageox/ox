package cursorpaths

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	conversationA = "11111111-1111-1111-1111-111111111111"
	conversationB = "22222222-2222-2222-2222-222222222222"
)

func cursorFixture(t *testing.T) (home, repo string) {
	t.Helper()
	base := t.TempDir()
	home = filepath.Join(base, "home")
	repo = filepath.Join(base, "work.space", "project")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	return home, repo
}

func writeNativeTranscript(t *testing.T, home, repo, id string) string {
	t.Helper()
	path, err := SessionPath(home, repo, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidateConversationID(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   string
		ok   bool
	}{
		{"canonical UUID", conversationA, true},
		{"fixture alias is not native identity", "conversation-1", false},
		{"uppercase is not canonical", "11111111-1111-1111-1111-11111111111A", false},
		{"generation ID is not a conversation", "generation-1", false},
		{"path traversal", "../11111111-1111-1111-1111-111111111111", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateConversationID(tc.id)
			if (err == nil) != tc.ok {
				t.Fatalf("ValidateConversationID(%q) error = %v, want success=%v", tc.id, err, tc.ok)
			}
		})
	}
}

func TestValidationErrorsDoNotExposeNativeIdentityOrPath(t *testing.T) {
	identity := "not-a-native-conversation-id"
	err := ValidateConversationID(identity)
	if !errors.Is(err, ErrInvalidConversationID) || strings.Contains(err.Error(), identity) {
		t.Fatalf("identity error leaked input or lost category: %v", err)
	}

	home, repo := cursorFixture(t)
	missingHome := filepath.Join(home, "not-present")
	_, err = SessionPath(missingHome, repo, conversationA)
	if !errors.Is(err, ErrInvalidSource) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source error lost category: %v", err)
	}
	if strings.Contains(err.Error(), missingHome) {
		t.Fatalf("source error leaked native path: %v", err)
	}
}

func TestSessionPathUsesCanonicalWorkspaceEncoding(t *testing.T) {
	home, repo := cursorFixture(t)
	path, err := SessionPath(home, repo, conversationA)
	if err != nil {
		t.Fatal(err)
	}
	canonicalHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	canonicalRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	key := strings.NewReplacer("/", "-", ".", "-").Replace(strings.TrimPrefix(filepath.ToSlash(canonicalRepo), "/"))
	want := filepath.Join(canonicalHome, ".cursor", "projects", key, "agent-transcripts", conversationA, conversationA+".jsonl")
	if path != want {
		t.Fatalf("SessionPath = %q, want %q", path, want)
	}
	if key == "" {
		t.Fatalf("workspace key %q did not preserve the observed encoding", key)
	}
}

func TestValidateSourceExactIdentityAndPendingPaths(t *testing.T) {
	home, repo := cursorFixture(t)
	sourceA := writeNativeTranscript(t, home, repo, conversationA)
	writeNativeTranscript(t, home, repo, conversationB)

	got, err := ValidateSource(home, repo, conversationA, sourceA)
	if err != nil || got != sourceA {
		t.Fatalf("exact source = %q, %v; want %q", got, err, sourceA)
	}

	pending, err := SessionPath(home, repo, "33333333-3333-3333-3333-333333333333")
	if err != nil {
		t.Fatal(err)
	}
	got, err = ValidateSource(home, repo, "33333333-3333-3333-3333-333333333333", "")
	if err != nil || got != pending {
		t.Fatalf("pending source = %q, %v; want %q", got, err, pending)
	}
	if _, statErr := os.Stat(pending); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("pending validation created or found %q: %v", pending, statErr)
	}

	for _, tc := range []struct {
		name string
		id   string
		hint string
	}{
		{"other chat", conversationA, filepath.Join(filepath.Dir(filepath.Dir(sourceA)), conversationB, conversationB+".jsonl")},
		{"relative hint", conversationA, "transcript.jsonl"},
		{"traversal hint", conversationA, filepath.Dir(sourceA) + "/../" + conversationA + "/" + conversationA + ".jsonl"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ValidateSource(home, repo, tc.id, tc.hint); !errors.Is(err, ErrInvalidSource) {
				t.Fatalf("ValidateSource error = %v, want invalid source", err)
			}
		})
	}
}

func TestValidateSourceRejectsWrongWorkspaceAndNativePathTricks(t *testing.T) {
	home, repo := cursorFixture(t)
	source := writeNativeTranscript(t, home, repo, conversationA)
	otherRepo := filepath.Join(filepath.Dir(repo), "other-worktree")
	if err := os.MkdirAll(otherRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateSource(home, otherRepo, conversationA, source); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("wrong worktree was accepted: %v", err)
	}

	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{
			name: "leaf symlink escape",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				outside := filepath.Join(t.TempDir(), "outside.jsonl")
				if err := os.WriteFile(outside, []byte("{}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, path); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			},
		},
		{
			name: "dangling parent symlink",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := os.RemoveAll(filepath.Dir(path)); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(t.TempDir(), "gone"), filepath.Dir(path)); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			},
		},
		{
			name: "parent symlink escape",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := os.RemoveAll(filepath.Dir(path)); err != nil {
					t.Fatal(err)
				}
				outside := t.TempDir()
				if err := os.Symlink(outside, filepath.Dir(path)); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			},
		},
		{
			name: "directory transcript leaf",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, repo := cursorFixture(t)
			path := writeNativeTranscript(t, home, repo, conversationA)
			tc.setup(t, path)
			if _, err := ValidateSource(home, repo, conversationA, path); !errors.Is(err, ErrInvalidSource) {
				t.Fatalf("unsafe source was accepted: %v", err)
			}
		})
	}
}
