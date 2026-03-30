package daemon

import (
	"testing"
	"time"
)

func TestTryConnectWithRetry_NoDaemon(t *testing.T) {
	// isolate from real daemon registry by running from a temp dir
	// (prevents config.FindProjectRoot from walking up to a real .sageox/)
	t.Chdir(t.TempDir())

	// with no daemon running, should return nil after retries
	// use short delay to speed up test
	start := time.Now()
	client := TryConnectWithRetry(1, 10*time.Millisecond)
	elapsed := time.Since(start)

	if client != nil {
		t.Errorf("TryConnectWithRetry() with no daemon should return nil, got %v", client)
	}

	// should have taken at least initialDelay for the retry
	if elapsed < 10*time.Millisecond {
		t.Errorf("TryConnectWithRetry() returned too quickly, elapsed %v", elapsed)
	}
}

func TestShouldUseDaemon_NoDaemonRunning(t *testing.T) {
	t.Chdir(t.TempDir())

	// when no daemon running, should return false
	if got := ShouldUseDaemon(); got != false {
		t.Errorf("ShouldUseDaemon() with no daemon should return false, got %v", got)
	}
}

func TestTryConnectOrDirect_NoDaemon(t *testing.T) {
	t.Chdir(t.TempDir())

	// when no daemon running, should return nil
	client := TryConnectOrDirect()
	if client != nil {
		t.Errorf("TryConnectOrDirect() with no daemon should return nil, got %v", client)
	}
}

func TestTryConnectOrDirectForSync_NoDaemon(t *testing.T) {
	t.Chdir(t.TempDir())

	// when no daemon running, should return nil
	client := TryConnectOrDirectForSync()
	if client != nil {
		t.Errorf("TryConnectOrDirectForSync() with no daemon should return nil, got %v", client)
	}
}

func TestTryConnectOrDirectForCheckout_NoDaemon(t *testing.T) {
	t.Chdir(t.TempDir())

	// when no daemon running, should return nil
	client := TryConnectOrDirectForCheckout()
	if client != nil {
		t.Errorf("TryConnectOrDirectForCheckout() with no daemon should return nil, got %v", client)
	}
}
