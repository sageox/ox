package telemetry

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTruncateString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"short_string", "hello", 10, "hello"},
		{"exact_length", "hello", 5, "hello"},
		{"needs_truncation", "hello world foo bar", 10, "hello w..."},
		{"very_short_max", "hello", 3, "..."},
		{"empty_string", "", 10, ""},
		{"truncate_boundary", "abcdef", 6, "abcdef"},
		{"truncate_just_over", "abcdefg", 6, "abc..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := truncateString(tt.input, tt.maxLen)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestWithHTTPClient(t *testing.T) {
	t.Parallel()
	customClient := &http.Client{Timeout: 5 * time.Second}
	client := NewClient("test", WithEnabled(true), WithHTTPClient(customClient))
	assert.Equal(t, customClient, client.httpClient)
}

func TestWithProjectRoot(t *testing.T) {
	tmpDir := t.TempDir()

	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	require.NoError(t, os.WriteFile(
		filepath.Join(sageoxDir, "config.json"),
		[]byte(`{"repo_id": "repo_test123"}`),
		0644,
	))

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	client := NewClient("test", WithEnabled(true), WithProjectRoot(tmpDir))

	assert.NotNil(t, client.fileQueue, "file queue should be initialized with project root")
	assert.Equal(t, "repo_test123", client.repoID, "repo_id should be loaded from config")
}

func TestTrack_AddsContextFields(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	client := NewClient("test", WithEnabled(true))
	client.repoID = "repo_abc"
	client.apiEndpoint = "https://sageox.ai"

	event := Event{
		Type:    EventCommandComplete,
		Command: "status",
	}

	client.Track(event)

	client.mu.Lock()
	defer client.mu.Unlock()

	require.Len(t, client.queue, 1)
	assert.Equal(t, "repo_abc", client.queue[0].RepoID, "repo_id should be injected")
	assert.Equal(t, "https://sageox.ai", client.queue[0].APIEndpoint, "api_endpoint should be injected")
	assert.False(t, client.queue[0].Timestamp.IsZero(), "timestamp should be set")
}

func TestTrack_PreservesExistingContextFields(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	client := NewClient("test", WithEnabled(true))
	client.repoID = "default_repo"
	client.apiEndpoint = "https://default.sageox.ai"

	event := Event{
		Type:        EventCommandComplete,
		RepoID:      "explicit_repo",
		APIEndpoint: "https://explicit.sageox.ai",
	}

	client.Track(event)

	client.mu.Lock()
	defer client.mu.Unlock()

	require.Len(t, client.queue, 1)
	assert.Equal(t, "explicit_repo", client.queue[0].RepoID, "should preserve explicitly set repo_id")
	assert.Equal(t, "https://explicit.sageox.ai", client.queue[0].APIEndpoint, "should preserve explicitly set endpoint")
}

func TestTrack_WithFileQueue(t *testing.T) {
	tmpDir := t.TempDir()

	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "README.md"), []byte("# SageOx"), 0644))

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	client := NewClient("test", WithEnabled(true), WithProjectRoot(tmpDir))

	event := Event{
		Type:    EventCommandComplete,
		Command: "test",
	}
	client.Track(event)

	client.mu.Lock()
	queueLen := len(client.queue)
	client.mu.Unlock()
	assert.Equal(t, 0, queueLen, "event should go to file queue, not in-memory queue")

	assert.Equal(t, 1, client.fileQueue.Count())
}

func TestSendEventsWithResult_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	client := NewClient("test",
		WithEnabled(true),
		WithBaseURL(server.URL),
	)

	events := []Event{{Type: EventCommandComplete, Command: "test"}}
	ctx := t.Context()
	assert.True(t, client.sendEventsWithResult(ctx, events))
}

func TestSendEventsWithResult_EmptyEvents(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	client := NewClient("test", WithEnabled(true))
	assert.True(t, client.sendEventsWithResult(t.Context(), []Event{}))
}

func TestSendEventsWithResult_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	client := NewClient("test",
		WithEnabled(true),
		WithBaseURL(server.URL),
	)

	events := []Event{{Type: EventCommandComplete}}
	assert.False(t, client.sendEventsWithResult(t.Context(), events))
}

func TestSendEventsWithResult_Unreachable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	client := NewClient("test",
		WithEnabled(true),
		WithBaseURL("http://localhost:1"),
		WithHTTPClient(&http.Client{Timeout: 100 * time.Millisecond}),
	)

	events := []Event{{Type: EventCommandComplete}}
	assert.False(t, client.sendEventsWithResult(t.Context(), events))
}

