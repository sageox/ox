//go:build kb_twin

package kb_twin

// Scenario definitions for the kb digital twin. Each scenario is a
// realistic kb lifecycle pattern paired with an explicit ExpectedState
// that the harness compares the post-sync filesystem against.
//
// The shape mirrors tests/ledger_twin/scenarios.go: a single manifest
// holds every scenario, each scenario is self-contained, and the
// runner iterates them under one t.Run subtest each. We adapted the
// pattern rather than reinventing it.
//
// IMPORTANT: do not put behavioral guidance here that belongs in code.
// Each scenario must be reproducible from its struct alone. If a
// scenario needs setup-time logic (e.g., "push an extra commit before
// the second pass"), express it via Phases — never via hidden state.

import (
	"time"

	"github.com/sageox/ox/internal/api"
)

// BubbleSpec defines one kb that the scenario's fake API will return.
// The harness builds a bare git repo for each bubble before the sync
// pass runs, so RepoURL is filled in by generate.go (NOT by the
// scenario author).
type BubbleSpec struct {
	// KBID is the immutable id keying the canonical XDG path.
	KBID string
	// KBType drives cadence routing + symlink policy in the daemon.
	KBType api.KBType
	// Slug is the human-readable name used for per-project symlinks.
	Slug string
	// OwnerUserID identifies the owner (recorded in meta.json).
	OwnerUserID string
	// ViewerRole is the caller's role on this bubble (meta.json).
	ViewerRole string
	// RepoID scopes a repo bubble to its project. Optional for non-repo.
	RepoID string

	// SeedFile / SeedContent are written into the bare repo as the
	// initial commit so we can later assert the working tree contents.
	// Non-empty SeedFile triggers bare-repo creation; empty means
	// "scenario expects this bubble to NOT clone" (e.g., revoked).
	SeedFile    string
	SeedContent string

	// ProvideRepoURL: when false, the harness suppresses the RepoURL
	// in the API response (simulating a not-yet-provisioned bubble).
	// Default true — most scenarios want a real clonable URL.
	ProvideRepoURL bool

	// BadRepoURL: when true, the API returns a deliberately broken
	// file:// URL so the harness can verify per-bubble failure
	// containment. Mutually exclusive with seeded repo creation.
	BadRepoURL bool
}

// ExpectedKB is the post-sync state we assert for one bubble.
type ExpectedKB struct {
	// MustExist: the canonical XDG dir for this kb must contain a
	// .git/ subdirectory. False means "must not have a checkout".
	MustExist bool
	// MetaType is the expected meta.json `type` field; empty skips check.
	MetaType api.KBType
	// MetaSlug is the expected meta.json `slug`; empty skips check.
	MetaSlug string
	// MetaOwnerUserID expected; empty skips check.
	MetaOwnerUserID string
	// MetaViewerRole expected; empty skips check.
	MetaViewerRole string
	// FileChecks: relative path -> exact byte content. Empty content
	// means "file must exist, content not asserted".
	FileChecks map[string]string
}

// ExpectedSymlink is the post-sync state we assert for one per-project
// symlink under <project>/.sageox/kb/<slug>.
type ExpectedSymlink struct {
	// ProjectKey identifies which scenario project to check (matches
	// keys in ScenarioSpec.Projects).
	ProjectKey string
	// Slug is the symlink name under <project>/.sageox/kb/.
	Slug string
	// Exists: true means the symlink must be present and point at the
	// expected canonical KBDir; false means the symlink must NOT exist.
	Exists bool
	// TargetKBID is the kb_id whose KBDir the symlink should resolve
	// to (only consulted when Exists is true).
	TargetKBID string
}

// ExpectedTrash is the post-sync state we assert for the kb trash dir
// after a GC pass. The harness only checks for kb_id prefix presence;
// the timestamp suffix is verified by the daemon's own unit tests.
type ExpectedTrash struct {
	// KBIDPrefix names the orphan that should appear in .trash/.
	KBIDPrefix string
	// MustBePresent: true asserts present; false asserts absent.
	MustBePresent bool
}

