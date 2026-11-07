package pettest

import (
	"bytes"
	"image/png"
	"testing"

	"github.com/sageox/ox/internal/pet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexSheet(t *testing.T) {
	sheet, err := CodexSheet("1.2.3")
	require.NoError(t, err)
	_, err = png.Decode(bytes.NewReader(sheet.PNG))
	require.NoError(t, err)
	v, err := pet.CodexManifestVersion(sheet.Manifest)
	require.NoError(t, err)
	assert.Equal(t, "1.2.3", v)
}
