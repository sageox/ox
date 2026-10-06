package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewOwnArtifactUploader_NoCredentialsFailsClosed covers a coworker who is not logged in: the uploader
// must exist (so the repair can report why) but refuse to hand out a client, never an unauthenticated one.
func TestNewOwnArtifactUploader_NoCredentialsFailsClosed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/.config")
	t.Setenv("XDG_DATA_HOME", home+"/.local/share")
	t.Setenv("XDG_CACHE_HOME", home+"/.cache")
	t.Setenv("SAGEOX_ENDPOINT", "http://127.0.0.1:1")

	ledger := newWedgedLedger(t, false)

	uploader := newOwnArtifactUploader(ledger)

	require.NotNil(t, uploader)
	require.NotNil(t, uploader.client)
	client, err := uploader.client()
	assert.Error(t, err)
	assert.Nil(t, client)
}
