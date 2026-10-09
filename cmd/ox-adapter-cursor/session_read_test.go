package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
	"github.com/stretchr/testify/require"
)

func cursorReaderFile(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(path, data, 0600))
	return path
}

// Every native LF split must reconstruct the same visible entries, even when
// the first batch ends on metadata and the next read runs without parser state.
func TestCursorReaderRealFixturesAgreeAtEveryLineSplit(t *testing.T) {
	cases := []struct {
		name    string
		entries int
		skipped int
	}{
		{"0012-stop.jsonl", 5, 1},
		{"0015-stop.jsonl", 7, 2},
		{"0019-stop.jsonl", 2, 1},
		{"0027-stop.jsonl", 4, 1},
		{"0030-stop.jsonl", 9, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "desktop", "transcript-snapshots", tc.name))
			require.NoError(t, err)
			path := cursorReaderFile(t, data)
			full, err := handleRead(adapterprotocol.ReadParams{SessionFile: path})
			require.NoError(t, err)
			require.Len(t, full.Entries, tc.entries)
			require.Equal(t, tc.skipped, full.Skipped)
			for _, entry := range full.Entries {
				require.Empty(t, entry.Timestamp)
				require.Empty(t, entry.CallID)
				require.Empty(t, entry.ToolOutput)
				require.False(t, entry.IsError)
			}
			boundaries := []int{0}
			for i, b := range data {
				if b == '\n' {
					boundaries = append(boundaries, i+1)
				}
			}
			for _, split := range boundaries {
				require.NoError(t, os.WriteFile(path, data[:split], 0600))
				first, err := handleReadFromOffset(adapterprotocol.ReadFromOffsetParams{SessionFile: path})
				require.NoError(t, err)
				require.Equal(t, int64(split), first.NewOffset)
				require.NoError(t, os.WriteFile(path, data, 0600))
				second, err := handleReadFromOffset(adapterprotocol.ReadFromOffsetParams{SessionFile: path, Offset: first.NewOffset})
				require.NoError(t, err)
				require.Equal(t, int64(len(data)), second.NewOffset)
				require.Equal(t, full.Entries, append(first.Entries, second.Entries...))
			}
			metadata, err := handleReadMetadata(adapterprotocol.ReadParams{SessionFile: path})
			require.NoError(t, err)
			require.Equal(t, &adapterprotocol.ReadMetadataResult{}, metadata)
		})
	}
}

