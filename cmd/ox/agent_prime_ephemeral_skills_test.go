package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/runtime"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	proseSkillManifest  = "---\nname: lint-guide\ndescription: How we lint.\n---\n\nRun the linter before pushing.\n"
	scriptSkillManifest = "---\nname: deploy\ndescription: Deploy safely.\n---\n\nRun scripts/deploy.sh.\n"
	grantSkillManifest  = "---\nname: ops-shell\ndescription: Shell access for ops.\nallowed-tools: Bash\n---\n\nUse the shell.\n"
)

func proseSkill() api.TeamContextSkill {
	return api.TeamContextSkill{Name: "lint-guide", Root: "agents/skills", Dir: "lint-guide", Digest: "sha256:1",
		Files: []api.TeamContextSkillFile{{Path: "SKILL.md", Content: proseSkillManifest}}}
}

func scriptSkill() api.TeamContextSkill {
	return api.TeamContextSkill{Name: "deploy", Root: "agents/skills", Dir: "deploy", Digest: "sha256:2",
		Files: []api.TeamContextSkillFile{
			{Path: "SKILL.md", Content: scriptSkillManifest},
			{Path: "scripts/deploy.sh", Content: "#!/bin/sh\necho deploy\n", Executable: true},
		}}
}

func grantSkill() api.TeamContextSkill {
	return api.TeamContextSkill{Name: "ops-shell", Root: "agents/skills", Dir: "ops-shell", Digest: "sha256:3",
		Files: []api.TeamContextSkillFile{{Path: "SKILL.md", Content: grantSkillManifest}}}
}

// ephemeralSkillsHarness is an ephemeral (non-persistent) environment: an
// initialized repository with a native skill target, no Team Context clone,
// and a team-context endpoint whose response the test swaps between primes.
type ephemeralSkillsHarness struct {
	project string
	teamDir string
	resp    atomic.Pointer[api.TeamContextContentResponse]
}

func newEphemeralSkillsHarness(t *testing.T) *ephemeralSkillsHarness {
	t.Helper()
	t.Setenv("OX_EPHEMERAL", "1")
	t.Setenv("OX_PERSIST_DISK", "0")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	runtime.Reset()
	t.Cleanup(runtime.Reset)

	h := &ephemeralSkillsHarness{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(h.resp.Load())
	}))
	t.Cleanup(server.Close)

	h.project = createInitializedProjectWithConfig(t, &config.ProjectConfig{
		ProjectID: "test_project", WorkspaceID: "test_workspace",
		TeamID: "team_abc", TeamName: "Team ABC", Endpoint: server.URL,
	})
	gitInitRepo(t, h.project)
	// A canonical origin, so untargeted AND targeted skills are authoritative and
	// retirement is not suppressed for an unknown repository identity.
	gitOutput(t, h.project, "remote", "add", "origin", "https://github.com/acme/ephemeral-test.git")
	stageSelectedTarget(t, h.project)
	// The baseline `ox init` leaves behind, ignore rules included: prime's
	// automatic reconcile refuses to write into an unprotected skills root.
	_, err := reconcileExactSelectedSkills(h.project)
	require.NoError(t, err)
	h.teamDir = paths.TeamContextDir("team_abc", server.URL)
	return h
}

func (h *ephemeralSkillsHarness) prime(t *testing.T, resp *api.TeamContextContentResponse) *teamContextInfo {
	t.Helper()
	resp.TeamID, resp.TeamName = "team_abc", "Team ABC"
	h.resp.Store(resp)
	info := discoverTeamContextWithFallback(h.project, "", true)
	require.NotNil(t, info)
	return info
}

func (h *ephemeralSkillsHarness) projected(skill string, rel ...string) string {
	return filepath.Join(append([]string{h.project, ".claude", "skills", skill + skillmanager.TeamSuffix}, rel...)...)
}

