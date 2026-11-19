package materialize

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/trace/model"
	"github.com/stretchr/testify/require"
)

func TestCorruptRecordShapesNeverPublish(t *testing.T) {
	for _, payload := range []string{"null\n", "[]\n", "42\n", "{}{}\n"} {
		t.Run(payload, func(t *testing.T) {
			spool, cache := setup(t)
			writeSpool(t, spool, nativeA, "spans", payload)
			_, err := Build(spool, cache, &model.Capture{Boundaries: []model.Boundary{boundary("start", 0, 0), boundary("stop", int64(len(payload)), 0)}})
			require.Error(t, err)
			require.NoFileExists(t, filepath.Join(cache, SpansFile))
			require.NoFileExists(t, filepath.Join(cache, EventsFile))
		})
	}
}

func TestInvalidBoundaryHistoryNeverPublishes(t *testing.T) {
	for _, mode := range []string{"no timestamp", "duplicate start", "early stop"} {
		t.Run(mode, func(t *testing.T) {
			spool, cache := setup(t)
			middle := boundary("native-session", 0, 0)
			switch mode {
			case "no timestamp":
				middle.At = time.Time{}
			case "duplicate start":
				middle.Action = "start"
			case "early stop":
				middle.Action = "stop"
			}
			_, err := Build(spool, cache, &model.Capture{Boundaries: []model.Boundary{boundary("start", 0, 0), middle, boundary("stop", 0, 0)}})
			require.Error(t, err)
			require.NoDirExists(t, cache)
		})
	}
}

func TestAdjacentNativeBoundariesPreserveScrubAndUnknownObservations(t *testing.T) {
	spool, cache := setup(t)
	// OTLP AnyValue may contain maps as well as key/value arrays. Malformed
	// version observations must remain unknown, not become guessed metadata.
	payload := "{\"user.email\":\"PRIVATE\",\"nested\":{\"organization.id\":\"PRIVATE\"},\"attributes\":[{\"key\":\"app.version\",\"value\":42},{\"key\":\"terminal.type\",\"value\":{\"intValue\":7}}]}\n"
	writeSpool(t, spool, nativeA, "spans", payload+payload)
	meta, err := Build(spool, cache, &model.Capture{Boundaries: []model.Boundary{boundary("start", 0, 0), boundary("native-session", int64(len(payload)), 0), boundary("stop", int64(2*len(payload)), 0)}})
	require.NoError(t, err)
	require.Equal(t, []model.ByteRange{{0, int64(2 * len(payload))}}, meta.NativeSessions[0].SpansBytes)
	require.Equal(t, int64(2), meta.SpansLines)
	require.Equal(t, int64(2), meta.Scrubbed["user.email"])
	require.Equal(t, int64(2), meta.Scrubbed["organization.id"])
	require.Nil(t, meta.ClaudeCodeVersion)
	require.Nil(t, meta.TerminalType)
	require.NotContains(t, string(gzipContent(t, cache, SpansFile)), "PRIVATE")
}

func TestMissingUnselectedSpoolProducesEmptyAttachment(t *testing.T) {
	spool, cache := setup(t)
	require.NoError(t, os.RemoveAll(spool))
	meta, err := Build(spool, cache, &model.Capture{Boundaries: []model.Boundary{boundary("start", 0, 0), boundary("stop", 0, 0)}})
	require.NoError(t, err)
	require.Zero(t, meta.Spans)
	require.Empty(t, gzipContent(t, cache, SpansFile))
	require.Empty(t, gzipContent(t, cache, EventsFile))
}

func TestMissingSelectedEventSpoolPreservesPublishedSidecars(t *testing.T) {
	spool, cache := setup(t)
	spans, events := body("previous-span", "spans"), body("previous-event", "events")
	writeSpool(t, spool, nativeA, "spans", spans)
	writeSpool(t, spool, nativeA, "events", events)
	capture := &model.Capture{Boundaries: []model.Boundary{
		boundary("start", 0, 0), boundary("stop", int64(len(spans)), int64(len(events))),
	}}
	_, err := Build(spool, cache, capture)
	require.NoError(t, err)
	beforeSpans := gzipContent(t, cache, SpansFile)
	beforeEvents := gzipContent(t, cache, EventsFile)

	replacement := body("replacement-span", "spans")
	writeSpool(t, spool, nativeA, "spans", replacement)
	require.NoError(t, os.Remove(filepath.Join(spool, nativeA, "logs.jsonl")))
	capture.Boundaries[1] = boundary("stop", int64(len(replacement)), int64(len(events)))
	_, err = Build(spool, cache, capture)
	require.ErrorIs(t, err, ErrSelectedSpoolUnavailable)
	require.Equal(t, beforeSpans, gzipContent(t, cache, SpansFile))
	require.Equal(t, beforeEvents, gzipContent(t, cache, EventsFile))

	writeSpool(t, spool, nativeA, "events", events)
	_, err = Build(spool, cache, capture)
	require.NoError(t, err)
	require.Contains(t, string(gzipContent(t, cache, SpansFile)), "replacement-span")
}
