//go:build !short

package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session/adapters"
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

// Codex hooks remain inactive until trusted, so init's next steps must explain
// approval only when the selected integration actually installed its hooks.
func TestRunInit_CodexTrustStepRequiresInstalledHooks(t *testing.T) {
	for _, tc := range []struct {
		name         string
		agents       string
		installFails bool
		wantTrust    bool
	}{
		{name: "installed", agents: "codex", wantTrust: true},
		{name: "install failed", agents: "codex", installFails: true},
		{name: "not selected", agents: "claude-code"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newOxE2E(t)
			withInitFlags(t, env.TeamID)
			initQuiet, initAgentsFlag = false, tc.agents

			adapterDir := t.TempDir()
			createFakeAdapterWithHooks(t, adapterDir, "codex", "0.1.0", "session", ".codex")
			t.Setenv("OX_ADAPTER_PATH", adapterDir)
			adapters.Unregister("codex")
			t.Cleanup(func() { adapters.Unregister("codex") })
			if tc.installFails {
				require.NoError(t, os.WriteFile(filepath.Join(env.Root, ".codex"), []byte("blocked"), 0o644))
			}

			var err error
			output := captureStdoutForPlanCLI(t, func() { err = runInit() })
			require.NoError(t, err, "an optional integration failure must not prevent repository setup")
			assert.Contains(t, env.Requested(), "/api/v1/repo/init")
			assert.Contains(t, output, "SageOx initialized successfully!")
			if tc.wantTrust {
				assert.FileExists(t, filepath.Join(env.Root, ".codex", "hooks.json"))
				require.Contains(t, output, "/hooks to review and trust the SageOx hooks")
				require.Contains(t, strings.ToLower(output), "next steps")
				steps := output[strings.Index(strings.ToLower(output), "next steps"):]
				require.Contains(t, steps, "ox doctor")
				assert.Less(t, strings.Index(steps, "ox doctor"), strings.Index(steps, "/hooks"))
				assert.Less(t, strings.Index(steps, "/hooks"), strings.Index(steps, "Invite teammates"))
			} else {
				assert.NotContains(t, output, "/hooks")
				assert.NoFileExists(t, filepath.Join(env.Root, ".codex", "hooks.json"))
			}
		})
	}
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

// TestRunInit_TeamFlag_ResolvesToTheTeamIDTheServerIsAsked drives `ox init --team`
// with each vocabulary a coworker might type and asserts on the team ID that
// actually reached POST /api/v1/repo/init.
//
// Failure prevented: `ox init --team platform` (the slug `ox team list` prints)
// reaching the server as the string "platform" and bouncing as HTTP 400 after init
// had already written files. The harness reports TWO teams, so a pass-through
// that sends the typed value, or a resolver that returns the wrong team, both fail
// here; the unit tests over resolveTeamFlag cannot see either.
func TestRunInit_TeamFlag_ResolvesToTheTeamIDTheServerIsAsked(t *testing.T) {
	for _, tc := range []struct {
		name     string
		flag     string
		wantID   string
		wantName string
	}{
		{name: "slug", flag: "other-team", wantID: "team-e2e-other", wantName: "Other Team"},
		{name: "name in any case", flag: "OTHER team", wantID: "team-e2e-other", wantName: "Other Team"},
		{name: "ID", flag: "team-e2e-other", wantID: "team-e2e-other", wantName: "Other Team"},
		{name: "slug with surrounding whitespace", flag: "  e2e-team  ", wantID: "team-e2e", wantName: "E2E Team"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newOxE2E(t)
			withInitFlags(t, tc.flag)

			require.NoError(t, runInit())

			assert.Contains(t, env.Requested(), "/api/v1/cli/repos",
				"init must have fetched the team list to resolve --team")
			reqs := env.InitRequests()
			require.Len(t, reqs, 1, "init must register exactly once")
			assert.Equal(t, []string{tc.wantID}, reqs[0].Teams,
				"the server must be asked to register against the RESOLVED team ID, not the typed value")

			cfg, err := config.LoadProjectConfig(env.Root)
			require.NoError(t, err)
			assert.Equal(t, tc.wantID, cfg.TeamID)
			assert.Equal(t, tc.wantName, cfg.TeamName,
				"the resolved team's display name must reach config.json, or every label falls back to the raw ID")
		})
	}
}