// HTTP-only prime must deliver team skills through the same gate a checkout
// does: prose installs, executable content waits for approval.
// Failure prevented: ephemeral environments (cloud coding agents, Codespaces)
// had team docs but no team skills at all, or — the worse version — got
// executable skill content with no approval because it skipped the gate.
func TestEphemeralPrime_MaterializesTeamSkillsThroughTheApprovalGate(t *testing.T) {
	h := newEphemeralSkillsHarness(t)
	info := h.prime(t, &api.TeamContextContentResponse{
		Docs:   []api.TeamContextDoc{},
		Skills: []api.TeamContextSkill{proseSkill(), scriptSkill(), grantSkill()},
		Rules: []api.TeamContextRule{{Name: "team-rules", Path: "agents/rules/team-rules.md",
			Content: "---\nname: team-rules\ndescription: Team rules\nvisibility: always\n---\n\nBe kind.\n"}},
	})

	require.Len(t, info.TeamRules, 1, "a served rule must reach prime like a checkout's rule")
	assert.Equal(t, "team-rules", info.TeamRules[0].Name)
	assert.Contains(t, info.TeamRules[0].Body, "Be kind.")

	_, withheld := reconcileSkillInventoryIfStale(h.project)

	require.Equal(t, proseSkillManifest, stripStamp(t, h.projected("lint-guide", "SKILL.md")),
		"prose team skill must install with no approval")
	require.FileExists(t, h.projected("deploy", "SKILL.md"), "a scripted skill's prose still installs")
	require.NoFileExists(t, h.projected("deploy", "scripts", "deploy.sh"),
		"an unapproved script reached the repository")
	require.NoDirExists(t, h.projected("ops-shell"), "a manifest that grants tools must wait for approval")

	needsApproval := map[string]bool{}
	for _, decision := range withheld {
		needsApproval[decision.Name] = decision.NeedsApprove
	}
	assert.True(t, needsApproval["deploy"], "the withheld script must be reported as awaiting approval")
	assert.True(t, needsApproval["ops-shell"], "the withheld manifest must be reported as awaiting approval")
	assert.NotContains(t, needsApproval, "lint-guide")
}

// stripStamp reads a projected manifest without the ownership stamp ox appends.
func stripStamp(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	content := string(data)
	if i := indexOfStamp(content); i >= 0 {
		content = content[:i]
	}
	return content
}

func indexOfStamp(content string) int {
	for i := 0; i+4 <= len(content); i++ {
		if content[i:i+4] == "<!--" {
			return i
		}
	}
	return -1
}

// A skill the team deletes must leave the environment on the next prime, and a
// changed skill must re-project — even though an HTTP-fetched Team Context has no
// git HEAD for the skill fast path to key on.
// Failure prevented: the revision stayed "" forever, so prime's fast path never
// re-planned and a retired or edited team skill lived on in the repository.
func TestEphemeralPrime_RetiresAndUpdatesSkillsAcrossPrimes(t *testing.T) {
	h := newEphemeralSkillsHarness(t)
	h.prime(t, &api.TeamContextContentResponse{Skills: []api.TeamContextSkill{proseSkill(), scriptSkill()}})
	reconcileSkillInventoryIfStale(h.project)
	require.FileExists(t, h.projected("deploy", "SKILL.md"))

	edited := proseSkill()
	edited.Files[0].Content = "---\nname: lint-guide\ndescription: How we lint.\n---\n\nRun the NEW linter.\n"
	h.prime(t, &api.TeamContextContentResponse{Skills: []api.TeamContextSkill{edited}})
	reconcileSkillInventoryIfStale(h.project)

	require.NoDirExists(t, filepath.Join(h.teamDir, "agents", "skills", "deploy"), "an unlisted skill must be swept")
	require.NoDirExists(t, h.projected("deploy"), "a retired team skill survived in the repository")
	require.Contains(t, stripStamp(t, h.projected("lint-guide", "SKILL.md")), "Run the NEW linter.")
}

// A truncated response is not the team deleting anything. The server sets the
// flag both when it dropped skills to fit its cap and when it could not read the
// Team Context at all (an empty, truncated list), so nothing unlisted is retired
// — skills or rules, which carry no flag of their own.
// Failure prevented: a transient server-side read failure retired every team
// skill and rule from the environment, and the next prime re-installed them.
func TestEphemeralPrime_TruncatedResponseRetiresNothing(t *testing.T) {
	h := newEphemeralSkillsHarness(t)
	rule := api.TeamContextRule{Name: "r", Path: "agents/rules/r.md", Content: "# r\n"}
	h.prime(t, &api.TeamContextContentResponse{
		Skills: []api.TeamContextSkill{proseSkill(), scriptSkill()}, Rules: []api.TeamContextRule{rule},
	})
	reconcileSkillInventoryIfStale(h.project)
	require.FileExists(t, h.projected("deploy", "SKILL.md"))

	for _, partial := range [][]api.TeamContextSkill{{proseSkill()}, {}} {
		h.prime(t, &api.TeamContextContentResponse{
			Skills: partial, SkillsTruncated: true, Rules: []api.TeamContextRule{},
		})
		reconcileSkillInventoryIfStale(h.project)
		require.FileExists(t, filepath.Join(h.teamDir, "agents", "skills", "deploy", "SKILL.md"))
		require.FileExists(t, filepath.Join(h.teamDir, "agents", "skills", "lint-guide", "SKILL.md"))
		require.FileExists(t, filepath.Join(h.teamDir, "agents", "rules", "r.md"), "a truncated bundle retired a rule")
		require.FileExists(t, h.projected("deploy", "SKILL.md"), "a truncated bundle retired a projected skill")
	}
}

