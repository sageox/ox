package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/paths"
)

func TestStopOutputCarriesScreenshotNoticeOnce(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	var out sessionStopOutput
	addLiveMediaNotice(&out)
	if len(out.DataWarnings) != 0 {
		t.Fatalf("notice shown before a screenshot streamed: %v", out.DataWarnings)
	}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.DataDir(), "live-media-notice"), []byte("owed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	confirm := addLiveMediaNotice(&out)
	if len(out.DataWarnings) != 1 || out.DataWarnings[0] != livesession.MediaNotice {
		t.Fatalf("warnings = %v", out.DataWarnings)
	}
	// the output write failed: nothing confirmed, so the notice is still owed
	var retry sessionStopOutput
	confirmRetry := addLiveMediaNotice(&retry)
	if len(retry.DataWarnings) != 1 {
		t.Fatalf("a notice that was never delivered was lost: %v", retry.DataWarnings)
	}
	confirm() // delivered
	confirmRetry()
	var after sessionStopOutput
	addLiveMediaNotice(&after)
	if len(after.DataWarnings) != 0 {
		t.Fatalf("notice shown after it was delivered: %v", after.DataWarnings)
	}
}