func TestIsSageoxInstalled(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(t *testing.T) string
		want  bool
	}{
		{
			name: "with_readme",
			setup: func(t *testing.T) string {
				dir := filepath.Join(t.TempDir(), ".sageox")
				require.NoError(t, os.MkdirAll(dir, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test"), 0644))
				return dir
			},
			want: true,
		},
		{
			name: "with_repo_marker",
			setup: func(t *testing.T) string {
				dir := filepath.Join(t.TempDir(), ".sageox")
				require.NoError(t, os.MkdirAll(dir, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(dir, ".repo_sageox_ai"), []byte(""), 0644))
				return dir
			},
			want: true,
		},
		{
			name: "empty_dir",
			setup: func(t *testing.T) string {
				dir := filepath.Join(t.TempDir(), ".sageox")
				require.NoError(t, os.MkdirAll(dir, 0755))
				return dir
			},
			want: false,
		},
		{
			name: "nonexistent_dir",
			setup: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "nonexistent")
			},
			want: false,
		},
		{
			name: "dir_with_other_files",
			setup: func(t *testing.T) string {
				dir := filepath.Join(t.TempDir(), ".sageox")
				require.NoError(t, os.MkdirAll(dir, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0644))
				return dir
			},
			want: false,
		},
		{
			name: "repo_marker_is_directory",
			setup: func(t *testing.T) string {
				dir := filepath.Join(t.TempDir(), ".sageox")
				require.NoError(t, os.MkdirAll(filepath.Join(dir, ".repo_something"), 0755))
				return dir
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := tt.setup(t)
			assert.Equal(t, tt.want, isSageoxInstalled(dir))
		})
	}
}

func TestFileQueue_AppendOverflowTruncates(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	setupTestProject(t, tmpDir)
	q := NewFileQueue(tmpDir)

	qPath := q.Path()
	dir := filepath.Dir(qPath)
	require.NoError(t, os.MkdirAll(dir, 0755))

	bigData := strings.Repeat(`{"type":"test","success":true}`+"\n", 50000)
	require.NoError(t, os.WriteFile(qPath, []byte(bigData), 0644))

	info, err := os.Stat(qPath)
	require.NoError(t, err)
	assert.Greater(t, info.Size(), int64(maxFileSize), "test setup: file should be over max")

	err = q.Append(Event{Type: EventCommandComplete, Success: true})
	require.NoError(t, err)

	info, err = os.Stat(qPath)
	require.NoError(t, err)
	assert.Less(t, info.Size(), int64(maxFileSize), "file should be under max after truncation")
}

func TestFileQueue_ReadAll_SkipsMalformed(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	setupTestProject(t, tmpDir)
	q := NewFileQueue(tmpDir)

	qPath := q.Path()
	require.NoError(t, os.MkdirAll(filepath.Dir(qPath), 0755))

	content := `{"type":"valid1","success":true}
not valid json at all
{"type":"valid2","success":false}
{malformed
{"type":"valid3","success":true}
`
	require.NoError(t, os.WriteFile(qPath, []byte(content), 0644))

	events, err := q.ReadAll()
	require.NoError(t, err)
	assert.Len(t, events, 3, "should skip malformed lines and return valid ones")
	assert.Equal(t, "valid1", events[0].Type)
	assert.Equal(t, "valid2", events[1].Type)
	assert.Equal(t, "valid3", events[2].Type)
}

func TestFileQueue_ShouldFlush_RecentEvents(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	setupTestProject(t, tmpDir)
	q := NewFileQueue(tmpDir)

	for i := 0; i < 3; i++ {
		_ = q.Append(Event{Type: EventCommandComplete, Success: true})
	}

	assert.False(t, q.ShouldFlush(), "should not flush when below count threshold and events are recent")
}

func TestSendToDaemon_SetsDefaults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	client := NewClient("test", WithEnabled(true))

	// fire-and-forget: should not panic even without daemon running
	client.SendToDaemon("test:event", nil)
	time.Sleep(10 * time.Millisecond)

	client.SendToDaemon("test:event", map[string]any{"key": "value"})
	time.Sleep(10 * time.Millisecond)
}

func TestClient_StopWhenDisabled(t *testing.T) {
	t.Parallel()
	client := NewClient("test", WithEnabled(false))
	client.Start()
	client.Stop()
}

func TestClient_FlushEmptyQueue(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	client := NewClient("test", WithEnabled(true))

	client.Flush()
	time.Sleep(50 * time.Millisecond)
}