// TestRunInit_TeamFlag_UnmatchedValueWarnsAndTheServerDecides pins the failure
// policy for a --team the user's team list does not contain: warn, send it to the
// server as typed, and let the server's answer stand. It matches `ox invite --team`.
//
// Failure prevented: a client-side list blocking a registration the server would
// accept. A token can be authorized to register into a team that /api/v1/cli/repos
// does not report (a team still provisioning, a wider token scope); rejecting
// locally would make that team unreachable from `ox init --team` with no way round.
// The warning is the other half: without it a typo would go to the server silently.
func TestRunInit_TeamFlag_UnmatchedValueWarnsAndTheServerDecides(t *testing.T) {
	for _, tc := range []struct {
		name         string
		serverStatus int
		wantErr      string
	}{
		{name: "server accepts a team missing from the list", serverStatus: 200},
		{name: "server rejects a typo", serverStatus: 400, wantErr: "failed to register with SageOx API"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newOxE2E(t)
			withInitFlags(t, "  team-unlisted ")
			env.OnRegister(func(req api.RepoInitRequest) (int, any) {
				if tc.serverStatus != 200 {
					return tc.serverStatus, map[string]any{"success": false, "error": "team name not found"}
				}
				return 200, api.RepoInitResponse{RepoID: env.RepoID, TeamID: "team-unlisted"}
			})

			var err error
			warned := captureStderr(t, func() { err = runInit() })

			// the typo is never silent, and the message is a heads-up, not a refusal
			assert.Contains(t, warned, `--team "team-unlisted"`)
			assert.Contains(t, warned, "the server will decide")
			assert.Contains(t, warned, "Other Team (other-team, team-e2e-other)",
				"the warning must show what would have matched")

			// the server — not the local list — was asked, with the value as typed
			reqs := env.InitRequests()
			require.Len(t, reqs, 1, "an unmatched --team must still reach the server")
			assert.Equal(t, []string{"team-unlisted"}, reqs[0].Teams)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr, "the server's rejection must stand")
				return
			}
			require.NoError(t, err, "a team the server accepts must not be blocked by the local list")
			cfg, cfgErr := config.LoadProjectConfig(env.Root)
			require.NoError(t, cfgErr)
			assert.Equal(t, "team-unlisted", cfg.TeamID, "config follows the server's reply")
			assert.Empty(t, cfg.TeamName, "init must not write a team name it never resolved")
		})
	}
}

// TestRunInit_TeamFlag_AmbiguousValueFailsBeforeAnythingIsWritten covers the one
// outcome that fails locally.
//
// Failure prevented: a consultant in two orgs that each named a team "Platform"
// having `--team platform` bind the repo to whichever came first — which, before the
// derived list was sorted, differed from run to run. Choosing a tenant is not a
// decision ox may make silently, so it stops, names both candidates, and has not
// contacted the server or created .sageox/. The team ID is the way out.
func TestRunInit_TeamFlag_AmbiguousValueFailsBeforeAnythingIsWritten(t *testing.T) {
	setup := func(t *testing.T, flag string) *oxE2E {
		env := newOxE2E(t)
		withInitFlags(t, flag)
		env.SetTeams(
			api.TeamMembership{ID: "team-orga", Name: "Platform", Slug: "platform-a"},
			api.TeamMembership{ID: "team-orgb", Name: "Platform", Slug: "platform-b"},
		)
		return env
	}

	t.Run("a shared name is refused and lists both candidates", func(t *testing.T) {
		env := setup(t, "platform")

		err := runInit()

		require.Error(t, err)
		assert.Contains(t, err.Error(), `ambiguous team "platform"`)
		assert.Contains(t, err.Error(), "Platform (platform-a, team-orga)")
		assert.Contains(t, err.Error(), "Platform (platform-b, team-orgb)")
		assert.Empty(t, env.InitRequests(), "an ambiguous --team must never reach registration")
		assert.NoDirExists(t, filepath.Join(env.Root, ".sageox"),
			"an ambiguous --team must fail before init writes anything")
	})

	t.Run("the team ID is the unambiguous way out", func(t *testing.T) {
		env := setup(t, "team-orgb")

		require.NoError(t, runInit())

		reqs := env.InitRequests()
		require.Len(t, reqs, 1)
		assert.Equal(t, []string{"team-orgb"}, reqs[0].Teams)
	})
}
