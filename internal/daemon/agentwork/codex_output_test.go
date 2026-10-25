package agentwork

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBoundedCodexOutput(t *testing.T) {
	w := &boundedCodexOutput{limit: 3}
	n, err := w.Write([]byte("abcdef"))
	require.NoError(t, err)
	assert.Equal(t, 6, n)
	assert.Equal(t, "abc", w.buf.String())
	assert.True(t, w.overflow)
	n, err = w.Write([]byte("more"))
	require.NoError(t, err)
	assert.Equal(t, 4, n)
	assert.Equal(t, "abc", w.buf.String())
}