// Partial writes must be held until LF arrives, including valid JSON without LF.
func TestCursorReaderDoesNotAcknowledgePartialRecords(t *testing.T) {
	first := "{\"type\":\"turn_ended\",\"status\":\"success\"}\n"
	last := `{"role":"user","message":{"content":[{"type":"text","text":"next"}]}}`
	path := cursorReaderFile(t, []byte(first+last))
	batch, err := handleReadFromOffset(adapterprotocol.ReadFromOffsetParams{SessionFile: path})
	require.NoError(t, err)
	require.Empty(t, batch.Entries)
	require.Equal(t, int64(len(first)), batch.NewOffset)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	require.NoError(t, err)
	_, err = f.WriteString("\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	batch, err = handleReadFromOffset(adapterprotocol.ReadFromOffsetParams{SessionFile: path, Offset: batch.NewOffset})
	require.NoError(t, err)
	require.Equal(t, []adapterprotocol.RawEntry{{Role: "user", Content: "next"}}, batch.Entries)
	require.Equal(t, int64(len(first+last)+1), batch.NewOffset)
}

// Generic tail helpers reset bad cursors. Cursor must refuse replay instead.
func TestCursorReaderRejectsOffsetsAndMixedFormatErrors(t *testing.T) {
	line := []byte("{\"type\":\"turn_ended\",\"status\":\"success\"}\n")
	path := cursorReaderFile(t, line)
	for _, offset := range []int64{-1, 1, int64(len(line) - 1), int64(len(line) + 1)} {
		result, err := handleReadFromOffset(adapterprotocol.ReadFromOffsetParams{SessionFile: path, Offset: offset})
		require.ErrorIs(t, err, errCursorInvalidOffset)
		require.Nil(t, result)
	}
	require.NoError(t, os.WriteFile(path, []byte("{}\n"), 0600))
	_, err := handleReadFromOffset(adapterprotocol.ReadFromOffsetParams{SessionFile: path, Offset: int64(len(line))})
	require.ErrorIs(t, err, errCursorInvalidOffset)
	for _, bad := range []string{"not json\n", "{\"type\":\"future\"}\n", "\n"} {
		require.NoError(t, os.WriteFile(path, append(bytes.Clone(line), []byte(bad)...), 0600))
		result, err := handleReadFromOffset(adapterprotocol.ReadFromOffsetParams{SessionFile: path})
		require.ErrorIs(t, err, errCursorSourceFormat)
		require.Nil(t, result)
		_, err = handleReadMetadata(adapterprotocol.ReadParams{SessionFile: path})
		require.ErrorIs(t, err, errCursorSourceFormat)
	}
}

func TestCursorReaderBoundsAndInvalidSources(t *testing.T) {
	path := cursorReaderFile(t, nil)
	empty, err := handleRead(adapterprotocol.ReadParams{SessionFile: path})
	require.NoError(t, err)
	require.NotNil(t, empty.Entries)
	require.Empty(t, empty.Entries)
	for _, invalid := range []string{"relative.jsonl", filepath.Dir(path), filepath.Join(t.TempDir(), "absent.jsonl")} {
		_, err := handleRead(adapterprotocol.ReadParams{SessionFile: invalid})
		require.Error(t, err)
	}
	// A record beyond the scanner's usual 64 KiB remains readable.
	long := `{"role":"assistant","message":{"content":[{"type":"text","text":"` + strings.Repeat("x", 70<<10) + `"}]}}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(long), 0600))
	result, err := handleRead(adapterprotocol.ReadParams{SessionFile: path})
	require.NoError(t, err)
	require.Len(t, result.Entries, 1)
	require.Len(t, result.Entries[0].Content, 70<<10)
	// Sparse truncation exercises the source bound without allocating 64 MiB.
	require.NoError(t, os.Truncate(path, cursorMaxSourceBytes+1))
	_, err = handleRead(adapterprotocol.ReadParams{SessionFile: path})
	require.ErrorIs(t, err, errCursorSourceTooLarge)
}

// Even a valid parsed batch cannot be acknowledged if its source changed
// during the read. Each mutation is injected after a real shared-tail read.
func TestCursorReaderRejectsConcurrentSourceMutation(t *testing.T) {
	data := []byte("{\"type\":\"turn_ended\",\"status\":\"success\"}\n")
	cases := map[string]func(*testing.T, string){
		"append": func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, append(bytes.Clone(data), data...), 0600))
		},
		"replace same bytes": func(t *testing.T, path string) {
			replacement := path + ".new"
			require.NoError(t, os.WriteFile(replacement, data, 0600))
			require.NoError(t, os.Rename(replacement, path))
		},
		"shrink": func(t *testing.T, path string) {
			require.NoError(t, os.Truncate(path, 0))
		},
		"disappear": func(t *testing.T, path string) {
			require.NoError(t, os.Remove(path))
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			path := cursorReaderFile(t, data)
			batch, err := readCursorSnapshot(path, 0, func(path string, offset int64, parse adapterruntime.LineParser) ([]adapterprotocol.RawEntry, int64, adapterruntime.TailStats, error) {
				entries, next, stats, err := adapterruntime.TailJSONLWithStats(path, offset, parse)
				require.NoError(t, err)
				mutate(t, path)
				return entries, next, stats, err
			})
			require.ErrorIs(t, err, errCursorSourceChanged)
			require.Nil(t, batch)
		})
	}
}

func TestCursorReaderRejectsOversizeCompleteRecord(t *testing.T) {
	if testing.Short() {
		t.Skip("short: large JSONL record")
	}
	data := `{"role":"assistant","message":{"content":[{"type":"text","text":"` + strings.Repeat("x", 10<<20) + `"}]}}` + "\n"
	path := cursorReaderFile(t, []byte(data))
	result, err := handleRead(adapterprotocol.ReadParams{SessionFile: path})
	require.ErrorIs(t, err, errCursorSourceFormat)
	require.ErrorContains(t, err, "line limit")
	require.Nil(t, result)
}

func TestCursorReaderErrorsDoNotExposeNativePaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-conversation.jsonl")
	_, err := handleRead(adapterprotocol.ReadParams{SessionFile: path})
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NotContains(t, err.Error(), filepath.Dir(path))
	require.NotContains(t, err.Error(), "private-conversation")
}
