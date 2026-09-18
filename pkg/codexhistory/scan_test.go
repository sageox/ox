package codexhistory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/require"
)

const testID = "01a0a62a-f6d2-7a62-9213-fd142782db9b"
const testHeader = `{"timestamp":"2026-09-15T10:00:00Z","type":"session_meta","payload":{"id":"` + testID + `","cwd":"/project","source":"cli"}}` + "\n"

func message(text string) string {
	b, _ := json.Marshal(map[string]any{"timestamp": "2026-09-15T11:00:00Z", "type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": text}}}})
	return string(b) + "\n"
}
func sourceFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(p, []byte(content), 0600))
	return p
}
func TestStreamLargeNativeHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("large native history fixture exceeds 64 MiB")
	}
	p := sourceFile(t, testHeader)
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
	require.NoError(t, err)
	const count = 8500
	for i := 0; i < count; i++ {
		_, err = fmt.Fprint(f, message(fmt.Sprintf("%d:%s", i, strings.Repeat("x", 8192))))
		require.NoError(t, err)
	}
	require.NoError(t, f.Close())
	seen := 0
	snapshot, err := Stream(context.Background(), p, func(e adapterprotocol.RawEntry) error {
		require.True(t, strings.HasPrefix(e.Content, fmt.Sprintf("%d:", seen)))
		seen++
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, count, seen)
	require.Greater(t, snapshot.Size, int64(64<<20))
	require.Len(t, snapshot.Generation, 64)
	after, err := Stream(context.Background(), p, nil)
	require.NoError(t, err)
	require.Equal(t, snapshot, after)
}
func TestStreamRejectsPartialOrChangingSource(t *testing.T) {
	for name, content := range map[string]string{"missing_header": message("x"), "malformed": testHeader + "{broken}\n", "unterminated": testHeader + strings.TrimSuffix(message("x"), "\n"), "bad_time": testHeader + strings.Replace(message("x"), "2026-09-15T11:00:00Z", "bad", 1)} {
		t.Run(name, func(t *testing.T) {
			_, err := Stream(context.Background(), sourceFile(t, content), nil)
			require.Error(t, err)
		})
	}
	for _, kind := range []string{"grow", "truncate", "replace"} {
		t.Run(kind, func(t *testing.T) {
			p := sourceFile(t, testHeader+message("x"))
			_, err := Stream(context.Background(), p, func(adapterprotocol.RawEntry) error {
				switch kind {
				case "grow":
					f, e := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
					if e != nil {
						return e
					}
					defer f.Close()
					_, e = f.WriteString(message("new"))
					return e
				case "truncate":
					return os.Truncate(p, 0)
				default:
					if e := os.Rename(p, p+".old"); e != nil {
						return e
					}
					return os.WriteFile(p, []byte(testHeader+message("x")), 0600)
				}
			})
			require.ErrorContains(t, err, "source changed")
		})
	}
}
func TestDiscoverCustomHomeArchivesAndOldDates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	got, err := Home()
	require.NoError(t, err)
	require.Equal(t, home, got)
	for _, dir := range []string{"sessions/2020/01/01", "archived_sessions"} {
		require.NoError(t, os.MkdirAll(filepath.Join(home, dir), 0700))
		require.NoError(t, os.WriteFile(filepath.Join(home, dir, "history.jsonl"), []byte(testHeader), 0600))
	}
	require.NoError(t, os.Symlink(filepath.Join(home, "archived_sessions/history.jsonl"), filepath.Join(home, "sessions/link.jsonl")))
	paths, err := Discover(home)
	require.NoError(t, err)
	require.Len(t, paths, 2)
}

func TestStreamPreservesHostInjectedToolCompletion(t *testing.T) {
	line := `{"timestamp":"2026-09-15T11:00:00Z","type":"response_item","payload":{"type":"function_call_output","id":"fco_native","name":"automation_update","namespace":"codex_app","output":"scheduled"}}` + "\n"
	var entries []adapterprotocol.RawEntry
	_, err := Stream(context.Background(), sourceFile(t, testHeader+line), func(e adapterprotocol.RawEntry) error { entries = append(entries, e); return nil })
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "scheduled", entries[0].ToolOutput)
	require.Equal(t, "codex_app.automation_update", entries[0].ToolName)
	require.Empty(t, entries[0].CallID, "host events must not be correlated to an invented call")
}
