package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/require"
)

// TestPRHeaderCommand_WithholdsManualPublishingSession proves that a session
// held back by `session_publishing: manual` never reaches a PR body.
//
// Manual publishing suppresses start-registration, so the server has never
// heard of the session and its /c/ link 404s. A PR body is the worst place for
// that: it is public, it is permanent, and it outlives the session and the
// person who opened it.
//
// The distinction from the "pending" case is deliberate and is the point of
// this test: pending resolves on its own once the upload lands, so a coworker
// may knowingly force it with --allow-unconfirmed. Manual never resolves until
// someone runs an explicit upload, so forcing it would only ever produce a dead
// link — the flag must NOT override it.
//
// Failure prevented: a manual-mode session ships a permanently dead /c/ link in
// a public PR. Red-first: drop the manual check in autoSessionURL and the
// no-/c/-link assertion below fails.
func TestPRHeaderCommand_WithholdsManualPublishingSession(t *testing.T) {
	root := prHeaderProject(t, true)

	// A real local recording with a valid ses_ id. State stays "deferred" —
	// which is exactly what manual mode leaves behind, and notably is NOT
	// "pending", so the pre-existing guard does not catch it.
	const manualID = "ses_01920000-0000-7000-8000-0000000000cd"
	startFakeRecording(t, root, session.RecordingState{
		SessionPath:                filepath.Join(root, "sessions", "2026-08-20T10-00-devon-Oxman01"),
		SessionID:                  manualID,
		LifecycleRegistrationState: "deferred",
	})

	restore := stubSessionPublishing(t, config.SessionPublishingManual)
	defer restore()

	// Default: no link, and with nothing else to link, no header at all.
	c, out, _ := buildPRHeaderCmd()
	require.NoError(t, runPRHeader(c, nil))
	require.NotContains(t, out.String(), "/c/ses_",
		"a manual-publishing session must never be linked — the link 404s")
	require.Empty(t, strings.TrimSpace(out.String()),
		"withheld session + nothing else => no header at all")

	// --allow-unconfirmed must NOT force it. Unlike "pending", this link can
	// never become valid on its own.
	c2, out2, _ := buildPRHeaderCmd()
	require.NoError(t, c2.Flags().Set("allow-unconfirmed", "true"))
	require.NoError(t, runPRHeader(c2, nil))
	require.NotContains(t, out2.String(), "/c/"+manualID,
		"--allow-unconfirmed must not force a link that can never resolve")

	// A real plan still carries the header — the gate keys on the session, not
	// on the whole feature.
	cp, outp, _ := buildPRHeaderCmd()
	require.NoError(t, cp.Flags().Set("plan", "pln_4d8e2f"))
	require.NoError(t, runPRHeader(cp, nil))
	require.Contains(t, outp.String(), "https://sageox.ai/plan/pln_4d8e2f",
		"a plan alone still carries the header")
	require.NotContains(t, outp.String(), "/c/ses_",
		"the manual-mode session is still not linked")
}

// TestAutoSessionURL_AutoModeStillLinks is the regression guard that matters
// most: auto is the default and must be completely unaffected by the gate above.
func TestAutoSessionURL_AutoModeStillLinks(t *testing.T) {
	root := prHeaderProject(t, true)
	const confirmedID = "ses_01920000-0000-7000-8000-0000000000ce"
	startFakeRecording(t, root, session.RecordingState{
		SessionPath:                filepath.Join(root, "sessions", "2026-08-20T10-00-devon-Oxauto1"),
		SessionID:                  confirmedID,
		LifecycleRegistrationState: "deferred",
	})

	restore := stubSessionPublishing(t, config.SessionPublishingAuto)
	defer restore()

	c, out, _ := buildPRHeaderCmd()
	require.NoError(t, runPRHeader(c, nil))
	require.Contains(t, out.String(), "/c/"+confirmedID,
		"auto publishing must still link the session")
}

// TestPRHeaderCommand_PendingAndManualIsHardWithheld covers the ordering the
// reviewer caught: a session can be BOTH pending and manual.
//
// With the manual guard behind the pending check, that combination reported
// unconfirmed=true, so ox told the coworker to re-run with --allow-unconfirmed
// — a flag that then produced no URL at all, because manual withholds
// unconditionally. Advice that cannot work is worse than no advice: it costs a
// second run and teaches people the flag is broken.
func TestPRHeaderCommand_PendingAndManualIsHardWithheld(t *testing.T) {
	root := prHeaderProject(t, true)
	const bothID = "ses_01920000-0000-7000-8000-0000000000df"
	startFakeRecording(t, root, session.RecordingState{
		SessionPath:                filepath.Join(root, "sessions", "2026-08-20T10-00-devon-Oxboth1"),
		SessionID:                  bothID,
		LifecycleRegistrationState: "pending", // pending AND manual
	})

	restore := stubSessionPublishing(t, config.SessionPublishingManual)
	defer restore()

	// No link, and crucially no suggestion of a flag that cannot help.
	c, out, errb := buildPRHeaderCmd()
	require.NoError(t, runPRHeader(c, nil))
	require.NotContains(t, out.String(), "/c/ses_", "must not link a manual-mode session")
	require.NotContains(t, errb.String(), "--allow-unconfirmed",
		"must not recommend a flag that yields no URL under manual publishing")

	// And the flag genuinely does nothing here, which is why recommending it
	// would have been a dead end.
	c2, out2, _ := buildPRHeaderCmd()
	require.NoError(t, c2.Flags().Set("allow-unconfirmed", "true"))
	require.NoError(t, runPRHeader(c2, nil))
	require.NotContains(t, out2.String(), "/c/"+bothID,
		"--allow-unconfirmed must not force a link that can never resolve")
}
