package lfs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countGitSpawns puts a logging `git` wrapper first on PATH and returns a func
// reporting how many invocations used the named subcommand.
func countGitSpawns(t *testing.T) func(subcommand string) int {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the git PATH wrapper is a POSIX shell script; PATH lookup does not run it on Windows")
	}
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "spawns.log")
	script := "#!/bin/sh\necho \"$@\" >> '" + logPath + "'\nexec '" + realGit + "' \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func(subcommand string) int {
		data, _ := os.ReadFile(logPath)
		count := 0
		for _, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, " "+subcommand+" ") || strings.Contains(line, " "+subcommand) && strings.HasSuffix(line, subcommand) {
				count++
			}
		}
		return count
	}
}

// The validator reads every unpushed blob through ONE long-lived cat-file
// process. Failure prevented: a process (and deadline) per file, so under load a
// single slow spawn threw away a 15-minute pass and re-wedged the push.
func TestValidateUnpushedTip_UsesOneCatFileProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git history with hundreds of files")
	}
	ledger, _ := initLedgerWithRemote(t)
	const files = 120
	batch := make(map[string]string, files*2)
	for i := 0; i < files; i++ {
		name := fmt.Sprintf("s%03d", i)
		oid := fmt.Sprintf("%064x", i+1)
		batch["sessions/"+name+"/raw.jsonl"] = lfsPointerContent(oid, 10)
		batch["sessions/"+name+"/meta.json"] = metaFor(metaRef("raw.jsonl", oid, 10))
	}
	writeAndCommit(t, ledger, "many unpushed sessions", batch)
	upstream := strings.TrimSpace(git(t, ledger, "rev-parse", "@{upstream}"))
	spawns := countGitSpawns(t)

	err := ValidateUnpushedTip(context.Background(), ledger, upstream)

	require.NoError(t, err)
	assert.Equal(t, 1, spawns("cat-file"), "one batch process, not one per file")
	assert.Zero(t, spawns("show"), "meta.json lookups go through the same process")
}

// The same pass still refuses what it refused before.
func TestValidateUnpushedTip_BatchReaderStillCatchesConflictMarkers(t *testing.T) {
	ledger, _ := initLedgerWithRemote(t)
	oid := fmt.Sprintf("%064x", 7)
	writeAndCommit(t, ledger, "clean session", map[string]string{
		"sessions/ok/raw.jsonl": lfsPointerContent(oid, 10),
		"sessions/ok/meta.json": metaFor(metaRef("raw.jsonl", oid, 10)),
	})
	writeAndCommit(t, ledger, "conflicted meta", map[string]string{
		"sessions/bad/meta.json": "<<<<<<< HEAD\n{}\n=======\n{}\n>>>>>>> other\n",
	})
	upstream := strings.TrimSpace(git(t, ledger, "rev-parse", "@{upstream}"))

	err := ValidateUnpushedTip(context.Background(), ledger, upstream)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "sessions/bad/meta.json")
}
