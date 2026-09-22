package receiver

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func payload(signal, id string) []byte {
	resources, scopes, records := signalKeys(signal)
	return []byte(`{ "` + resources + `": [{"resource":{"attributes":[]},"` + scopes + `":[{"scope":{"name":"claude"},"` + records + `":[` + span(id) + `]}]}] }`)
}

func testReceiver(t *testing.T) (*Receiver, string) {
	t.Helper()
	spool := filepath.Join(t.TempDir(), "spool")
	return New(Config{SpoolDir: spool, Version: "test", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}), spool
}

func post(r *Receiver, signal, contentType, encoding string, b []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/"+signal, bytes.NewReader(b))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Content-Encoding", encoding)
	response := httptest.NewRecorder()
	r.Handler().ServeHTTP(response, req)
	return response
}

// Formatting and numeric precision must survive the common single-session path.
func TestCapturePreservesBytesAndMetadata(t *testing.T) {
	t.Parallel()
	for _, signal := range []string{"traces", "logs"} {
		t.Run(signal, func(t *testing.T) {
			r, spool := testReceiver(t)
			body := payload(signal, sessionA)
			require.Equal(t, http.StatusOK, post(r, signal, "application/json; charset=utf-8", "", body).Code)
			require.Equal(t, http.StatusOK, post(r, signal, "application/json", "", body).Code)
			content, err := os.ReadFile(filepath.Join(spool, sessionA, signal+".jsonl"))
			require.NoError(t, err)
			require.Equal(t, append(append(append([]byte{}, body...), '\n'), append(body, '\n')...), content)
			var index SessionIndex
			b, err := os.ReadFile(filepath.Join(spool, sessionA, "index.json"))
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(b, &index))
			require.Equal(t, sessionA, index.SessionID)
			require.Equal(t, int64(2), index.Requests)
			require.Equal(t, int64(len(content)), index.Bytes)
			require.False(t, index.LastSeen.Before(index.FirstSeen))
			receipts, err := os.ReadFile(filepath.Join(spool, sessionA, "receipts.jsonl"))
			require.NoError(t, err)
			lines := bytes.Split(bytes.TrimSpace(receipts), []byte{'\n'})
			require.Len(t, lines, 2)
			var a, bReceipt receipt
			require.NoError(t, json.Unmarshal(lines[0], &a))
			require.NoError(t, json.Unmarshal(lines[1], &bReceipt))
			require.NotEqual(t, a.RequestID, bReceipt.RequestID)
			require.Equal(t, len(body), a.Bytes)
			for _, name := range []string{"", sessionA} {
				info, err := os.Stat(filepath.Join(spool, name))
				require.NoError(t, err)
				require.Equal(t, os.FileMode(0700), info.Mode().Perm())
			}
			for _, name := range []string{signal + ".jsonl", "index.json", "receipts.jsonl"} {
				info, err := os.Stat(filepath.Join(spool, sessionA, name))
				require.NoError(t, err)
				require.Equal(t, os.FileMode(0600), info.Mode().Perm())
			}
			stats, err := Stats(spool)
			require.NoError(t, err)
			require.Equal(t, 1, stats.Sessions)
			require.Greater(t, stats.Bytes, index.Bytes)
		})
	}
}

