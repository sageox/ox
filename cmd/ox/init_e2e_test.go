//go:build !short

package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/skillmanager"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withInitFlags sets runInit's package-level flag vars and restores them, so a
// test cannot leak flag state into the rest of the package.
func withInitFlags(t *testing.T, team string) {
	t.Helper()
	prevQuiet, prevTeam, prevForce := initQuiet, initTeamFlag, initForce
	prevEndpoint, prevAgents := initEndpointFlag, initAgentsFlag
	initQuiet, initTeamFlag, initForce = true, team, true
	initEndpointFlag, initAgentsFlag = "", ""
	t.Cleanup(func() {
		initQuiet, initTeamFlag, initForce = prevQuiet, prevTeam, prevForce
		initEndpointFlag, initAgentsFlag = prevEndpoint, prevAgents
	})
}

// TestRunInit_WritesScopedIgnoreAndNeverStagesReservedArtifacts drives runInit
// end to end for the first time.
//
// Failure prevented: shipping ox's own skills and rules inside customers'
// pull requests. ADR-031 made those files gitignored working-tree state, which
// only holds if BOTH halves happen inside runInit — the scoped .gitignore block
// is written before anything is staged, and the staging filter skips reserved
// paths. Every previous test covered the helpers in isolation; nothing checked
// that runInit actually calls them, in that order, on a real repo. An ignore
// rule is useless while something still force-adds the path.
func TestRunInit_WritesScopedIgnoreAndNeverStagesReservedArtifacts(t *testing.T) {
	env := newOxE2E(t)
	withInitFlags(t, env.TeamID)

	require.NoError(t, runInit(), "ox init must succeed against the stub endpoint")

	// --- it reached the server, so this is a real end-to-end path ---
	assert.Contains(t, env.Requested(), "/api/v1/repo/init",
		"init must have registered the repo against the stub endpoint")

	// --- the repo is initialized ---
	assert.DirExists(t, filepath.Join(env.Root, ".sageox"))

	// --- the ox-managed ignore block exists ---
	var wroteAny bool
	for _, f := range skillmanager.ScopedIgnoreFiles() {
		path := filepath.Join(env.Root, f.Dir, ".gitignore")
		data, err := os.ReadFile(path)
		if err != nil {
			continue // adapter for this dir was not detected in the test env
		}
		wroteAny = true
		assert.Contains(t, string(data), "ox-cli-",
			"%s must carry the ox-managed reserved-prefix block", path)
	}
	assert.True(t, wroteAny, "runInit must write at least one scoped .gitignore")

	// --- and nothing reserved reached the index ---
	for _, staged := range stagedPaths(t, env.Root) {
		assert.False(t, isReservedManagedPath(env.Root, filepath.Join(env.Root, staged)),
			"runInit staged a reserved ox artifact (%s) — this is exactly what put vendor files in customer PRs", staged)
		assert.False(t, strings.Contains(staged, "/ox-cli-"),
			"reserved ox-cli artifact must never be staged: %s", staged)
	}
}

// TestRunInit_SnapshotsPreExistingScopedIgnoreBeforeWriting covers the ordering
// subtlety the code comment calls out: the snapshot must happen BEFORE the
// write.
//
// Failure prevented: a corrupted rollback. trackModifiedFile snapshots eagerly,
// at call time — so snapshotting after the write would capture the
// already-modified bytes, and a rollback would "restore" ox's own block into
// the user's file instead of removing it. The user ends up with vendor content
// they never accepted, left behind by a failed init.
func TestRunInit_SnapshotsPreExistingScopedIgnoreBeforeWriting(t *testing.T) {
	env := newOxE2E(t)
	withInitFlags(t, env.TeamID)

	// a user-authored ignore file that already exists before ox init runs
	const userRule = "# my own rule\nscratch/\n"
	claudeDir := filepath.Join(env.Root, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o755))
	userIgnore := filepath.Join(claudeDir, ".gitignore")
	require.NoError(t, os.WriteFile(userIgnore, []byte(userRule), 0o644))

	require.NoError(t, runInit())

	data, err := os.ReadFile(userIgnore)
	require.NoError(t, err)
	got := string(data)

	assert.Contains(t, got, "scratch/",
		"init must preserve the user's own ignore rules, not overwrite the file")
	assert.Contains(t, got, "ox-cli-",
		"init must append its managed block to the existing file")
}

