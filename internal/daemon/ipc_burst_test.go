package daemon

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testHoldMsg    = "test-hold"
	testBurstLimit = 3
)

// startBurstServer starts a server whose connection limit is testBurstLimit and
// whose testHoldMsg handler blocks until release is closed. started receives
// one value per handler entry.
func startBurstServer(t *testing.T, connWait time.Duration) (release chan struct{}, started chan struct{}) {
	t.Helper()
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", "/tmp") // short path: unix socket limit ~104 chars

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := NewServer(logger)
	server.DisablePeerCredForTesting()
	server.connSem = make(chan struct{}, testBurstLimit)
	server.connWait = connWait

	release = make(chan struct{})
	started = make(chan struct{}, 16)
	server.router.Register(testHoldMsg, func(_ *Server, _ Message, _ net.Conn) HandlerResult {
		started <- struct{}{}
		<-release
		return HandlerResult{Response: &Response{Success: true}}
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Start(ctx) }()
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		cancel()
		<-done
	})

	require.Eventually(t, func() bool {
		return newDirectClient().Ping() == nil
	}, 2*time.Second, 10*time.Millisecond)
	return release, started
}

// sendHold dials and sends a testHoldMsg request. The returned func reports
// whether a response arrived (false = the server closed the connection without
// answering, i.e. it was rejected).
func sendHold(t *testing.T) (wait func() bool) {
	t.Helper()
	conn, err := newDirectClient().Connect()
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	_, err = conn.Write([]byte(`{"type":"` + testHoldMsg + `"}` + "\n"))
	require.NoError(t, err)
	return func() bool {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, err := bufio.NewReader(conn).ReadBytes('\n')
		return err == nil
	}
}

func TestServer_ConnectionBurstQueuesInsteadOfRejecting(t *testing.T) {
	release, started := startBurstServer(t, 3*time.Second)

	var waits []func() bool
	for range testBurstLimit {
		waits = append(waits, sendHold(t))
		<-started
	}
	// more connections than the limit arrive while every slot is held
	const extra = 2
	for range extra {
		waits = append(waits, sendHold(t))
	}

	// slots free up well inside the wait bound
	time.AfterFunc(200*time.Millisecond, func() { close(release) })

	var wg sync.WaitGroup
	served := make([]bool, len(waits))
	for i, w := range waits {
		wg.Add(1)
		go func() {
			defer wg.Done()
			served[i] = w()
		}()
	}
	wg.Wait()
	for i, ok := range served {
		assert.True(t, ok, "connection %d was dropped instead of queued", i)
	}
}

func TestServer_ConnectionLimitStillRejectsWhenStuck(t *testing.T) {
	release, started := startBurstServer(t, 100*time.Millisecond)

	var held []func() bool
	for range testBurstLimit {
		held = append(held, sendHold(t))
		<-started
	}

	// nothing releases within the wait bound: the extra connection is rejected
	rejected := sendHold(t)
	assert.False(t, rejected(), "extra connection should be closed without a response")

	close(release)
	for i, w := range held {
		assert.True(t, w(), "held connection %d should still complete", i)
	}
}

func TestRejectionLog_CoalescesBurst(t *testing.T) {
	var buf lockedBuffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	var r rejectionLog
	for range 50 {
		r.note(logger, 512)
	}
	assert.Equal(t, 1, strings.Count(buf.String(), "connection limit reached"), "one warning per burst")
	r.flush(logger, 512)
	assert.Contains(t, buf.String(), "additional_rejected=49")
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
