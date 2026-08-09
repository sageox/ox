package main

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func draftCfg(publishTurn, refreshEvery int) *config.ResolvedSessionDraft {
	return &config.ResolvedSessionDraft{Enabled: true, PublishTurn: publishTurn, RefreshEvery: refreshEvery}
}

// TestDraftDecision_Boundaries pins the exact publish/refresh sequence.
//
// Every entry here is an off-by-one that would ship silently: a session that
// never publishes, one that publishes twice, or one that emits a redundant
// commit on the publish turn itself.
func TestDraftDecision_Boundaries(t *testing.T) {
	tests := []struct {
		name        string
		cfg         *config.ResolvedSessionDraft
		turns       int
		wantActions []draftAction // index i == turn i+1
	}{
		{
			name:  "default cadence: publish at 2, refresh every 10 measured from the publish turn",
			cfg:   draftCfg(2, 10),
			turns: 23,
			wantActions: []draftAction{
				draftActionNone,    // 1 — a one-shot question never publishes
				draftActionPublish, // 2
				draftActionNone, draftActionNone, draftActionNone, draftActionNone,
				draftActionNone, draftActionNone, draftActionNone, draftActionNone,
				draftActionNone,
				draftActionRefresh, // 12 == publishTurn + 10
				draftActionNone, draftActionNone, draftActionNone, draftActionNone,
				draftActionNone, draftActionNone, draftActionNone, draftActionNone,
				draftActionNone,
				draftActionRefresh, // 22
				draftActionNone,    // 23
			},
		},
		{
			name:        "publishTurn 1 publishes immediately and never republishes",
			cfg:         draftCfg(1, 10),
			turns:       3,
			wantActions: []draftAction{draftActionPublish, draftActionNone, draftActionNone},
		},
		{
			name:        "publishTurn 0 is treated as turn 1, not as publish-on-every-turn",
			cfg:         draftCfg(0, 10),
			turns:       3,
			wantActions: []draftAction{draftActionPublish, draftActionNone, draftActionNone},
		},
		{
			name:        "refreshEvery 0 disables refresh without dividing by zero",
			cfg:         draftCfg(2, 0),
			turns:       6,
			wantActions: []draftAction{draftActionNone, draftActionPublish, draftActionNone, draftActionNone, draftActionNone, draftActionNone},
		},
		{
			name:  "refreshEvery 1 refreshes every turn AFTER publish, never on the publish turn",
			cfg:   draftCfg(2, 1),
			turns: 5,
			wantActions: []draftAction{
				draftActionNone, draftActionPublish,
				draftActionRefresh, draftActionRefresh, draftActionRefresh,
			},
		},
		{
			name:  "refresh boundary: publishTurn+R-1 none, +R refresh, +R+1 none",
			cfg:   draftCfg(2, 3),
			turns: 7,
			wantActions: []draftAction{
				draftActionNone, draftActionPublish,
				draftActionNone, draftActionNone,
				draftActionRefresh, // 5 == 2+3
				draftActionNone,
				draftActionNone,
			},
		},
		{
			name:        "threshold beyond the session length never publishes",
			cfg:         draftCfg(99, 10),
			turns:       5,
			wantActions: []draftAction{draftActionNone, draftActionNone, draftActionNone, draftActionNone, draftActionNone},
		},
		{
			name:        "disabled never acts",
			cfg:         &config.ResolvedSessionDraft{Enabled: false, PublishTurn: 2, RefreshEvery: 10},
			turns:       15,
			wantActions: make([]draftAction, 15),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Len(t, tc.wantActions, tc.turns, "test fixture: one expectation per turn")
			publishedTurn := 0
			published := false
			for turn := 1; turn <= tc.turns; turn++ {
				got := draftDecision(turn, publishedTurn, published, tc.cfg)
				assert.Equal(t, tc.wantActions[turn-1], got, "turn %d", turn)
				if got != draftActionNone {
					publishedTurn = turn
					published = true
				}
			}
		})
	}
}

