package recap

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/plan"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newPlanFixture wires a project root's .sageox/config.json plus an isolated
// XDG_DATA_HOME/XDG_CONFIG_HOME so plan.Save/List/Load — which resolve the
// ledger internally via config.LoadProjectContext(gitRoot).DefaultLedgerPath(),
// completely independent of the recap package's own BuildInput.LedgerPath —
// land in a sandboxed location instead of the developer's real
// ~/.local/share/sageox. Because it mutates process env vars via t.Setenv,
// every caller of this helper MUST NOT call t.Parallel().
func newPlanFixture(t *testing.T) (projectRoot string, id Identity) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "xdg-data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg-config"))

	projectRoot = t.TempDir()
	require.NoError(t, config.SaveProjectConfig(projectRoot, &config.ProjectConfig{
		RepoID:   "repo_recaptest01",
		Endpoint: "https://test.sageox.ai",
	}))
	return projectRoot, Identity{DisplayName: "Ryan Snodgrass"}
}

// savePlan writes a fixture plan via the real plan.Save path (not a hand
// rolled JSON file) so gatherPlans is exercised against the actual on-disk
// shape plan.List/Load expect.
func savePlan(t *testing.T, projectRoot, topic string, createdAt time.Time, authors []string, signals plan.SignalSummary) {
	t.Helper()
	_, _, err := plan.Save(
		projectRoot,
		plan.Input{Raw: "# " + topic + "\n\nplan body\n", Topic: topic},
		plan.Result{Signals: signals},
		nil,
		plan.Meta{Topic: topic, CreatedAt: createdAt, Authors: authors},
	)
	require.NoError(t, err)
}

// --- gatherPlans ---
//
// Failure prevented: a plan report that either buries the ONE thing SageOx
// actually caught among a pile of no-op plans, or leaks a teammate's plan
// (or work outside the reporting window) into "your work" claims.

func TestGatherPlans_OnlyFiredSignalsIncluded(t *testing.T) {
	// non-parallel: newPlanFixture uses t.Setenv
	projectRoot, id := newPlanFixture(t)
	since := fixtureNow.Add(-24 * time.Hour)
	until := fixtureNow.Add(24 * time.Hour)

	savePlan(t, projectRoot, "Plan With Collision", fixtureNow, nil, plan.SignalSummary{Collisions: 2})
	savePlan(t, projectRoot, "Plan With Nothing Caught", fixtureNow, nil, plan.SignalSummary{})

	got := gatherPlans(projectRoot, since, until, id)
	require.Len(t, got, 1, "a plan where nothing was caught (all signals zero) is not a value receipt and must be excluded")
	assert.Equal(t, "Plan With Collision", got[0].Topic)
	assert.Equal(t, 2, got[0].Collisions)
}

func TestGatherPlans_WindowFilter(t *testing.T) {
	projectRoot, id := newPlanFixture(t)
	since := fixtureNow.Add(-24 * time.Hour)
	until := fixtureNow.Add(24 * time.Hour)

	savePlan(t, projectRoot, "Plan In Window", fixtureNow, nil, plan.SignalSummary{Collisions: 1})
	savePlan(t, projectRoot, "Plan Before Window", fixtureNow.Add(-72*time.Hour), nil, plan.SignalSummary{Collisions: 1})
	savePlan(t, projectRoot, "Plan After Window", fixtureNow.Add(72*time.Hour), nil, plan.SignalSummary{Collisions: 1})

	got := gatherPlans(projectRoot, since, until, id)
	require.Len(t, got, 1)
	assert.Equal(t, "Plan In Window", got[0].Topic)
}

func TestGatherPlans_AuthorFilter(t *testing.T) {
	projectRoot, id := newPlanFixture(t) // id.DisplayName == "Ryan Snodgrass"
	since := fixtureNow.Add(-24 * time.Hour)
	until := fixtureNow.Add(24 * time.Hour)

	savePlan(t, projectRoot, "My Own Plan", fixtureNow, []string{"Ryan Snodgrass"}, plan.SignalSummary{PriorArt: 1})
	savePlan(t, projectRoot, "Teammates Plan", fixtureNow, []string{"Someone Else"}, plan.SignalSummary{PriorArt: 1})
	savePlan(t, projectRoot, "Legacy No Author Plan", fixtureNow, nil, plan.SignalSummary{PriorArt: 1})

	got := gatherPlans(projectRoot, since, until, id)
	topics := map[string]bool{}
	for _, p := range got {
		topics[p.Topic] = true
	}
	assert.True(t, topics["My Own Plan"])
	assert.False(t, topics["Teammates Plan"], "a plan authored by someone else must never surface as the user's own work")
	assert.True(t, topics["Legacy No Author Plan"], "a plan with no recorded authors is treated as the solo player's own")
}

func TestGatherPlans_NoLedgerConfigured_Empty(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir() // no .sageox/config.json — ledger cannot resolve
	got := gatherPlans(projectRoot, time.Time{}, time.Time{}, Identity{})
	assert.Empty(t, got)
}

// --- planIsMine ---

func TestPlanIsMine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		id   Identity
		info plan.PlanInfo
		want bool
	}{
		{"unresolved identity treats every plan as the solo player's own", Identity{}, plan.PlanInfo{Authors: []string{"anyone"}}, true},
		{"author matches display name", Identity{DisplayName: "Ryan"}, plan.PlanInfo{Authors: []string{"Ryan"}}, true},
		{"author match is case-insensitive", Identity{DisplayName: "ryan"}, plan.PlanInfo{Authors: []string{"RYAN"}}, true},
		{"author does not match", Identity{DisplayName: "Ryan"}, plan.PlanInfo{Authors: []string{"Someone Else"}}, false},
		{"no authors recorded is treated as mine (legacy/solo)", Identity{DisplayName: "Ryan"}, plan.PlanInfo{Authors: nil}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, planIsMine(tt.id, tt.info))
		})
	}
}
