package telemetry

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestQueueCache(t *testing.T) {
	t.Helper()
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("XDG_CACHE_HOME", queueTestDir(t))
}

// setupTestProject creates a valid SageOx project structure for testing
func setupTestProject(t *testing.T, projectRoot string) {
	t.Helper()
	setupTestQueueCache(t)
	sageoxDir := filepath.Join(projectRoot, ".sageox")
	err := os.MkdirAll(sageoxDir, 0755)
	require.NoError(t, err, "failed to create .sageox dir")
	// create README.md marker file
	readmePath := filepath.Join(sageoxDir, "README.md")
	err = os.WriteFile(readmePath, []byte("# SageOx"), 0644)
	require.NoError(t, err, "failed to create README.md")
}

func TestFileQueue_Append(t *testing.T) {
	tmpDir := t.TempDir()
	setupTestProject(t, tmpDir)
	q := NewFileQueue(tmpDir)

	event := Event{
		Type:    EventGuidanceFetch,
		Path:    "infra/aws/s3",
		Success: true,
	}

	err := q.Append(event)
	require.NoError(t, err, "Append failed")

	// verify file was created
	_, err = os.Stat(q.Path())
	assert.False(t, os.IsNotExist(err), "queue file was not created")
}

func TestFileQueue_ReadAll(t *testing.T) {
	tmpDir := t.TempDir()
	setupTestProject(t, tmpDir)
	q := NewFileQueue(tmpDir)

	// append multiple events
	events := []Event{
		{Type: EventSessionStart, AgentID: "abc123", Success: true},
		{Type: EventGuidanceFetch, Path: "infra/aws", Success: true},
		{Type: EventAttributionShown, Path: "security/iam", Success: true},
	}

	for _, e := range events {
		err := q.Append(e)
		require.NoError(t, err, "Append failed")
	}

	// read all events
	read, err := q.ReadAll()
	require.NoError(t, err, "ReadAll failed")

	assert.Len(t, read, len(events))

	// verify event types match
	for i, e := range read {
		assert.Equal(t, events[i].Type, e.Type, "event %d type mismatch", i)
	}
}

func TestFileQueue_Truncate(t *testing.T) {
	tmpDir := t.TempDir()
	setupTestProject(t, tmpDir)
	q := NewFileQueue(tmpDir)

	// append an event
	_ = q.Append(Event{Type: EventGuidanceFetch, Success: true})

	// verify event is there
	events, _ := q.ReadAll()
	require.Len(t, events, 1, "expected 1 event before truncate")

	// truncate
	err := q.Truncate()
	require.NoError(t, err, "Truncate failed")

	// verify empty
	events, _ = q.ReadAll()
	assert.Empty(t, events, "expected 0 events after truncate")
}

func TestFileQueue_ShouldFlush_CountThreshold(t *testing.T) {
	tmpDir := t.TempDir()
	setupTestProject(t, tmpDir)
	q := NewFileQueue(tmpDir)

	// add events up to threshold
	for i := 0; i < flushThresholdCount-1; i++ {
		_ = q.Append(Event{Type: EventGuidanceFetch, Success: true})
	}

	assert.False(t, q.ShouldFlush(), "ShouldFlush returned true before threshold")

	// add one more to hit threshold
	_ = q.Append(Event{Type: EventGuidanceFetch, Success: true})

	assert.True(t, q.ShouldFlush(), "ShouldFlush returned false at threshold")
}

func TestFileQueue_ShouldFlush_AgeThreshold(t *testing.T) {
	tmpDir := t.TempDir()
	setupTestProject(t, tmpDir)
	q := NewFileQueue(tmpDir)

	// add an old event
	oldEvent := Event{
		Type:      EventGuidanceFetch,
		Timestamp: time.Now().Add(-2 * time.Hour), // 2 hours old
		Success:   true,
	}
	_ = q.Append(oldEvent)

	assert.True(t, q.ShouldFlush(), "ShouldFlush returned false for old event")
}

func TestFileQueue_ShouldFlush_Empty(t *testing.T) {
	setupTestQueueCache(t)
	tmpDir := t.TempDir()
	q := NewFileQueue(tmpDir)

	assert.False(t, q.ShouldFlush(), "ShouldFlush returned true for empty queue")
}

func TestFileQueue_NilSafe(t *testing.T) {
	var q *FileQueue

	// all operations should be safe on nil
	err := q.Append(Event{})
	assert.NoError(t, err, "Append on nil returned error")

	events, err := q.ReadAll()
	assert.NoError(t, err, "ReadAll on nil returned error")
	assert.Nil(t, events, "ReadAll on nil should return nil events")

	err = q.Truncate()
	assert.NoError(t, err, "Truncate on nil returned error")

	assert.False(t, q.ShouldFlush(), "ShouldFlush on nil returned true")
	assert.Equal(t, 0, q.Count(), "Count on nil should be 0")
	assert.Empty(t, q.Path(), "Path on nil should be empty")
}

func TestFileQueue_Count(t *testing.T) {
	tmpDir := t.TempDir()
	setupTestProject(t, tmpDir)
	q := NewFileQueue(tmpDir)

	assert.Equal(t, 0, q.Count(), "Count on empty queue should be 0")

	_ = q.Append(Event{Type: EventGuidanceFetch})
	_ = q.Append(Event{Type: EventSessionStart})

	assert.Equal(t, 2, q.Count(), "Count after 2 appends should be 2")
}