// TestDraftDecision_NilConfigIsDisabled — a nil resolved config must not panic
// in a hook. A panic here fails the user's turn.
func TestDraftDecision_NilConfigIsDisabled(t *testing.T) {
	assert.Equal(t, draftActionNone, draftDecision(5, 0, false, nil))
}

// TestDraftDecision_Property is a randomized property test over the scheduling
// rule, verified against a reference model recomputed by linear scan.
//
// We avoid a third-party generator (gopter / rapid) to keep dependencies
// minimal, and run with a fixed seed for determinism — the same convention as
// TestApplySegmentMask_Property in internal/session.
//
// The invariant that matters most is `publishes <= 1`. A second publish would
// re-run the whole write+commit path against a directory that already has a
// placeholder, and at the git layer that is an extra commit per turn per agent
// on the shared ledger.
func TestDraftDecision_Property(t *testing.T) {
	const trials = 200
	rng := rand.New(rand.NewSource(1)) //nolint:gosec // deterministic test input, not security

	for trial := 0; trial < trials; trial++ {
		publishTurn := rng.Intn(5)  // 0..4
		refreshEvery := rng.Intn(7) // 0..6
		turns := 1 + rng.Intn(40)
		cfg := draftCfg(publishTurn, refreshEvery)

		// reference model — deliberately recomputed by linear scan rather than
		// sharing any code with draftDecision.
		type step struct {
			turn   int
			action draftAction
		}
		var want []step
		modelPublished := 0
		for turn := 1; turn <= turns; turn++ {
			effectiveThreshold := publishTurn
			if effectiveThreshold < 1 {
				effectiveThreshold = 1
			}
			var act draftAction
			switch {
			case turn < effectiveThreshold:
				act = draftActionNone
			case modelPublished == 0:
				act = draftActionPublish
			case refreshEvery > 0 && (turn-modelPublished)%refreshEvery == 0:
				act = draftActionRefresh
			default:
				act = draftActionNone
			}
			if act != draftActionNone {
				modelPublished = turn
			}
			want = append(want, step{turn, act})
		}

		// drive the real function
		var got []step
		publishedTurn := 0
		published := false
		publishes := 0
		for turn := 1; turn <= turns; turn++ {
			act := draftDecision(turn, publishedTurn, published, cfg)
			if act == draftActionPublish {
				publishes++
			}
			if act != draftActionNone {
				publishedTurn = turn
				published = true
			}
			got = append(got, step{turn, act})
			require.LessOrEqual(t, publishes, 1,
				"trial=%d publishTurn=%d refreshEvery=%d turns=%d: published more than once",
				trial, publishTurn, refreshEvery, turns)
		}

		require.Equal(t, want, got,
			"trial=%d publishTurn=%d refreshEvery=%d turns=%d", trial, publishTurn, refreshEvery, turns)
	}
}

// TestDraftDecision_RefreshAnchoredToPublishNotAbsoluteTurn.
//
// Catches the specific bug of writing `turn % refreshEvery` instead of
// `(turn - publishedTurn) % refreshEvery`. With an absolute anchor, a session
// whose publish was delayed refreshes almost immediately after publishing
// rather than a full interval later — burning a ledger commit for no new
// information.
func TestDraftDecision_RefreshAnchoredToPublishNotAbsoluteTurn(t *testing.T) {
	cfg := draftCfg(7, 5)

	// publish lands late, at turn 7
	require.Equal(t, draftActionPublish, draftDecision(7, 0, false, cfg))

	// turn 10 is a multiple of 5 in ABSOLUTE terms but only 3 turns after
	// publish — it must not refresh.
	assert.Equal(t, draftActionNone, draftDecision(10, 7, true, cfg),
		"refresh must be anchored to the publish turn, not the absolute turn number")

	// 7 + 5 == 12 is the first legitimate refresh.
	assert.Equal(t, draftActionRefresh, draftDecision(12, 7, true, cfg))
}

func TestDraftAction_String(t *testing.T) {
	// Guards the zero value: draftActionNone must be the zero value so an
	// uninitialized draftAction is inert rather than accidentally publishing.
	var zero draftAction
	assert.Equal(t, draftActionNone, zero, fmt.Sprintf("zero draftAction must be none, got %d", zero))
}
