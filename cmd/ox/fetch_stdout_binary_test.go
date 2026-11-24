package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/testguard"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `ox fetch --stdout` promises the file's exact bytes on a pipe. Stdout on a
// pipe goes through the ANSI-stripping writer, which reads its input as
// terminal text: a C1 byte (0x9d starts an OSC string), an ESC or invalid
// UTF-8 in a binary file would be parsed as an escape and the bytes after it
// dropped. The cache-hit path needs no server, so this runs the real binary.
func TestFetchStdoutWritesBinaryBytesUnchanged(t *testing.T) {
	skipIntegration(t)
	oxBin := testguard.BuildOxBinary(t, repoPath("..", ".."))

	content := []byte("BM\x9d\x00\x1b]8;;not-a-link\x1b[31m\xff\xfe\xc3(" + string(bytes.Repeat([]byte{0x41}, 70*1024)) + "\x9c\x07\x00end")
	sum := sha256.Sum256(content)
	repo := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(repo, ".git"), 0o755))
	pointer := filepath.Join(repo, "frame.bmp")
	require.NoError(t, os.WriteFile(pointer, []byte(fmt.Sprintf(
		"version https://git-lfs.github.com/spec/v1\noid sha256:%s\nsize %d\n", hex.EncodeToString(sum[:]), len(content))), 0o644))
	cache := filepath.Join(repo, ".sageox", "cache", "frame.bmp")
	require.NoError(t, os.MkdirAll(filepath.Dir(cache), 0o755))
	require.NoError(t, os.WriteFile(cache, content, 0o644))

	env := append(noInputCLIEnv(t), "FEATURE_CLOUD=false", "FEATURE_AUTH=false", "CLICOLOR_FORCE=0")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := testguard.OxCmdContext(t, ctx, oxBin, repo, env, "fetch", "--stdout", pointer)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	require.NoError(t, cmd.Run(), "stderr: %s", stderr.String())
	assert.True(t, bytes.Equal(content, stdout.Bytes()), "got %d bytes, want %d; stderr: %s", stdout.Len(), len(content), stderr.String())
}

// A writer a caller set on the command wins (tests, embedding); otherwise the
// real stdout is used, never the ANSI-stripping pipe that os.Stdout points at.
func TestFetchStdoutTarget(t *testing.T) {
	var real bytes.Buffer
	old := binaryStdout
	binaryStdout = &real
	t.Cleanup(func() { binaryStdout = old })

	plain := &cobra.Command{}
	assert.Same(t, &real, fetchStdout(plain).(*bytes.Buffer), "no writer set: the real stdout")

	var own bytes.Buffer
	set := &cobra.Command{}
	set.SetOut(&own)
	assert.Same(t, &own, fetchStdout(set).(*bytes.Buffer), "a writer set on the command wins")
}