func TestFileQueue_CreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	deepPath := filepath.Join(tmpDir, "deep", "nested", "project")
	setupTestProject(t, deepPath) // this creates .sageox/README.md
	q := NewFileQueue(deepPath)

	err := q.Append(Event{Type: EventGuidanceFetch, Success: true})
	require.NoError(t, err, "Append failed")

	// verify cache directory structure was created
	expectedDir := paths.TelemetryCacheDir()
	assert.Equal(t, expectedDir, filepath.Dir(q.Path()))
	info, err := os.Stat(expectedDir)
	require.NoError(t, err)
	assert.True(t, queuePrivateMode(info))
	info, err = os.Stat(q.Path())
	require.NoError(t, err)
	assert.True(t, queuePrivateMode(info))
	_, err = os.Stat(filepath.Join(deepPath, ".sageox", "cache"))
	assert.ErrorIs(t, err, os.ErrNotExist, "must not create a repository-local queue")
}

func TestNewFileQueue_EmptyProjectRoot(t *testing.T) {
	q := NewFileQueue("")
	assert.Nil(t, q, "NewFileQueue with empty projectRoot should return nil")
}

func TestNewFileQueue_CanonicalProjectKey(t *testing.T) {
	setupTestQueueCache(t)
	project := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(project, alias))
	q := NewFileQueue(project)
	require.NotNil(t, q)
	assert.Equal(t, q.Path(), NewFileQueue(alias).Path())
	assert.NotEqual(t, q.Path(), NewFileQueue(t.TempDir()).Path())
	assert.Equal(t, paths.TelemetryCacheDir(), filepath.Dir(q.Path()))
	assert.Len(t, strings.TrimSuffix(filepath.Base(q.Path()), ".jsonl"), 64)
	assert.Nil(t, NewFileQueue(filepath.Join(project, "missing")))
	t.Setenv("XDG_CACHE_HOME", "relative-cache")
	assert.Nil(t, NewFileQueue(project))
}

func TestFileQueue_IgnoresRepositorySymlinks(t *testing.T) {
	for _, component := range []string{"file", "cache", "sageox"} {
		t.Run(component, func(t *testing.T) {
			project := t.TempDir()
			setupTestProject(t, project)
			sageoxDir := filepath.Join(project, ".sageox")
			cacheDir := filepath.Join(sageoxDir, "cache")
			require.NoError(t, os.Mkdir(cacheDir, 0700))
			outside := t.TempDir()
			sentinel := filepath.Join(outside, "telemetry.jsonl")
			switch component {
			case "file":
				require.NoError(t, os.Symlink(sentinel, filepath.Join(cacheDir, "telemetry.jsonl")))
			case "cache":
				require.NoError(t, os.Remove(cacheDir))
				require.NoError(t, os.Symlink(outside, cacheDir))
			case "sageox":
				moved := filepath.Join(outside, ".sageox")
				require.NoError(t, os.Rename(sageoxDir, moved))
				require.NoError(t, os.Symlink(moved, sageoxDir))
				sentinel = filepath.Join(moved, "cache", "telemetry.jsonl")
			}
			data := []byte(strings.Repeat("external data\n", maxFileSize/10))
			require.Greater(t, len(data), maxFileSize)
			require.NoError(t, os.WriteFile(sentinel, data, 0600))
			q := NewFileQueue(project)
			require.NotNil(t, q)
			require.NoError(t, q.Append(Event{Type: EventCommandComplete}))
			events, err := q.ReadAll()
			require.NoError(t, err)
			require.Len(t, events, 1)
			require.NoError(t, q.Truncate())
			got, err := os.ReadFile(sentinel)
			require.NoError(t, err)
			assert.Equal(t, data, got)
		})
	}
}

type queueFlushTransport func(*http.Request) (*http.Response, error)

func (f queueFlushTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFileQueue_FlushRejectsSymlinkSubstitution(t *testing.T) {
	for _, component := range []string{"file", "parent"} {
		t.Run(component, func(t *testing.T) {
			project := t.TempDir()
			setupTestProject(t, project)
			q := NewFileQueue(project)
			require.NoError(t, q.Append(Event{Type: EventCommandComplete, Timestamp: time.Now().Add(-2 * time.Hour)}))
			require.True(t, q.ShouldFlush())
			outside := t.TempDir()
			sentinel := filepath.Join(outside, filepath.Base(q.Path()))
			data := []byte(strings.Repeat("external data\n", maxFileSize/10))
			require.NoError(t, os.WriteFile(sentinel, data, 0600))
			posted := false
			client := &Client{
				enabled:   true,
				fileQueue: q,
				baseURL:   "https://telemetry.invalid",
				httpClient: &http.Client{Transport: queueFlushTransport(func(*http.Request) (*http.Response, error) {
					posted = true
					path, target := q.Path(), sentinel
					if component == "parent" {
						path, target = filepath.Dir(path), outside
					}
					require.NoError(t, os.Rename(path, path+".saved"))
					require.NoError(t, os.Symlink(target, path))
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
				})},
			}
			client.flushFileQueue()
			require.True(t, posted, "test must reach successful POST cleanup")
			assert.Error(t, q.Append(Event{Type: EventCommandComplete}), "must not truncate an oversized symlink target")
			events, err := q.ReadAll()
			assert.Error(t, err)
			assert.Empty(t, events)
			assert.Error(t, q.Truncate())
			got, err := os.ReadFile(sentinel)
			require.NoError(t, err)
			assert.Equal(t, data, got)
		})
	}
}
