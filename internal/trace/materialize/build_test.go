package materialize

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/trace/model"
	"github.com/stretchr/testify/require"
)

const nativeA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
const nativeB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"

func body(name, signal string) string {
	resource, scope, records := "resourceSpans", "scopeSpans", "spans"
	if signal == "events" {
		resource, scope, records = "resourceLogs", "scopeLogs", "logRecords"
	}
	return `{"` + resource + `":[{"resource":{"attributes":[{"key":"user.email","value":{"stringValue":"PRIVATE_EMAIL"}},{"key":"app.version","value":{"stringValue":"2.1.278"}}]},"` + scope + `":[{"scope":{"attributes":[{"key":"user.id","value":{"stringValue":"PRIVATE_ID"}}]},"` + records + `":[{"name":"` + name + `","timeUnixNano":18446744073709551615,"attributes":[{"key":"session.id","value":{"stringValue":"` + nativeA + `"}},{"key":"app.entrypoint","value":{"stringValue":"cli"}},{"key":"terminal.type","value":{"stringValue":"tmux"}},{"key":"user.account_id","value":{"stringValue":"PRIVATE_ACCOUNT"}}],"events":[{"attributes":[{"key":"user.account_uuid","value":{"stringValue":"PRIVATE_ACCOUNT_UUID"}}]}],"links":[{"attributes":[{"key":"organization.id","value":{"stringValue":"PRIVATE_ORGANIZATION"}}]}]}]}]}]}` + "\n"
}

func setup(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	spool := filepath.Join(root, "spool")
	cache := filepath.Join(root, "ledger", ".sageox", "cache", "sessions", "recording")
	require.NoError(t, os.MkdirAll(filepath.Join(spool, nativeA), 0700))
	return spool, cache
}

func writeSpool(t *testing.T, spool, id, signal, data string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(spool, id), 0700))
	name := "traces.jsonl"
	if signal == "events" {
		name = "logs.jsonl"
	}
	require.NoError(t, os.WriteFile(filepath.Join(spool, id, name), []byte(data), 0600))
}

func boundary(action string, spans, events int64) model.Boundary {
	return model.Boundary{Action: action, At: time.Date(2026, 9, 22, 1, 0, 0, 0, time.UTC), Offsets: map[string]model.Offsets{nativeA: {Spans: spans, Events: events}}}
}

func gzipContent(t *testing.T, cache, name string) []byte {
	t.Helper()
	file, err := os.Open(filepath.Join(cache, name))
	require.NoError(t, err)
	defer file.Close()
	gz, err := gzip.NewReader(file)
	require.NoError(t, err)
	defer gz.Close()
	data, err := io.ReadAll(gz)
	require.NoError(t, err)
	return data
}

