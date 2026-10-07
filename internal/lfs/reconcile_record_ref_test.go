package lfs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// meta.json can vanish between the ownership read and the lock; the mutator then
// receives nil and must refuse rather than panic or invent a manifest.
func TestRecordFileRef_NilMetaIsAnErrorNotAPanic(t *testing.T) {
	mutate := recordFileRef("ses_1", "raw.jsonl", FileRef{OID: "sha256:abc", Size: 3})

	got, err := mutate(nil)

	require.Error(t, err)
	assert.Nil(t, got)
	assert.Contains(t, err.Error(), "disappeared")

	// negative control: an existing meta gets the entry, including from a nil Files map
	meta, err := mutate(&SessionMeta{})
	require.NoError(t, err)
	assert.Equal(t, "sha256:abc", meta.Files["raw.jsonl"].OID)
}