// Both compressed and uncompressed input limits must reject before creating files.
func TestRejectedPayloadsDoNotCreateCapture(t *testing.T) {
	t.Parallel()
	compressed := func(b []byte) []byte {
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		_, err := w.Write(b)
		require.NoError(t, err)
		require.NoError(t, w.Close())
		return buf.Bytes()
	}
	for _, tt := range []struct {
		name, contentType, encoding string
		body                        []byte
		status                      int
	}{
		{"protobuf", "application/x-protobuf", "", []byte{1, 2}, 415},
		{"missing content type", "", "", []byte(`{}`), 415},
		{"unsupported encoding", "application/json", "br", []byte(`{}`), 415},
		{"invalid json", "application/json", "", []byte(`{"`), 400},
		{"null json", "application/json", "", []byte(`null`), 400},
		{"invalid gzip", "application/json", "gzip", []byte(`no`), 400},
		{"truncated gzip", "application/json", "gzip", compressed(payload("traces", sessionA))[:20], 400},
		{"wire limit", "application/json", "", bytes.Repeat([]byte{' '}, MaxBodyBytes+1), 413},
		{"decompressed limit", "application/json", "gzip", compressed(bytes.Repeat([]byte{' '}, MaxBodyBytes+1)), 413},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, spool := testReceiver(t)
			require.Equal(t, tt.status, post(r, "traces", tt.contentType, tt.encoding, tt.body).Code)
			_, err := os.Stat(spool)
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestGzipDecodesToOriginalBytes(t *testing.T) {
	t.Parallel()
	r, spool := testReceiver(t)
	body := payload("traces", sessionA)
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, err := writer.Write(body)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.Equal(t, 200, post(r, "traces", "application/json", "gzip", compressed.Bytes()).Code)
	captured, err := os.ReadFile(filepath.Join(spool, sessionA, "traces.jsonl"))
	require.NoError(t, err)
	require.Equal(t, append(body, '\n'), captured)
}

// Concurrent exporters must retain every complete record, without mixing sessions.
func TestConcurrentSessionsNeverInterleave(t *testing.T) {
	t.Parallel()
	r, spool := testReceiver(t)
	var wg sync.WaitGroup
	codes := make(chan int, 40)
	for i := 0; i < 40; i++ {
		wg.Go(func() {
			id := sessionA
			if i%2 == 1 {
				id = sessionB
			}
			codes <- post(r, "traces", "application/json", "", payload("traces", id)).Code
		})
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		require.Equal(t, 200, code)
	}
	for _, id := range []string{sessionA, sessionB} {
		data, err := os.ReadFile(filepath.Join(spool, id, "traces.jsonl"))
		require.NoError(t, err)
		lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
		require.Len(t, lines, 20)
		for _, line := range lines {
			require.JSONEq(t, string(payload("traces", id)), string(line))
		}
	}
	stats, err := Stats(spool)
	require.NoError(t, err)
	require.Equal(t, 2, stats.Sessions)
}

// Filesystem failures must be observable without exposing or overwriting data.
func TestStorageFailuresAndSymlinks(t *testing.T) {
	t.Parallel()
	for _, destination := range []string{"spool", "session", "traces.jsonl", "receipts.jsonl", "index.json", "index.json.tmp", ".lock"} {
		t.Run(destination, func(t *testing.T) {
			r, spool := testReceiver(t)
			outside := t.TempDir()
			sentinel := filepath.Join(outside, "sentinel")
			require.NoError(t, os.WriteFile(sentinel, []byte("keep"), 0600))
			var target string
			switch destination {
			case "spool":
				target = spool
			case "session":
				require.NoError(t, os.MkdirAll(spool, 0700))
				target = filepath.Join(spool, sessionA)
			case ".lock":
				require.NoError(t, os.MkdirAll(spool, 0700))
				target = filepath.Join(spool, destination)
			default:
				require.NoError(t, os.MkdirAll(filepath.Join(spool, sessionA), 0700))
				target = filepath.Join(spool, sessionA, destination)
			}
			if err := os.Symlink(outside, target); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			require.Equal(t, 500, post(r, "traces", "application/json", "", payload("traces", sessionA)).Code)
			b, err := os.ReadFile(sentinel)
			require.NoError(t, err)
			require.Equal(t, "keep", string(b))
			entries, err := os.ReadDir(outside)
			require.NoError(t, err)
			require.Len(t, entries, 1)
		})
	}
	t.Run("corrupt index keeps payload", func(t *testing.T) {
		r, spool := testReceiver(t)
		require.Equal(t, 200, post(r, "traces", "application/json", "", payload("traces", sessionA)).Code)
		traceFile := filepath.Join(spool, sessionA, "traces.jsonl")
		before, err := os.ReadFile(traceFile)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(spool, sessionA, "index.json"), []byte("corrupt"), 0600))
		require.Equal(t, 500, post(r, "traces", "application/json", "", payload("traces", sessionA)).Code)
		after, err := os.ReadFile(traceFile)
		require.NoError(t, err)
		require.Equal(t, before, after)
		_, err = Stats(spool)
		require.Error(t, err)
	})
}

