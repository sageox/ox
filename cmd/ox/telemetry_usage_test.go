package main

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/telemetry"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capturedUsage struct {
	Event string
	Props map[string]any
	Opts  telemetry.CaptureOpts
}

func captureUsageForTest(t *testing.T, onCapture func(capturedUsage)) *[]capturedUsage {
	t.Helper()
	withIsolatedConfig(t)
	previousCtx := cliCtx
	cliCtx = nil
	t.Cleanup(func() { cliCtx = previousCtx })
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("SAGEOX_TELEMETRY", "true")
	t.Setenv("SAGEOX_TOKEN", "")
	previous := capturePostHogUsage
	t.Cleanup(func() { capturePostHogUsage = previous })
	var captured []capturedUsage
	capturePostHogUsage = func(event string, props map[string]any, opts telemetry.CaptureOpts) {
		c := capturedUsage{event, props, opts}
		captured = append(captured, c)
		if onCapture != nil {
			onCapture(c)
		}
	}
	return &captured
}

// Failure prevented: either event bypasses opt-out or attaches a production
// person to signed-out, team-token or non-production use.
func TestPostHogUsage_PersonAndOptOutBoundary(t *testing.T) {
	for _, event := range []string{"context", "murmur"} {
		for _, tt := range []struct{ name, ep, person, token, opt, want string }{
			{"signed out", endpoint.Default, "", "", "", ""},
			{"person", endpoint.Default, "usr_fixture", "opaque_fixture", "", "usr_fixture"},
			{"team token", endpoint.Default, "usr_fixture", "oxt_invalid_fixture", "", ""},
			{"test endpoint", "https://test.sageox.ai", "usr_fixture", "opaque_fixture", "", ""},
			{"opted out", endpoint.Default, "usr_fixture", "opaque_fixture", "1", ""},
		} {
			t.Run(event+"/"+tt.name, func(t *testing.T) {
				captured := captureUsageForTest(t, nil)
				t.Setenv("SAGEOX_ENDPOINT", tt.ep)
				t.Setenv("OX_XDG_DISABLE", "")
				t.Setenv("DO_NOT_TRACK", tt.opt)
				if tt.person != "" {
					require.NoError(t, auth.SaveTokenForEndpoint(tt.ep, &auth.StoredToken{AccessToken: tt.token, UserInfo: auth.UserInfo{UserID: tt.person}}))
				}
				cliCtx = &cli.Context{Cmd: &cobra.Command{Use: "agent"}}
				at := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
				if event == "context" {
					captureContextUsed("", contexttrace.Event{SessionID: "ses_019ff2f5-2079-7be1-b05e-8caad2772e61", OccurrenceSeq: 1, Timestamp: at.Format(time.RFC3339Nano), Evidence: "reported"}, true)
				} else {
					captureMurmurPublished("", ledger.MurmurFile{ID: "019ff2f5-2079-7be1-b05e-8caad2772e61", Timestamp: at, Topic: "general", Scope: "ledger", Importance: "normal"})
				}
				if tt.opt != "" {
					assert.Empty(t, *captured)
					return
				}
				require.Len(t, *captured, 1)
				assert.Equal(t, tt.want, (*captured)[0].Opts.PersonID)
				assert.Contains(t, []string{"person", "ai_coworker", "ci"}, (*captured)[0].Props["actor"])
			})
		}
	}
}

func strictUsageProps(t *testing.T, props map[string]any, target any) []byte {
	t.Helper()
	raw, err := json.Marshal(props)
	require.NoError(t, err)
	d := json.NewDecoder(bytes.NewReader(raw))
	// strict-reader-ok: guard the closed telemetry property list we write.
	d.DisallowUnknownFields()
	require.NoError(t, d.Decode(target))
	return raw
}

// Failure prevented: a damaged replay silently acquires a new capture time or
// emits an occurrence without its valid persisted session scope and ordinal.
func TestPostHogUsage_InvalidOccurrencesAreDropped(t *testing.T) {
	const scope = "ses_019ff2f5-2079-7be1-b05e-8caad2772e61"
	const timestamp = "2026-11-01T00:00:00Z"
	for _, tt := range []struct {
		name, sessionID, at string
		seq                 int
	}{
		// Failure prevented: arbitrary session text becomes analytics identity.
		{"invalid scope", "private-session", timestamp, 1},
		// Failure prevented: a trace with no allocated ordinal becomes a synthetic event.
		{"missing ordinal", scope, timestamp, 0},
		// Failure prevented: an invalid clock becomes time.Now during capture.
		{"invalid time", scope, "invalid-time", 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			captured := captureUsageForTest(t, nil)
			captureContextUsed("", contexttrace.Event{SessionID: tt.sessionID, OccurrenceSeq: tt.seq, Timestamp: tt.at, Evidence: "reported"}, true)
			assert.Empty(t, *captured)
		})
	}
}
