package hooks

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const (
	hookTimeout        = 500 * time.Millisecond
	hookKillDelay      = 500 * time.Millisecond
	maxConcurrentHooks = 4
)

// HookRunner executes user-defined hook scripts for matching events.
// Scripts receive the event as JSON on stdin with a hard 500ms timeout.
type HookRunner struct {
	mu     sync.RWMutex
	hooks  []HookConfig
	sem    chan struct{}
	wg     sync.WaitGroup
	logger *slog.Logger
}

// NewHookRunner creates a HookRunner with the given hooks.
func NewHookRunner(hooks []HookConfig, logger *slog.Logger) *HookRunner {
	return &HookRunner{
		hooks:  hooks,
		sem:    make(chan struct{}, maxConcurrentHooks),
		logger: logger,
	}
}

// SetHooks replaces the current hook configuration.
func (r *HookRunner) SetHooks(hooks []HookConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hooks = hooks
}

// Dispatch finds hooks matching the event and runs each in a background goroutine.
// Never blocks the caller.
func (r *HookRunner) Dispatch(ctx context.Context, event Event) {
	r.mu.RLock()
	hooks := r.hooks
	r.mu.RUnlock()

	for _, hook := range hooks {
		if hook.Event != "*" && hook.Event != event.Name {
			continue
		}
		h := hook
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.run(ctx, event, h)
		}()
	}
}

// Wait blocks until all dispatched hooks have finished.
// Intended for tests; production callers should treat Dispatch as fire-and-forget.
func (r *HookRunner) Wait() {
	r.wg.Wait()
}

func (r *HookRunner) run(ctx context.Context, event Event, hook HookConfig) {
	r.sem <- struct{}{}
	defer func() { <-r.sem }()

	start := time.Now()

	eventJSON, err := event.Marshal()
	if err != nil {
		r.logger.Warn("hook marshal failed", "event", event.Name, "command", hook.Command, "error", err)
		return
	}

	cmd := exec.Command("sh", "-c", hook.Command)
	cmd.Stdin = bytes.NewReader(eventJSON)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = append(os.Environ(),
		"OX_EVENT="+event.Name,
		"OX_EVENT_TIMESTAMP="+event.Timestamp.UTC().Format(time.RFC3339),
	)

	if err := cmd.Start(); err != nil {
		r.logger.Warn("hook start failed", "event", event.Name, "command", hook.Command, "error", err)
		return
	}

	// signal the entire process group so forked children are also terminated
	pgid := cmd.Process.Pid
	termTimer := time.AfterFunc(hookTimeout, func() {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
	})
	killTimer := time.AfterFunc(hookTimeout+hookKillDelay, func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	})

	waitErr := cmd.Wait()
	elapsed := time.Since(start)
	termTimer.Stop()
	killTimer.Stop()

	if waitErr == nil {
		r.logger.Debug("hook completed", "event", event.Name, "command", hook.Command, "duration", elapsed)
		return
	}

	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		if exitErr.ExitCode() == -1 {
			r.logger.Warn("hook timed out", "event", event.Name, "command", hook.Command, "duration", elapsed)
		} else {
			r.logger.Warn("hook failed", "event", event.Name, "command", hook.Command, "exit", exitErr.ExitCode(), "duration", elapsed)
		}
	} else {
		r.logger.Warn("hook error", "event", event.Name, "command", hook.Command, "error", waitErr, "duration", elapsed)
	}
}