// Health checks must not prolong an unused receiver, and prune failures are nonfatal.
func TestServeIdleAndCancellation(t *testing.T) {
	t.Parallel()
	for _, cancelEarly := range []bool{false, true} {
		t.Run(map[bool]string{false: "idle", true: "cancel"}[cancelEarly], func(t *testing.T) {
			r, _ := testReceiver(t)
			r.config.IdleTimeout = 30 * time.Millisecond
			pruned := make(chan struct{}, 1)
			r.config.Prune = func() error { pruned <- struct{}{}; return errors.New("test prune failure") }
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- r.Serve(ctx, listener) }()
			<-pruned
			if cancelEarly {
				cancel()
			}
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(time.Second):
				t.Fatal("receiver did not stop")
			}
			_, err = net.DialTimeout("tcp", listener.Addr().String(), 20*time.Millisecond)
			require.Error(t, err)
		})
	}
}

func TestHealthShutdownAndHTTPMethods(t *testing.T) {
	t.Parallel()
	r, _ := testReceiver(t)
	r.config.InstanceID = "public-id"
	r.config.ShutdownToken = "private-token"
	stopped := false
	r.config.Shutdown = func() { stopped = true }
	response := httptest.NewRecorder()
	r.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/healthz", nil))
	require.Equal(t, 200, response.Code)
	require.Contains(t, response.Body.String(), `"service":"ox-trace"`)
	require.Contains(t, response.Body.String(), `"instance_id":"public-id"`)
	require.NotContains(t, response.Body.String(), "private-token")
	for _, tt := range []struct {
		method, path, token, origin string
		status                      int
	}{
		{"GET", "/v1/traces", "", "", 405},
		{"POST", "/shutdown", "wrong", "", 401},
		{"POST", "/shutdown", "private-token", "https://evil.example", 401},
		{"POST", "/v1/traces", "", "https://evil.example", 403},
		{"POST", "/shutdown", "private-token", "", 200},
	} {
		req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+tt.token)
		req.Header.Set("Origin", tt.origin)
		response := httptest.NewRecorder()
		r.Handler().ServeHTTP(response, req)
		require.Equal(t, tt.status, response.Code)
	}
	require.True(t, stopped)
	r.config.Shutdown = nil
	req := httptest.NewRequest("POST", "/shutdown", nil)
	req.Header.Set("Authorization", "Bearer private-token")
	response = httptest.NewRecorder()
	r.Handler().ServeHTTP(response, req)
	require.Equal(t, 503, response.Code)
}

// Scanning pending recording metadata must not delay SessionStart readiness or
// prevent disabling the receiver while a retention scan is still running.
func TestSlowRetentionDoesNotBlockReadinessOrShutdown(t *testing.T) {
	t.Parallel()
	r, _ := testReceiver(t)
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	defer func() { close(release); <-finished }()
	r.config.Prune = func() error { close(started); <-release; close(finished); return nil }
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Serve(ctx, listener) }()
	<-started
	client := http.Client{Timeout: time.Second}
	response, err := client.Get("http://" + listener.Addr().String() + "/healthz")
	require.NoError(t, err)
	require.Equal(t, 200, response.StatusCode)
	require.NoError(t, response.Body.Close())
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("shutdown waited on retention")
	}
}
