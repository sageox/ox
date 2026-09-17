package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCountValidatedEntriesRejectsPartialConversion(t *testing.T) {
	for _, test := range []struct {
		name, raw string
		count     int
		bad       bool
	}{
		{"header only", "{\"type\":\"header\"}\n", 0, false},
		{"alternate header and footer", "{\"_meta\":{}}\n{\"type\":\"user\",\"content\":\"hello\"}\n{\"type\":\"footer\"}\n", 1, false},
		{"malformed middle", "{\"type\":\"user\"}\ninvalid\n{\"type\":\"assistant\"}\n", 0, true},
		{"missing type", "{}\n", 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "raw.jsonl")
			require.NoError(t, os.WriteFile(path, []byte(test.raw), 0600))
			count, err := CountValidatedEntries(context.Background(), path)
			if test.bad {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, test.count, count)
		})
	}
}

func TestCountValidatedEntriesAbove64MiB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raw.jsonl")
	file, err := os.Create(path)
	require.NoError(t, err)
	_, err = file.WriteString("{\"type\":\"header\"}\n")
	require.NoError(t, err)
	record := "{\"type\":\"user\",\"content\":\"" + strings.Repeat("x", 32*1024) + "\"}\n"
	const records = 2100
	for range records {
		_, err = file.WriteString(record)
		require.NoError(t, err)
	}
	require.NoError(t, file.Close())
	count, err := CountValidatedEntries(context.Background(), path)
	require.NoError(t, err)
	require.Equal(t, records, count)
}

func TestCountValidatedEntriesKeepsRecordLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raw.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("{\"type\":\"user\",\"content\":\""+strings.Repeat("x", 10*1024*1024)+"\"}\n"), 0600))
	_, err := CountValidatedEntries(context.Background(), path)
	require.ErrorContains(t, err, "token too long")
}

func TestCountValidatedEntriesLegacyMetadataAndFooter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raw.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("{\"metadata\":{}}\n{\"type\":\"user\",\"content\":\"hello\"}\n{\"entry_count\":1}\n"), 0600))
	count, err := CountValidatedEntries(context.Background(), path)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}