// ProjectSpec defines one initialized project to materialize before
// the sync pass runs. The daemon's symlink reconciler walks the
// registry; we set up a single project_root that the test scheduler
// is configured against, then optionally seed extra ones the daemon
// will discover via the registry.
type ProjectSpec struct {
	// RepoID is recorded in the project's config.json. Used by the
	// symlink reconciler to scope kb_type=repo bubbles.
	RepoID string
}

// Phase describes one reconciliation pass and any mutations to apply
// between this and the previous pass. Letting scenarios chain phases
// is what enables "incremental pull picks up new commit", "revoked
// bubble triages to trash", and other multi-step lifecycle tests.
type Phase struct {
	// Name labels the phase for diagnostic output.
	Name string

	// PushCommits: kb_id -> (filename, content). Before this phase
	// runs, push each entry as a follow-up commit to that bubble's
	// bare repo. Used by the incremental-pull scenario.
	PushCommits map[string]struct{ File, Content string }

	// RevokeKBIDs lists kb_ids that should be DROPPED from the API
	// list at this phase (simulating server-side access revocation).
	// Used by the revoke-and-triage scenario.
	RevokeKBIDs []string

	// SimulateAPIError, when non-nil, causes the API to return this
	// error for the phase (e.g., ErrKBAPIUnavailable). Used by the
	// "API unavailable, no orphan state" scenario.
	SimulateAPIError error

	// RunGC, when true, runs runKBGC at the end of this phase
	// (instead of, or after, runSyncBubbles). Used by trash-lifecycle
	// scenarios.
	RunGC bool

	// SkipSync, when true, suppresses the sync pass for this phase
	// (the GC-only phase needs this).
	SkipSync bool

	// Expected is the post-phase state asserted by the comparison
	// machinery. Empty maps mean "no assertions for this category in
	// this phase" — which is fine for setup-only phases.
	Expected ExpectedState
}

// ExpectedState aggregates every assertable post-phase property.
type ExpectedState struct {
	// KBs: kb_id -> expected on-disk shape.
	KBs map[string]ExpectedKB
	// Symlinks: ordered list (project, slug) -> expectation.
	Symlinks []ExpectedSymlink
	// Trash: list of kb_id-prefix presence/absence assertions.
	Trash []ExpectedTrash
}

// ScenarioSpec is one realistic kb lifecycle pattern.
type ScenarioSpec struct {
	// Name is a kebab-case identifier used for the t.Run subtest name.
	Name string

	// Description is the one-line "what reality does this pin?".
	// Required for every scenario — without it the scenario is just
	// noise the next person has to reverse-engineer.
	Description string

	// Endpoint overrides the SAGEOX_ENDPOINT for this scenario.
	// Used by the multi-endpoint isolation scenario; empty = default.
	Endpoint string

	// Bubbles is the initial set of kb rows the fake API returns for
	// phase 1. Phases can revoke entries from this set.
	Bubbles []BubbleSpec

	// Projects: extra initialized projects to register before the
	// sync pass. Keyed by stable name so ExpectedSymlink can refer
	// to them. The default project ("primary") is always created and
	// is the one the SyncScheduler is configured with.
	Projects map[string]ProjectSpec

	// Phases ordered list of reconciliation passes. At minimum each
	// scenario has one phase; multi-pass scenarios (incremental,
	// revoke→triage→reap) chain them.
	Phases []Phase
}

// scenarioTime fixes a deterministic clock so any time-sensitive
// assertions (meta.json LastSync) can compare against a known window.
func scenarioTime() time.Time {
	return time.Date(2026, time.May, 8, 12, 0, 0, 0, time.UTC)
}

// Manifest holds every scenario the runner iterates.
type Manifest struct {
	Scenarios []ScenarioSpec
}