// Two recordings sharing a native session must not import each other's bytes,
// paused content, late exports, or identity attributes at any nesting level.
func TestRecordingsSelectDisjointRangesAndExcludePause(t *testing.T) {
	t.Parallel()
	spool, cache := setup(t)
	first, paused, second, third, late := body("first", "spans"), body("paused", "spans"), body("second", "spans"), body("third", "spans"), body("late", "spans")
	event := body("event", "events")
	writeSpool(t, spool, nativeA, "spans", first+paused+second+third+late)
	writeSpool(t, spool, nativeA, "events", event+event)
	a, b, c := int64(len(first)), int64(len(first+paused)), int64(len(first+paused+second))
	capture := &model.Capture{Boundaries: []model.Boundary{boundary("start", 0, 0), boundary("pause", a, int64(len(event))), boundary("resume", b, int64(len(event))), boundary("stop", c, int64(len(event)))}}
	meta, err := Build(spool, cache, capture)
	require.NoError(t, err)
	data := string(gzipContent(t, cache, SpansFile))
	require.Contains(t, data, `"name":"first"`)
	require.Contains(t, data, `"name":"second"`)
	for _, missing := range []string{`"name":"paused"`, `"name":"third"`, `"name":"late"`, "PRIVATE_", "user.email", "user.id", "user.account_id", "user.account_uuid", "organization.id"} {
		require.NotContains(t, data, missing)
	}
	require.Contains(t, data, "18446744073709551615")
	require.Equal(t, int64(2), meta.SpansLines)
	require.Equal(t, int64(2), meta.Spans)
	require.Equal(t, int64(1), meta.Events)
	require.Equal(t, int64(len(paused)), meta.PausedBytesSkipped)
	require.Equal(t, int64(len(third+late+event)), meta.LateBytes)
	for key := range identities {
		require.Equal(t, int64(3), meta.Scrubbed[key], key)
	}
	require.Equal(t, "2.1.278", *meta.ClaudeCodeVersion)
	require.Equal(t, "cli", *meta.Entrypoint)
	require.Equal(t, "tmux", *meta.TerminalType)
	require.Nil(t, meta.ReceiverVersion)
	require.Nil(t, meta.ReceiverUpBeforeFirstPrompt)
	require.Nil(t, meta.SettingsTag)
	require.Equal(t, []model.ByteRange{{0, a}, {b, c}}, meta.NativeSessions[0].SpansBytes)
	before, err := os.ReadFile(filepath.Join(cache, SpansFile))
	require.NoError(t, err)
	_, err = Build(spool, cache, capture)
	require.NoError(t, err)
	after, err := os.ReadFile(filepath.Join(cache, SpansFile))
	require.NoError(t, err)
	require.Equal(t, before, after, "retry output must be deterministic")
	capture2 := &model.Capture{Boundaries: []model.Boundary{boundary("start", c, int64(len(event))), boundary("stop", c+int64(len(third)), int64(len(event)))}}
	cache2 := filepath.Join(filepath.Dir(cache), "second-recording")
	secondMeta, err := Build(spool, cache2, capture2)
	require.NoError(t, err)
	data = string(gzipContent(t, cache2, SpansFile))
	require.Contains(t, data, `"name":"third"`)
	require.NotContains(t, data, `"name":"first"`)
	require.NotContains(t, data, `"name":"second"`)
	require.Equal(t, int64(1), secondMeta.Spans)
	original, err := os.ReadFile(filepath.Join(spool, nativeA, "traces.jsonl"))
	require.NoError(t, err)
	require.Equal(t, first+paused+second+third+late, string(original))
}

// Receiver bodies can contain formatting newlines. JSON decoding must retain a
// whole object, while an incomplete trailing object is omitted and counted.
func TestMultilineAndUnterminatedTail(t *testing.T) {
	t.Parallel()
	var pretty bytes.Buffer
	require.NoError(t, json.Indent(&pretty, []byte(strings.TrimSpace(body("pretty", "spans"))), "", "  "))
	complete := pretty.String() + "\n"
	for _, tail := range []string{`{"resourceSpans":[`, strings.TrimSpace(body("no newline", "spans")), "bad unterminated tail", "{\n\"incomplete\":\n"} {
		t.Run(tail[:min(15, len(tail))], func(t *testing.T) {
			spool, cache := setup(t)
			writeSpool(t, spool, nativeA, "spans", complete+tail)
			meta, err := Build(spool, cache, &model.Capture{Boundaries: []model.Boundary{boundary("start", 0, 0), boundary("stop", int64(len(complete+tail)), 0)}})
			require.NoError(t, err)
			require.Equal(t, int64(1), meta.SpansLines)
			require.Equal(t, int64(len(tail)), meta.TrailingBytesSkipped)
			output := gzipContent(t, cache, SpansFile)
			require.Equal(t, 1, bytes.Count(output, []byte{'\n'}))
			require.NotContains(t, string(output), "no newline")
		})
	}
}

func TestUnknownBoundariesNeverBecomeZero(t *testing.T) {
	t.Parallel()
	spool, cache := setup(t)
	payload := body("must-not-copy", "spans")
	writeSpool(t, spool, nativeA, "spans", payload)
	start := boundary("start", 0, 0)
	delete(start.Offsets, nativeA)
	meta, err := Build(spool, cache, &model.Capture{Boundaries: []model.Boundary{start, boundary("stop", int64(len(payload)), 0)}})
	require.NoError(t, err)
	require.Zero(t, meta.Spans)
	require.Empty(t, gzipContent(t, cache, SpansFile))
}

