package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/session/nativeimport"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Reuse the test executable as an actual external adapter. Running before
// testing parses flags lets ExternalAdapter use its real read protocol without
// compiling a second binary or relying on a platform-specific shell script.
func init() {
	if os.Getenv("OX_TEST_IMPORT_ADAPTER") != "1" || len(os.Args) != 4 || os.Args[1] != "read" || os.Args[2] != "--session-file" {
		return
	}
	path := os.Args[3]
	_, blocked := os.Stat(path + ".block")
	if strings.HasPrefix(filepath.Base(path), "slow-") || blocked == nil {
		if err := os.WriteFile(path+".started", nil, 0600); err != nil {
			os.Exit(1)
		}
		// The release file only supports cleanup when proving the old behavior.
		// Successful cancellation kills this process while it is still waiting.
		for {
			if _, err := os.Stat(path + ".release"); err == nil {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}
	result := adapterprotocol.ReadResult{Entries: []adapterprotocol.RawEntry{
		{Role: "user", Content: "Review this session"},
		{Role: "assistant", Content: "Here is the session's final reply"},
	}}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func importCancellationAdapter(t *testing.T) *adapters.ExternalAdapter {
	t.Helper()
	binary, err := os.Executable()
	require.NoError(t, err)
	t.Setenv("OX_TEST_IMPORT_ADAPTER", "1")
	// Child test executables must emit the adapter JSON directly, and race
	// instrumentation need not spend a second sleeping after a successful exit.
	t.Setenv("CLICOLOR_FORCE", "1")
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	var prior adapters.Adapter
	for _, name := range adapters.ListAdapters() {
		if name == "claude-code" {
			prior, err = adapters.GetAdapter(name)
			require.NoError(t, err)
			break
		}
	}
	adapters.Unregister("claude-code")
	adapter := adapters.NewExternalAdapterWithInfo(binary, &adapterprotocol.InfoResponse{
		Name: "claude-code", RequiredEnv: []string{"CLICOLOR_FORCE", "GORACE"},
	})
	adapters.Register(adapter)
	t.Cleanup(func() {
		adapters.Unregister("claude-code")
		if prior != nil {
			adapters.Register(prior)
		}
	})
	return adapter
}

func TestImportContent_CanceledAdapterReadsReleasePreviewSlots(t *testing.T) {
	projectRoot := t.TempDir()
	importCancellationAdapter(t)
	var cands []*importCandidate
	for _, id := range []string{"slow-a", "slow-b", "fast"} {
		path := filepath.Join(projectRoot, id)
		require.NoError(t, os.WriteFile(path, []byte("native session source"), 0600))
		info, err := os.Stat(path)
		require.NoError(t, err)
		cands = append(cands, &importCandidate{Session: nativeimport.Session{
			NativeID: id, Agent: nativeimport.AgentClaude, Path: path, Size: info.Size(), ModTime: info.ModTime(),
		}})
	}
	env := &importEnv{projectRoot: projectRoot, deps: importDeps{readNative: readNativeWithAdapter}}
	load := newImportPreviewLoader(env, cands)
	type pendingRead struct {
		cancel context.CancelFunc
		done   chan struct{}
		err    chan error
		path   string
	}
	var pending []pendingRead
	t.Cleanup(func() {
		for _, read := range pending {
			read.cancel()
			_ = os.WriteFile(read.path+".release", nil, 0600)
		}
		for _, read := range pending {
			select {
			case <-read.done:
			case <-time.After(3 * time.Second):
				t.Error("adapter read did not finish during cleanup")
			}
		}
	})
	for _, c := range cands[:2] {
		ctx, cancel := context.WithCancel(context.Background())
		read := pendingRead{cancel: cancel, done: make(chan struct{}), err: make(chan error, 1), path: c.Session.Path}
		pending = append(pending, read)
		go func() {
			defer close(read.done)
			_, err := load(ctx, c.Session.NativeID)
			read.err <- err
		}()
	}
	require.Eventually(t, func() bool {
		for _, read := range pending {
			if _, err := os.Stat(read.path + ".started"); err != nil {
				return false
			}
		}
		return true
	}, 3*time.Second, time.Millisecond, "both real adapter processes must occupy the preview reader slots")
	for _, read := range pending {
		read.cancel()
	}

	// No release file is present: this can succeed only if cancellation reaches
	// the running adapter processes and frees both loader semaphore slots.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	preview, err := load(ctx, "fast")
	require.NoError(t, err, "canceled reads must not block the newly focused session")
	assert.Equal(t, "Review this session", preview.OpeningRequest)
	assert.Equal(t, "Here is the session's final reply", preview.LastReply)
	for _, read := range pending {
		select {
		case err := <-read.err:
			assert.ErrorIs(t, err, context.Canceled)
		case <-ctx.Done():
			t.Fatal("canceled preview read did not return")
		}
		_, err := os.Stat(read.path + ".release")
		assert.True(t, os.IsNotExist(err), "adapter cancellation must not depend on the cleanup release")
	}
}

func TestImportPreparation_CancelStopsNativeAdapterAndRemovesStaging(t *testing.T) {
	f := newImportFixture(t)
	path := f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA,
		start: time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC), prompt: loginPrompt, reply: "Fixed."})
	env, _ := f.envFor(f.ledgerPath, importOptions{})
	cands, _, failure := planImport(context.Background(), importOptions{}, env)
	require.Nil(t, failure)
	require.Len(t, cands, 1)
	importCancellationAdapter(t)
	env.deps.readNative = readNativeWithAdapter
	require.NoError(t, os.WriteFile(path+".block", nil, 0600))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		_ = os.WriteFile(path+".release", nil, 0600)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("native adapter preparation did not finish during cleanup")
		}
	})
	var prepared *preparedImport
	var skip string
	var readErr error
	go func() {
		defer close(done)
		prepared, skip, readErr = prepareImport(ctx, env, cands[0])
	}()
	require.Eventually(t, func() bool {
		_, err := os.Stat(path + ".started")
		return err == nil
	}, 3*time.Second, time.Millisecond, "preparation must reach the actual adapter subprocess")
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceling import must stop its adapter read promptly")
	}
	assert.Nil(t, prepared)
	assert.Empty(t, skip)
	assert.ErrorIs(t, readErr, context.Canceled, "cancellation must not become a held session error")
	_, err := os.Stat(filepath.Join(env.stagingRoot, cands[0].Name))
	assert.True(t, os.IsNotExist(err), "canceled import must remove its staging directory")
	assert.Zero(t, f.summarizer.calls())
	assert.Zero(t, f.store.count())
}

func TestImportNativeAdapter_AlreadyCanceledDoesNotStartRead(t *testing.T) {
	adapter := importCancellationAdapter(t)
	path := filepath.Join(t.TempDir(), "slow-canceled")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	entries, err := adapter.ReadWithContext(ctx, path, importReadTimeout)
	assert.Nil(t, entries)
	assert.ErrorIs(t, err, context.Canceled)
	entries, err = readNativeWithAdapter(ctx, nativeimport.AgentClaude, path)
	assert.Nil(t, entries)
	assert.ErrorIs(t, err, context.Canceled)
	_, err = os.Stat(path + ".started")
	assert.True(t, os.IsNotExist(err), "already canceled reads must not spawn an adapter")
}
