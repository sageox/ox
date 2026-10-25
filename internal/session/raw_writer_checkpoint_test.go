package session

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Failure prevented: a failed append or checkpoint destroys the prior prefix,
// acknowledges missing entries, or bypasses redaction on the retry.
func TestRawCheckpointPreservesPrefixAndRetriesRedactedBatch(t *testing.T) {
	for _, failure := range []string{"none", "encode", "checkpoint"} {
		t.Run(failure, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "raw.jsonl")
			prefix := []byte("{\"type\":\"header\",\"metadata\":{}}\n")
			require.NoError(t, os.WriteFile(path, prefix, 0600))
			w, err := NewRawWriter(path, "")
			require.NoError(t, err)
			t.Cleanup(func() { _ = w.Close() })
			entries := []Entry{{Type: EntryTypeUser, Content: "first"}, {Type: EntryTypeUser, Content: "AKIA" + "IOSFODNN7EXAMPLE"}}
			if failure == "encode" {
				entries[1].Timestamp = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			}
			checkpointErr := errors.New("checkpoint blocked")
			called := false
			err = w.AppendCheckpointed(entries, func() error {
				called = true
				data, readErr := os.ReadFile(path)
				require.NoError(t, readErr)
				require.Contains(t, string(data), "[REDACTED_AWS_KEY]")
				if failure == "checkpoint" {
					return checkpointErr
				}
				return nil
			})
			require.Equal(t, failure != "encode", called)
			if failure != "none" {
				require.Error(t, err)
				if failure == "checkpoint" {
					require.ErrorIs(t, err, checkpointErr)
				}
				data, readErr := os.ReadFile(path)
				require.NoError(t, readErr)
				require.Equal(t, prefix, data)
				entries[1].Timestamp = time.Time{}
				require.NoError(t, w.AppendCheckpointed(entries, func() error { return nil }))
			} else {
				require.NoError(t, err)
			}
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Contains(t, string(data), "[REDACTED_AWS_KEY]")
			require.NotContains(t, string(data), "AKIA"+"IOSFODNN7EXAMPLE")
		})
	}
}

// A stop hook can stamp a footer while the watcher is publishing a cursor.
// Rollback must neither delete that footer nor another appender's entries.
func TestRawCheckpointRollbackPreservesConcurrentAppenders(t *testing.T) {
	for _, kind := range []string{"entry", "raw", "carrier", "carrier-directory-alias", "carrier-file-alias"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "raw.jsonl")
			w, err := NewRawWriter(path, "")
			require.NoError(t, err)
			t.Cleanup(func() { _ = w.Close() })
			other, err := NewRawWriter(path, "")
			require.NoError(t, err)
			t.Cleanup(func() { _ = other.Close() })
			carrierPath := path
			if kind == "carrier-directory-alias" || kind == "carrier-file-alias" {
				alias := filepath.Join(t.TempDir(), "alias")
				target := path
				if kind == "carrier-directory-alias" {
					target = filepath.Dir(path)
				}
				if err := os.Symlink(target, alias); err != nil {
					if runtime.GOOS == "windows" {
						t.Skipf("symlinks unavailable: %v", err)
					}
					require.NoError(t, err)
				}
				carrierPath = alias
				if kind == "carrier-directory-alias" {
					carrierPath = filepath.Join(alias, "raw.jsonl")
				}
			}
			started := make(chan struct{})
			done := make(chan error, 1)
			checkpointErr := errors.New("checkpoint blocked")
			err = w.AppendCheckpointed([]Entry{{Type: EntryTypeUser, Content: "uncommitted batch"}}, func() error {
				go func() {
					close(started)
					switch kind {
					case "entry":
						done <- other.WriteEntry(&Entry{Type: EntryTypeUser, Content: "concurrent append"})
					case "raw":
						done <- other.WriteRaw(map[string]any{"type": "user", "content": "concurrent append"})
					case "carrier", "carrier-directory-alias", "carrier-file-alias":
						done <- StampRawCarrier(carrierPath, CarrierStamp{StoppedAt: time.Now()})
					}
				}()
				<-started
				select {
				case err := <-done:
					t.Errorf("concurrent append escaped checkpoint lock: %v", err)
					// Return the result to let the test finish even without the lock.
					done <- err
				case <-time.After(100 * time.Millisecond):
				}
				return checkpointErr
			})
			require.ErrorIs(t, err, checkpointErr)
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("concurrent appender did not resume after rollback")
			}
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			require.NotContains(t, string(data), "uncommitted batch")
			if kind != "entry" && kind != "raw" {
				require.Contains(t, string(data), `"type":"footer"`)
			} else {
				require.Contains(t, string(data), "concurrent append")
			}
		})
	}
}

func TestRawCheckpointReportsUnavailableWriter(t *testing.T) {
	var missing *RawWriter
	require.ErrorContains(t, missing.AppendCheckpointed(nil, func() error {
		t.Fatal("checkpoint must not run without a writer")
		return nil
	}), "nil")
	w, err := NewRawWriter(filepath.Join(t.TempDir(), "raw.jsonl"), "")
	require.NoError(t, err)
	require.NoError(t, w.Close())
	require.ErrorContains(t, w.AppendCheckpointed(nil, func() error {
		t.Fatal("checkpoint must not run with a closed file")
		return nil
	}), "inspect raw checkpoint")
}

// If rollback itself fails, retain both errors so the caller cannot mistake
// the result for a successfully restored prefix and blindly resume capture.
func TestRawCheckpointReportsRollbackFailure(t *testing.T) {
	w, err := NewRawWriter(filepath.Join(t.TempDir(), "raw.jsonl"), "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })
	checkpointErr := errors.New("publication failed")
	err = w.AppendCheckpointed([]Entry{{Content: "batch"}}, func() error {
		require.NoError(t, w.Close())
		return checkpointErr
	})
	require.ErrorIs(t, err, checkpointErr)
	require.ErrorContains(t, err, "rollback uncheckpointed raw batch")
}