func TestInvalidCaptureFailsBeforePublishing(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		capture *model.Capture
	}{
		{"no stop", &model.Capture{Boundaries: []model.Boundary{boundary("start", 0, 0)}}},
		{"shrinking", &model.Capture{Boundaries: []model.Boundary{boundary("start", 2, 0), boundary("stop", 1, 0)}}},
		{"negative", &model.Capture{Boundaries: []model.Boundary{boundary("start", -1, 0), boundary("stop", 1, 0)}}},
		{"unknown action", &model.Capture{Boundaries: []model.Boundary{boundary("start", 0, 0), boundary("weird", 0, 0), boundary("stop", 1, 0)}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spool, cache := setup(t)
			_, err := Build(spool, cache, tt.capture)
			require.Error(t, err)
			_, err = os.Stat(filepath.Join(cache, SpansFile))
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestFailureInSecondStreamPublishesNeitherSidecar(t *testing.T) {
	t.Parallel()
	spool, cache := setup(t)
	valid := body("valid", "spans")
	invalid := "{bad json}\n"
	writeSpool(t, spool, nativeA, "spans", valid)
	writeSpool(t, spool, nativeA, "events", invalid)
	_, err := Build(spool, cache, &model.Capture{Boundaries: []model.Boundary{boundary("start", 0, 0), boundary("stop", int64(len(valid)), int64(len(invalid)))}})
	require.Error(t, err)
	for _, name := range []string{SpansFile, EventsFile} {
		_, err := os.Stat(filepath.Join(cache, name))
		require.ErrorIs(t, err, os.ErrNotExist)
	}
	entries, err := os.ReadDir(cache)
	require.NoError(t, err)
	for _, entry := range entries {
		require.False(t, strings.HasSuffix(entry.Name(), ".tmp"))
	}
}

func TestCacheOnlyAndSymlinkSafety(t *testing.T) {
	t.Parallel()
	for _, shape := range []string{"tracked", "cache symlink", "spool symlink", "session symlink", "file symlink", "output symlink", "missing selected", "shrunk selected"} {
		t.Run(shape, func(t *testing.T) {
			spool, cache := setup(t)
			payload := body("span", "spans")
			writeSpool(t, spool, nativeA, "spans", payload)
			outside := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(outside, "keep"), []byte("keep"), 0600))
			link := func(target, path string) {
				t.Helper()
				if err := os.Symlink(target, path); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			}
			switch shape {
			case "tracked":
				cache = filepath.Join(t.TempDir(), "sessions", "name")
			case "cache symlink":
				require.NoError(t, os.MkdirAll(filepath.Dir(cache), 0700))
				link(outside, cache)
			case "spool symlink":
				other := spool + "-link"
				link(spool, other)
				spool = other
			case "session symlink":
				require.NoError(t, os.RemoveAll(filepath.Join(spool, nativeA)))
				link(outside, filepath.Join(spool, nativeA))
			case "file symlink":
				require.NoError(t, os.Remove(filepath.Join(spool, nativeA, "traces.jsonl")))
				link(filepath.Join(outside, "keep"), filepath.Join(spool, nativeA, "traces.jsonl"))
			case "output symlink":
				require.NoError(t, os.MkdirAll(cache, 0700))
				link(filepath.Join(outside, "keep"), filepath.Join(cache, SpansFile))
			case "missing selected":
				require.NoError(t, os.RemoveAll(spool))
			case "shrunk selected":
				writeSpool(t, spool, nativeA, "spans", "")
			}
			_, err := Build(spool, cache, &model.Capture{Boundaries: []model.Boundary{boundary("start", 0, 0), boundary("stop", int64(len(payload)), 0)}})
			require.Error(t, err)
			b, err := os.ReadFile(filepath.Join(outside, "keep"))
			require.NoError(t, err)
			require.Equal(t, "keep", string(b))
		})
	}
}

func TestNotOptedInDoesNothing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	meta, err := Build("", filepath.Join(root, "invalid"), nil)
	require.NoError(t, err)
	require.Nil(t, meta)
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Empty(t, entries)
}

