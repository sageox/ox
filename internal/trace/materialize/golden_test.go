package materialize

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/trace/model"
	"github.com/sageox/ox/internal/trace/receiver"
	"github.com/stretchr/testify/require"
)

// The sanitized prototype retains real batching, interleaving, timestamps, and
// number shapes: all 443 decoded spans must survive receiver demux + sidecars.
func TestPrototypeGoldenCapturePreserves443Spans(t *testing.T) {
	t.Parallel()
	spool, cache := setup(t)
	file, err := os.Open("testdata/prototype-443.jsonl.gz")
	require.NoError(t, err)
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	require.NoError(t, err)
	defer compressed.Close()
	rec := receiver.New(receiver.Config{SpoolDir: spool, Version: "fixture", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	scanner := bufio.NewScanner(compressed)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	var envelopes int
	for scanner.Scan() {
		req := httptest.NewRequest("POST", "/v1/traces", bytes.NewReader(scanner.Bytes()))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		rec.Handler().ServeHTTP(response, req)
		require.Equal(t, 200, response.Code)
		envelopes++
	}
	require.NoError(t, scanner.Err())
	require.Equal(t, 177, envelopes)
	start := boundary("start", 0, 0)
	start.Offsets[nativeB] = model.Offsets{}
	stop := boundary("stop", 0, 0)
	expected := map[string][]any{}
	var rawCount int64
	for _, id := range []string{nativeA, nativeB} {
		data, err := os.ReadFile(filepath.Join(spool, id, "traces.jsonl"))
		require.NoError(t, err)
		stop.Offsets[id] = model.Offsets{Spans: int64(len(data))}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		for {
			var value map[string]any
			err := dec.Decode(&value)
			if err == io.EOF {
				break
			}
			require.NoError(t, err)
			rawCount += countRecords(value, "spans")
			expected[id] = append(expected[id], goldenRemoveIdentities(value))
		}
	}
	require.Equal(t, int64(443), rawCount)
	meta, err := Build(spool, cache, &model.Capture{Boundaries: []model.Boundary{start, stop}})
	require.NoError(t, err)
	require.Equal(t, int64(443), meta.Spans)
	require.Equal(t, int64(0), meta.Events)
	for key := range identities {
		require.Equal(t, int64(443), meta.Scrubbed[key], key)
	}
	output := gzipContent(t, cache, SpansFile)
	var actual []any
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.UseNumber()
	for {
		var value any
		err := decoder.Decode(&value)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		actual = append(actual, value)
	}
	want := append(expected[nativeA], expected[nativeB]...)
	require.Equal(t, want, actual, "all retained OTLP data must match exactly after identity removal")
	// The generic independent matcher above must not normalize known lost fields.
	require.NotEmpty(t, actual)
}

// Deliberately independent from the production scrubber: removes only known
// OTLP KeyValue entries, then compares every remaining decoded field verbatim.
func goldenRemoveIdentities(value any) any {
	switch v := value.(type) {
	case []any:
		out := make([]any, 0, len(v))
		for _, child := range v {
			if obj, ok := child.(map[string]any); ok {
				key, _ := obj["key"].(string)
				switch key {
				case "user.email", "user.id", "user.account_id", "user.account_uuid", "organization.id":
					continue
				}
			}
			out = append(out, goldenRemoveIdentities(child))
		}
		return out
	case map[string]any:
		for key, child := range v {
			v[key] = goldenRemoveIdentities(child)
		}
		return v
	default:
		return v
	}
}
