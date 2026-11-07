package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/runtime"
)

const filesMountTestEndpoint = "https://files-mount.test"

// primeWithMountedTeam lays out a repo bound to team_abc with an empty git
// checkout, plus a mounted drive carrying one of every kind of team document.
func primeWithMountedTeam(t *testing.T, flag string) *teamContextInfo {
	t.Helper()
	home := mountHome(t, "team_abc")
	t.Setenv("OX_PERSIST_DISK", "1")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(filesMountEnv, flag)
	runtime.Reset()
	t.Cleanup(runtime.Reset)

	projectRoot := createInitializedProjectWithConfig(t, &config.ProjectConfig{
		ProjectID:   "test_project",
		WorkspaceID: "test_workspace",
		TeamID:      "team_abc",
		TeamName:    "Team ABC",
		Endpoint:    filesMountTestEndpoint,
	})
	checkout := paths.TeamContextDir("team_abc", filesMountTestEndpoint)
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}

	drive := filepath.Join(home, "SageOx", "SageOx Internal")
	writeTeamFile(t, drive, "docs/onboarding.md", doc("Onboarding"))
	writeTeamFile(t, drive, "agents/rules/react.md", rule("react", "only on the drive"))
	writeTeamFile(t, drive, "agents/profiles/sre.md", "---\nname: sre\ndescription: only on the drive\n---\nbody\n")
	writeTeamFile(t, drive, "agents/commands/deploy.md", "---\ndescription: Deploy\n---\nbody\n")
	writeTeamFile(t, drive, "MEMORY.md", "drive memory\n")
	writeTeamFile(t, drive, "memory/daily/2026-10-01.md", "day\n")
	writeTeamFile(t, drive, "capabilities/team/index.md", "roster\n")
	writeTeamFile(t, drive, "agent-context/distilled-discussions.md", "distilled\n")

	info := discoverTeamContextWithFallback(projectRoot, "", false)
	if info == nil {
		t.Fatal("no team context discovered")
	}
	return info
}

// With the flag unset, a mounted drive changes nothing about prime.
func TestPrimeIgnoresAMountedDriveWithoutTheFlag(t *testing.T) {
	info := primeWithMountedTeam(t, "")

	if info.Mount != nil {
		t.Errorf("Mount = %+v, want nil with the flag unset", info.Mount)
	}
	if len(info.TeamDocs) != 0 || len(info.TeamRules) != 0 || info.HasAgentContext ||
		info.Escalation != "" || info.MemoryContent != "" {
		t.Errorf("prime read the drive without the flag: %+v", info)
	}
}

// With the flag set, the drive fills every gap the empty checkout leaves, and
// prime reports what it supplied.
func TestPrimeReadsAMountedDriveWithTheFlag(t *testing.T) {
	info := primeWithMountedTeam(t, "1")

	if info.Mount == nil {
		t.Fatal("Mount = nil, want the drive recorded as a second source")
	}
	if info.Mount.Docs != 1 || info.Mount.Rules != 1 || info.Mount.Agents != 1 ||
		info.Mount.Commands != 1 || info.Mount.Memory != 2 {
		t.Errorf("contribution counts = %+v, want 1 doc, rule, agent, command and 2 memory", info.Mount)
	}
	if info.Escalation == "" {
		t.Error("the escalation roster only the drive carried was dropped")
	}
	if !info.HasAgentContext || !strings.HasPrefix(info.AgentContextPath, info.Mount.Path) {
		t.Errorf("distilled discussions = %v at %q, want read from the drive", info.HasAgentContext, info.AgentContextPath)
	}
	if !strings.Contains(info.Mount.Summary(), "1 docs") {
		t.Errorf("summary = %q", info.Mount.Summary())
	}
}

func TestRenderFilesRow(t *testing.T) {
	mountHome(t, "team_abc")

	render := func(flag string, showPath bool) string {
		t.Setenv(filesMountEnv, flag)
		scan := scanFilesMounts()
		defer scan.close()
		var b strings.Builder
		renderFilesRow(&b, scan, "team_abc", showPath)
		return b.String()
	}

	if out := render("", true); !strings.Contains(out, "not in use") || !strings.Contains(out, filesMountEnv+"=1") ||
		!strings.Contains(out, "Files path") {
		t.Errorf("flag off should say the drive is not in use and how to opt in:\n%s", out)
	}
	if out := render("1", false); !strings.Contains(out, "second source") || strings.Contains(out, "Files path") {
		t.Errorf("flag on should say second source and hide the path without --verbose:\n%s", out)
	}

	scan := scanFilesMounts()
	defer scan.close()
	var b strings.Builder
	renderFilesRow(&b, scan, "team_other", true)
	if b.Len() != 0 {
		t.Errorf("a team the drive does not carry rendered a row:\n%s", b.String())
	}
}