// If the second publication fails, keep neither a mixed pair nor a single new
// file. Previously successful files must survive the failed retry unchanged.
func TestPublicationFailureRestoresPreviousPair(t *testing.T) {
	t.Parallel()
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "first build", true: "retry"}[existing], func(t *testing.T) {
			dir := t.TempDir()
			root, err := os.OpenRoot(dir)
			require.NoError(t, err)
			defer root.Close()
			if existing {
				require.NoError(t, root.WriteFile(SpansFile, []byte("old spans"), 0600))
				require.NoError(t, root.WriteFile(EventsFile, []byte("old events"), 0600))
			}
			require.NoError(t, root.WriteFile("span.tmp", []byte("new spans"), 0600))
			require.Error(t, publish(root, map[string]string{SpansFile: "span.tmp", EventsFile: "missing.tmp"}))
			for _, name := range []string{SpansFile, EventsFile} {
				b, err := root.ReadFile(name)
				if existing {
					require.NoError(t, err)
					require.Contains(t, string(b), "old ")
				} else {
					require.ErrorIs(t, err, os.ErrNotExist)
				}
			}
		})
	}
}

// A newly observed native session must start at its own first snapshot, not at
// zero; bytes from earlier use of that native session belong to other recordings.
func TestNativeSessionBoundaryExcludesEarlierBytes(t *testing.T) {
	t.Parallel()
	spool, cache := setup(t)
	a := body("first-native", "spans")
	prior := body("other-recording", "spans")
	b := body("new-native", "spans")
	writeSpool(t, spool, nativeA, "spans", a)
	writeSpool(t, spool, nativeB, "spans", prior+b)
	native := boundary("native-session", int64(len(a)), 0)
	native.Offsets[nativeB] = model.Offsets{Spans: int64(len(prior))}
	stop := boundary("stop", int64(len(a)), 0)
	stop.Offsets[nativeB] = model.Offsets{Spans: int64(len(prior + b))}
	meta, err := Build(spool, cache, &model.Capture{Boundaries: []model.Boundary{boundary("start", 0, 0), native, stop}})
	require.NoError(t, err)
	require.Len(t, meta.NativeSessions, 2)
	require.Equal(t, int64(2), meta.Spans)
	output := string(gzipContent(t, cache, SpansFile))
	require.Contains(t, output, "first-native")
	require.Contains(t, output, "new-native")
	require.NotContains(t, output, "other-recording")
	require.Equal(t, []model.ByteRange{{int64(len(prior)), int64(len(prior + b))}}, meta.NativeSessions[1].SpansBytes)
}

func TestUnknownBoundaryCannotHideSpoolTruncation(t *testing.T) {
	t.Parallel()
	spool, cache := setup(t)
	unknown := boundary("native-session", 0, 0)
	unknown.Offsets = nil
	_, err := Build(spool, cache, &model.Capture{Boundaries: []model.Boundary{boundary("start", 30, 0), unknown, boundary("stop", 20, 0)}})
	require.ErrorContains(t, err, "shrank")
}

func TestConflictingMetadataObservationsRemainUnknown(t *testing.T) {
	t.Parallel()
	spool, cache := setup(t)
	first := body("first", "spans")
	second := strings.ReplaceAll(body("second", "spans"), "2.1.278", "2.1.279")
	writeSpool(t, spool, nativeA, "spans", first+second)
	meta, err := Build(spool, cache, &model.Capture{Boundaries: []model.Boundary{boundary("start", 0, 0), boundary("stop", int64(len(first+second)), 0)}})
	require.NoError(t, err)
	require.Nil(t, meta.ClaudeCodeVersion)
	require.NotNil(t, meta.Entrypoint)
}