// TestRunInit_AdoptsUntrackedManagedOnlyScopedIgnore covers the adopt branch:
// a scoped .gitignore that already exists, is untracked, and contains only ox's
// generated block must still be force-staged.
//
// Failure prevented: the committed on-ramp invisible forever. A prior `ox
// doctor` run can create a correct .claude/.gitignore without adding it to git.
// Init then writes nothing (the block is already current) so the file is absent
// from the write results — and repositories commonly root-ignore .claude/, so
// without an explicit force-stage the file, and the on-ramp it un-hides, never
// reach the index or any teammate.
func TestRunInit_AdoptsUntrackedManagedOnlyScopedIgnore(t *testing.T) {
	env := newOxE2E(t)
	withInitFlags(t, env.TeamID)

	// simulate the prior doctor run: managed block on disk, nothing staged.
	// EnsureScopedIgnoreFiles is existence-gated so it never creates an agent
	// directory the project does not already use — hence the mkdir first.
	require.NoError(t, os.MkdirAll(filepath.Join(env.Root, ".claude"), 0o755))
	_, err := skillmanager.EnsureScopedIgnoreFiles(env.Root)
	require.NoError(t, err)

	claudeIgnore := filepath.Join(env.Root, ".claude", ".gitignore")
	before, err := os.ReadFile(claudeIgnore)
	require.NoError(t, err, "precondition: the managed ignore file must already exist")

	require.NoError(t, runInit())

	after, err := os.ReadFile(claudeIgnore)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after),
		"init must not rewrite an already-current managed block")

	assert.Contains(t, stagedPaths(t, env.Root), filepath.Join(".claude", ".gitignore"),
		"an untracked, managed-only scoped ignore must be adopted and staged, or the on-ramp stays invisible")
}

// TestRunInit_UnwritableScopedIgnoreWarnsAndStillCompletes covers the
// write-failure path in runInit.
//
// Failure prevented: `ox init` dying, or worse silently continuing, because it
// could not write one ignore file. Init has already registered the repo and
// written config by this point; aborting would leave a half-initialized repo,
// and saying nothing would leave the coworker believing ox's files are hidden
// when they are not. It must warn and carry on.
//
// The fixture makes .claude/.gitignore a DIRECTORY. os.WriteFile fails on a
// directory on every platform we ship to, so unlike an os.Chmod fixture this
// exercises the branch identically on Windows — see bead ox-avjb for the
// fail-open trap that avoids.
func TestRunInit_UnwritableScopedIgnoreWarnsAndStillCompletes(t *testing.T) {
	env := newOxE2E(t)
	withInitFlags(t, env.TeamID)
	// the warning is gated on !initQuiet, so this test must not be quiet
	initQuiet = false

	blocker := filepath.Join(env.Root, ".claude", ".gitignore")
	require.NoError(t, os.MkdirAll(blocker, 0o755),
		"fixture: .claude/.gitignore must be a directory so writing it fails")

	// Capture stdout: the warning is the user-visible half of this contract, and
	// asserting only "init completed" let it go silent unnoticed once already.
	// ensureScopedIgnoreFiles reports a directory it cannot own as UNPROTECTED with
	// no error, so an error-only warning check printed nothing in exactly the case
	// the user needs to hear about.
	// STDERR, not stdout: cli.PrintWarning writes there. Capturing stdout returned
	// the whole success banner and no warning, which reads exactly like "ox stayed
	// silent" — the failure this assertion is meant to catch.
	r, w, pipeErr := os.Pipe()
	require.NoError(t, pipeErr)
	realStderr := os.Stderr
	os.Stderr = w
	initErr := runInit()
	os.Stderr = realStderr
	require.NoError(t, w.Close())
	printed, readErr := io.ReadAll(r)
	require.NoError(t, readErr)

	require.NoError(t, initErr,
		"a failed ignore write must not abort an otherwise successful init")
	assert.Contains(t, strings.ToLower(string(printed)), "ignore rules",
		"init said nothing about an ignore file it could not write; ox files there are visible to git")

	// init still finished its real work
	assert.DirExists(t, filepath.Join(env.Root, ".sageox"))
	assert.Contains(t, env.Requested(), "/api/v1/repo/init")

	// and the blocker is untouched — ox never destroys what it cannot write
	info, err := os.Stat(blocker)
	require.NoError(t, err)
	assert.True(t, info.IsDir(), "ox must not replace a path it failed to write")
}