// An older server sends no skills or rules keys: what is on disk stays exactly
// as it was, as it would with an older ox.
func TestEphemeralPrime_OldServerLeavesSkillsAndRulesAlone(t *testing.T) {
	h := newEphemeralSkillsHarness(t)
	h.prime(t, &api.TeamContextContentResponse{
		Skills: []api.TeamContextSkill{proseSkill()},
		Rules:  []api.TeamContextRule{{Name: "r", Path: "agents/rules/r.md", Content: "# r\n"}},
	})
	snapshot, err := os.ReadFile(filepath.Join(h.teamDir, filepath.FromSlash(skillmanager.HTTPSnapshotFile)))
	require.NoError(t, err)

	h.prime(t, &api.TeamContextContentResponse{Docs: []api.TeamContextDoc{}}) // nil Skills / Rules

	require.FileExists(t, filepath.Join(h.teamDir, "agents", "skills", "lint-guide", "SKILL.md"))
	require.FileExists(t, filepath.Join(h.teamDir, "agents", "rules", "r.md"))
	after, err := os.ReadFile(filepath.Join(h.teamDir, filepath.FromSlash(skillmanager.HTTPSnapshotFile)))
	require.NoError(t, err)
	require.Equal(t, snapshot, after)
}

// The server is trusted for skill content, never for where it lands.
func TestMaterializeTeamSkills_RefusesUnsafeLocationsAndExecutableDisagreement(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	teamCtxDir := filepath.Join(parent, "team")
	require.NoError(t, os.MkdirAll(teamCtxDir, 0o755))

	escape := func(root, dir, file string) api.TeamContextSkill {
		return api.TeamContextSkill{Name: "x", Root: root, Dir: dir,
			Files: []api.TeamContextSkillFile{{Path: file, Content: "---\nname: x\n---\n"}}}
	}
	mismatch := proseSkill()
	mismatch.Files = append(mismatch.Files, api.TeamContextSkillFile{Path: "notes.md", Content: "notes", Executable: true})

	require.NoError(t, materializeTeamSkillsAndRules(teamCtxDir, &api.TeamContextContentResponse{
		Skills: []api.TeamContextSkill{
			escape("agents/skills", "..", "SKILL.md"),
			escape("agents/skills", "a/b", "SKILL.md"),
			escape("../outside", "x", "SKILL.md"),
			escape("agents/skills", "x", "../../escaped.md"),
			escape("agents/skills", "y", "/abs.md"),
			mismatch,
		},
		Rules: []api.TeamContextRule{
			{Path: "../rules/escape.md", Content: "x"},
			{Path: "agents/rules/not-markdown.sh", Content: "x"},
			{Path: "docs/elsewhere.md", Content: "x"},
		},
	}))

	entries, err := os.ReadDir(parent)
	require.NoError(t, err)
	require.Len(t, entries, 1, "nothing may be written beside the Team Context dir")
	for _, rel := range []string{"agents/skills/x", "agents/skills/y", "agents/skills/lint-guide", "docs/elsewhere.md", "agents/rules/not-markdown.sh"} {
		_, statErr := os.Stat(filepath.Join(teamCtxDir, filepath.FromSlash(rel)))
		assert.True(t, os.IsNotExist(statErr), "%s must not be materialized", rel)
	}
}

// A real checkout at the same path keeps its own tracked files.
func TestMaterializeTeamSkills_LeavesAGitCheckoutAlone(t *testing.T) {
	t.Parallel()
	teamCtxDir := t.TempDir()
	gitInitRepo(t, teamCtxDir)
	tracked := filepath.Join(teamCtxDir, "agents", "skills", "kept", "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(tracked), 0o755))
	require.NoError(t, os.WriteFile(tracked, []byte("---\nname: kept\n---\n"), 0o644))

	require.NoError(t, materializeTeamSkillsAndRules(teamCtxDir, &api.TeamContextContentResponse{
		Skills: []api.TeamContextSkill{}, Rules: []api.TeamContextRule{},
	}))
	require.FileExists(t, tracked)
}