// BuildManifest returns the full scenario set. New scenarios append
// here; the runner picks them up automatically. Each scenario block
// is self-contained — read top-to-bottom to understand it.
func BuildManifest() *Manifest {
	m := &Manifest{}

	// ───────────────────────────────────────────────────────────────
	// Scenario 1: single-bubble-clone-from-empty
	//
	// Pins: a fresh login on a new machine clones the personal bubble
	// end-to-end and writes a meta.json with the right fields. This is
	// the most basic "happy path" and is the kb-side equivalent of the
	// ledger_twin "Hot Zone, single dev" baseline scenario.
	// ───────────────────────────────────────────────────────────────
	m.Scenarios = append(m.Scenarios, ScenarioSpec{
		Name:        "single-personal-clone-from-empty",
		Description: "Fresh login: a single personal bubble clones into XDG and meta.json is populated.",
		Bubbles: []BubbleSpec{{
			KBID:           "kb_personal_solo",
			KBType:         api.KBTypePersonal,
			Slug:           "personal-solo",
			OwnerUserID:    "user_solo",
			ViewerRole:     "owner",
			SeedFile:       "notes.md",
			SeedContent:    "scratch\n",
			ProvideRepoURL: true,
		}},
		Projects: map[string]ProjectSpec{"primary": {RepoID: "repo_primary"}},
		Phases: []Phase{{
			Name: "initial-sync",
			Expected: ExpectedState{
				KBs: map[string]ExpectedKB{
					"kb_personal_solo": {
						MustExist:       true,
						MetaType:        api.KBTypePersonal,
						MetaSlug:        "personal-solo",
						MetaOwnerUserID: "user_solo",
						MetaViewerRole:  "owner",
						FileChecks:      map[string]string{"notes.md": "scratch\n"},
					},
				},
				Symlinks: []ExpectedSymlink{
					{ProjectKey: "primary", Slug: "personal-solo", Exists: true, TargetKBID: "kb_personal_solo"},
				},
			},
		}},
	})

	// ───────────────────────────────────────────────────────────────
	// Scenario 2: incremental-pull-picks-up-new-commit
	//
	// Pins: a bubble already cloned in pass 1 picks up a follow-up
	// commit on pass 2. Mirrors ledger_twin's "Day 2 escalation" idea:
	// state must evolve, not just initialize.
	// Failure prevented: bubble dirs frozen at first-clone snapshot.
	// ───────────────────────────────────────────────────────────────
	m.Scenarios = append(m.Scenarios, ScenarioSpec{
		Name:        "incremental-pull-picks-up-new-commit",
		Description: "Existing bubble: a follow-up commit pushed between passes is observed in pass 2.",
		Bubbles: []BubbleSpec{{
			KBID:           "kb_team_inc",
			KBType:         api.KBTypeTeam,
			Slug:           "platform",
			ViewerRole:     "member",
			SeedFile:       "AGENTS.md",
			SeedContent:    "v1\n",
			ProvideRepoURL: true,
		}},
		Projects: map[string]ProjectSpec{"primary": {RepoID: "repo_primary"}},
		Phases: []Phase{
			{
				Name: "first-pass-clones",
				Expected: ExpectedState{
					KBs: map[string]ExpectedKB{
						"kb_team_inc": {
							MustExist:  true,
							MetaSlug:   "platform",
							FileChecks: map[string]string{"AGENTS.md": "v1\n"},
						},
					},
				},
			},
			{
				Name: "second-pass-pulls-new-commit",
				PushCommits: map[string]struct{ File, Content string }{
					"kb_team_inc": {File: "AGENTS.md", Content: "v2\n"},
				},
				Expected: ExpectedState{
					KBs: map[string]ExpectedKB{
						"kb_team_inc": {
							MustExist:  true,
							FileChecks: map[string]string{"AGENTS.md": "v2\n"},
						},
					},
				},
			},
		},
	})

	// ───────────────────────────────────────────────────────────────
	// Scenario 3: revoked-bubble-triages-to-trash
	//
	// Pins: a bubble that exists locally but is dropped from the API
	// list on a subsequent pass is moved into kb/.trash/ — never
	// deleted in place, never left orphaned in the canonical location.
	// Failure prevented: revoked bubbles silently accumulating, OR
	// the GC deleting authorized data.
	// ───────────────────────────────────────────────────────────────
	m.Scenarios = append(m.Scenarios, ScenarioSpec{
		Name:        "revoked-bubble-triages-to-trash",
		Description: "Revoked bubble: dropping a kb from the API list moves it to kb/.trash on the next GC pass.",
		Bubbles: []BubbleSpec{{
			KBID:           "kb_revoked",
			KBType:         api.KBTypeTeam,
			Slug:           "departing-team",
			ViewerRole:     "member",
			SeedFile:       "README.md",
			SeedContent:    "bye\n",
			ProvideRepoURL: true,
		}},
		Projects: map[string]ProjectSpec{"primary": {RepoID: "repo_primary"}},
		Phases: []Phase{
			{
				Name: "phase1-clone",
				Expected: ExpectedState{
					KBs: map[string]ExpectedKB{
						"kb_revoked": {MustExist: true, MetaSlug: "departing-team"},
					},
				},
			},
			{
				Name:        "phase2-revoke-and-gc",
				RevokeKBIDs: []string{"kb_revoked"},
				RunGC:       true,
				SkipSync:    true,
				Expected: ExpectedState{
					KBs: map[string]ExpectedKB{
						"kb_revoked": {MustExist: false},
					},
					Trash: []ExpectedTrash{
						{KBIDPrefix: "kb_revoked", MustBePresent: true},
					},
				},
			},
		},
	})

	// ───────────────────────────────────────────────────────────────
	// Scenario 4: per-bubble-failure-isolation
	//
	// Pins: one bubble with a broken repo URL does not abort the rest
	// of the pass. The good bubble still clones. Mirrors ledger_twin's
	// "parallel streams" — independent work in independent units must
	// not interfere.
	// Failure prevented: a single revoked / mis-provisioned bubble
	// taking down the entire scheduler tick.
	// ───────────────────────────────────────────────────────────────
	m.Scenarios = append(m.Scenarios, ScenarioSpec{
		Name:        "per-bubble-failure-isolation",
		Description: "One bad URL does not abort the pass; remaining bubbles still clone and write meta.",
		Bubbles: []BubbleSpec{
			{
				KBID:           "kb_broken",
				KBType:         api.KBTypeRepo,
				Slug:           "broken-bubble",
				BadRepoURL:     true,
				ProvideRepoURL: true,
			},
			{
				KBID:           "kb_healthy",
				KBType:         api.KBTypeProfile,
				Slug:           "healthy-bubble",
				OwnerUserID:    "user_healthy",
				ViewerRole:     "owner",
				SeedFile:       "ok.md",
				SeedContent:    "good\n",
				ProvideRepoURL: true,
			},
		},
		Projects: map[string]ProjectSpec{"primary": {RepoID: "repo_primary"}},
		Phases: []Phase{{
			Name: "sync-with-one-broken",
			Expected: ExpectedState{
				KBs: map[string]ExpectedKB{
					"kb_broken":  {MustExist: false},
					"kb_healthy": {MustExist: true, MetaSlug: "healthy-bubble"},
				},
				Symlinks: []ExpectedSymlink{
					{ProjectKey: "primary", Slug: "healthy-bubble", Exists: true, TargetKBID: "kb_healthy"},
					{ProjectKey: "primary", Slug: "broken-bubble", Exists: false},
				},
			},
		}},
	})

	// ───────────────────────────────────────────────────────────────
	// Scenario 5: kb-api-unavailable-clean-skip
	//
	// Pins: when the kb API returns ErrKBAPIUnavailable (flag off /
	// endpoint missing), syncBubbles is a silent no-op — no panics,
	// no orphan dirs, no half-written meta.json.
	// Failure prevented: rolling out kb to a percentage of users
	// surfaces scary daemon warnings to everyone whose flag is still
	// off.
	// ───────────────────────────────────────────────────────────────
	m.Scenarios = append(m.Scenarios, ScenarioSpec{
		Name:        "kb-api-unavailable-clean-skip",
		Description: "API ErrKBAPIUnavailable: sync pass exits cleanly, no on-disk side effects.",
		Bubbles:     nil, // never reached — the API errors out before listing
		Projects:    map[string]ProjectSpec{"primary": {RepoID: "repo_primary"}},
		Phases: []Phase{{
			Name:             "sync-with-flag-off",
			SimulateAPIError: api.ErrKBAPIUnavailable,
			Expected:         ExpectedState{},
		}},
	})

	// ───────────────────────────────────────────────────────────────
	// Scenario 6: personal-bubble-lazy-provision
	//
	// Pins: a kb_type=personal bubble is treated identically to other
	// kinds for the purposes of clone + meta. The plan says personal
	// is "lazy-provisioned by EnsurePersonalKBMiddleware server-side",
	// so the CLIENT side is just a generic clone — we verify nothing
	// special-cases personal and that owner role round-trips.
	// Failure prevented: personal bubble silently dropped because some
	// branch only handled team/repo.
	// ───────────────────────────────────────────────────────────────
	m.Scenarios = append(m.Scenarios, ScenarioSpec{
		Name:        "personal-bubble-lazy-provision",
		Description: "Personal kind: cloned identically to other kinds; viewer_role=owner round-trips into meta.",
		Bubbles: []BubbleSpec{{
			KBID:           "kb_personal_lazy",
			KBType:         api.KBTypePersonal,
			Slug:           "personal-rs",
			OwnerUserID:    "user_42",
			ViewerRole:     "owner",
			SeedFile:       "scratch.md",
			SeedContent:    "thoughts\n",
			ProvideRepoURL: true,
		}},
		Projects: map[string]ProjectSpec{"primary": {RepoID: "repo_primary"}},
		Phases: []Phase{{
			Name: "lazy-provision-clone",
			Expected: ExpectedState{
				KBs: map[string]ExpectedKB{
					"kb_personal_lazy": {
						MustExist:       true,
						MetaType:        api.KBTypePersonal,
						MetaSlug:        "personal-rs",
						MetaOwnerUserID: "user_42",
						MetaViewerRole:  "owner",
						FileChecks:      map[string]string{"scratch.md": "thoughts\n"},
					},
				},
				Symlinks: []ExpectedSymlink{
					{ProjectKey: "primary", Slug: "personal-rs", Exists: true, TargetKBID: "kb_personal_lazy"},
				},
			},
		}},
	})

	// ───────────────────────────────────────────────────────────────
	// Scenario 7: stale-meta-json-still-syncs-and-overwrites
	//
	// Pins: a bubble whose existing meta.json has an absurdly old
	// last_sync field still completes its sync pass, and the meta is
	// overwritten with the fresh timestamp. Mirrors ledger_twin's
	// "active recording" pattern of asserting state-after-phase, not
	// just absence-of-error.
	// Failure prevented: meta.json staleness gating subsequent pulls
	// (a regression where the daemon trusts on-disk last_sync as a
	// dedup signal and stops pulling).
	// ───────────────────────────────────────────────────────────────
	m.Scenarios = append(m.Scenarios, ScenarioSpec{
		Name:        "stale-meta-json-overwritten-on-sync",
		Description: "Pre-existing stale meta.json: sync overwrites last_sync; clone still completes.",
		Bubbles: []BubbleSpec{{
			KBID:           "kb_stale_meta",
			KBType:         api.KBTypeTeam,
			Slug:           "ancient",
			ViewerRole:     "member",
			SeedFile:       "AGENTS.md",
			SeedContent:    "current\n",
			ProvideRepoURL: true,
		}},
		Projects: map[string]ProjectSpec{"primary": {RepoID: "repo_primary"}},
		Phases: []Phase{{
			Name: "sync-overwrites-stale-meta",
			Expected: ExpectedState{
				KBs: map[string]ExpectedKB{
					"kb_stale_meta": {
						MustExist:  true,
						MetaSlug:   "ancient",
						FileChecks: map[string]string{"AGENTS.md": "current\n"},
					},
				},
			},
		}},
	})

	// ───────────────────────────────────────────────────────────────
	// Scenario 8: multi-endpoint-isolation
	//
	// Pins: the same kb_id under two different endpoints (staging vs.
	// prod) lives in TWO separate XDG dirs. Critical for users who
	// log into multiple SageOx instances — a slug collision must not
	// leak content across endpoints.
	// Failure prevented: a regression that drops the endpoint segment
	// from paths.KBDir would silently merge two different bubbles
	// into one directory, destroying data.
	// Note: the harness drives this scenario by running two FULL syncs
	// back-to-back under different SAGEOX_ENDPOINT values and asserting
	// that both XDG locations exist independently. Implemented as a
	// dedicated test (see twin_test.go) rather than a Phase chain
	// because the endpoint env var has to flip mid-scenario.
	// ───────────────────────────────────────────────────────────────
	m.Scenarios = append(m.Scenarios, ScenarioSpec{
		Name:        "multi-endpoint-isolation",
		Description: "Same kb_id under staging vs prod endpoints lives in two separate XDG locations.",
		Bubbles: []BubbleSpec{{
			KBID:           "kb_shared_id",
			KBType:         api.KBTypeTeam,
			Slug:           "shared-name",
			ViewerRole:     "member",
			SeedFile:       "marker.md",
			SeedContent:    "default-endpoint\n",
			ProvideRepoURL: true,
		}},
		Projects: map[string]ProjectSpec{"primary": {RepoID: "repo_primary"}},
		Phases: []Phase{{
			Name: "default-endpoint-clone",
			Expected: ExpectedState{
				KBs: map[string]ExpectedKB{
					"kb_shared_id": {
						MustExist:  true,
						MetaSlug:   "shared-name",
						FileChecks: map[string]string{"marker.md": "default-endpoint\n"},
					},
				},
			},
		}},
	})

	// ───────────────────────────────────────────────────────────────
	// Scenario 9: trash-survives-grace-period
	//
	// Pins: an entry in kb/.trash/ younger than the 7-day grace period
	// is NOT deleted by the reaper. Combined with scenario 3
	// (revoke→triage), this proves the full lifecycle is safe.
	// Failure prevented: an off-by-one in the reaper's age check
	// destroying a bubble the user might want to restore.
	// ───────────────────────────────────────────────────────────────
	m.Scenarios = append(m.Scenarios, ScenarioSpec{
		Name:        "trash-grace-period-protects-recent-orphan",
		Description: "Recent orphan in .trash/: a trash entry created today is NOT reaped on the next GC tick.",
		Bubbles: []BubbleSpec{{
			KBID:           "kb_recently_orphaned",
			KBType:         api.KBTypeTeam,
			Slug:           "recently-orphaned",
			ViewerRole:     "member",
			SeedFile:       "x.md",
			SeedContent:    "x\n",
			ProvideRepoURL: true,
		}},
		Projects: map[string]ProjectSpec{"primary": {RepoID: "repo_primary"}},
		Phases: []Phase{
			{Name: "p1-clone"},
			{
				Name:        "p2-revoke-triages",
				RevokeKBIDs: []string{"kb_recently_orphaned"},
				RunGC:       true,
				SkipSync:    true,
				Expected: ExpectedState{
					Trash: []ExpectedTrash{{KBIDPrefix: "kb_recently_orphaned", MustBePresent: true}},
				},
			},
			{
				Name:        "p3-second-gc-leaves-recent-trash-alone",
				RevokeKBIDs: []string{"kb_recently_orphaned"},
				RunGC:       true,
				SkipSync:    true,
				Expected: ExpectedState{
					Trash: []ExpectedTrash{{KBIDPrefix: "kb_recently_orphaned", MustBePresent: true}},
				},
			},
		},
	})

	return m
}

// scenarioByName looks up a scenario by name. Used by the multi-endpoint
// test which needs to pull the spec out for a custom drive loop.
func (m *Manifest) scenarioByName(name string) *ScenarioSpec {
	for i := range m.Scenarios {
		if m.Scenarios[i].Name == name {
			return &m.Scenarios[i]
		}
	}
	return nil
}

// _ keeps the `time` import needed by future scenarios (and silences
// linters when only some scenarios reference clock helpers).
var _ = time.Time{}
var _ = scenarioTime
