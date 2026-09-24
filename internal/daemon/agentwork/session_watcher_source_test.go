package agentwork

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateWatcherSource_RejectsAppendedForeignTurn(t *testing.T) {
	repo := t.TempDir()
	repo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	const id = "77b16b24-5b7d-4598-aacf-4c9afeb4b5ca"
	path := filepath.Join(t.TempDir(), id+".jsonl")
	first := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, repo)
	if err := os.WriteFile(path, []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	aw := &activeWatcher{adapterName: "claude-code", projectRoot: repo, sessionFile: path}
	if err := validateWatcherSource(aw); err != nil {
		t.Fatalf("initial source: %v", err)
	}
	foreign := fmt.Sprintf("{\"type\":\"assistant\",\"sessionId\":%q,\"cwd\":%q}\n", id, filepath.Dir(repo))
	if err := os.WriteFile(path, []byte(first+foreign), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateWatcherSource(aw); err == nil {
		t.Fatal("watcher accepted source after foreign turn")
	}
}
