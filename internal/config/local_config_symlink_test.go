package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateOrUpdateSymlink_RegularFileBlocks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require elevated privileges on Windows")
	}

	tmpDir := t.TempDir()
	target := filepath.Join(tmpDir, "target")
	require.NoError(t, os.MkdirAll(target, 0755))

	// create a regular file where the symlink should go
	link := filepath.Join(tmpDir, "link")
	require.NoError(t, os.WriteFile(link, []byte("not a symlink"), 0644))

	err := createOrUpdateSymlink(link, target)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not a symlink")
}
