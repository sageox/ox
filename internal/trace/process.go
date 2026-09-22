package trace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/proc"
	"github.com/sageox/ox/internal/selfexec"
	"github.com/sageox/ox/internal/trace/receiver"
	"github.com/sageox/ox/internal/version"
)

const (
	DefaultPort    = 14318
	processTimeout = 5 * time.Second
)

// HealthInfo identifies the receiver answering on the configured loopback port.
type HealthInfo struct {
	Service    string `json:"service"`
	Version    string `json:"version"`
	PID        int    `json:"pid"`
	InstanceID string `json:"instance_id"`
}

type processState struct {
	HealthInfo
	Port          int    `json:"port"`
	ShutdownToken string `json:"shutdown_token"`
}

func pidPath() string           { return filepath.Join(StateDir(), "receiver.json") }
func processLock() *flock.Flock { return flock.New(filepath.Join(StateDir(), "receiver.lock")) }

func validatePort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("trace port must be between 1 and 65535")
	}
	return nil
}

func localClient() *http.Client {
	return &http.Client{
		Timeout:       500 * time.Millisecond,
		Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func receiverURL(port int, path string) string {
	return "http://127.0.0.1:" + strconv.Itoa(port) + path
}

// Health probes loopback directly, without proxies or redirects. An unrelated
// service answering HTTP 200 is never considered a receiver.
func Health(ctx context.Context, port int) (HealthInfo, error) {
	var info HealthInfo
	if err := validatePort(port); err != nil {
		return info, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, receiverURL(port, "/healthz"), nil)
	if err != nil {
		return info, err
	}
	resp, err := localClient().Do(req)
	if err != nil {
		return info, fmt.Errorf("trace receiver health: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return info, fmt.Errorf("trace receiver health returned HTTP %d", resp.StatusCode)
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&info); err != nil {
		return info, fmt.Errorf("decode trace receiver health: %w", err)
	}
	if info.Service != "ox-trace" || info.PID <= 0 || info.InstanceID == "" || info.Version == "" {
		return HealthInfo{}, errors.New("port is occupied by an unrecognized trace receiver")
	}
	return info, nil
}

func readState() (processState, error) {
	var state processState
	data, err := os.ReadFile(pidPath())
	if err != nil {
		return state, err
	}
	if err = json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("decode trace receiver state: %w", err)
	}
	return state, nil
}

func matches(info HealthInfo, state processState) bool {
	return info.Service == "ox-trace" && info.PID == state.PID && info.InstanceID == state.InstanceID && info.Version == state.Version
}

func withLaunchLock(ctx context.Context, fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, processTimeout)
	defer cancel()
	if err := ensurePrivateDir(StateDir()); err != nil {
		return fmt.Errorf("create trace state directory: %w", err)
	}
	lock := flock.New(filepath.Join(StateDir(), "launch.lock"))
	defer lock.Close()
	locked, err := lock.TryLockContext(ctx, 25*time.Millisecond)
	if err != nil {
		return fmt.Errorf("lock trace startup: %w", err)
	}
	if !locked {
		return errors.New("trace startup is busy")
	}
	return fn(ctx)
}

// EnsureRunning starts one detached receiver and waits until its health endpoint
// and private process record agree. The experimental command flag is supplied
// only to the child; the caller's environment is not changed.
func EnsureRunning(ctx context.Context, port int) error { return EnsureRunningIf(ctx, port, nil) }

// EnsureRunningIf rechecks persistent opt-in while holding the startup lock.
// This keeps a delayed startup hook from undoing a completed disable command.
func EnsureRunningIf(ctx context.Context, port int, allowed func() bool) error {
	if err := validatePort(port); err != nil {
		return err
	}
	return withLaunchLock(ctx, func(ctx context.Context) error {
		if allowed != nil && !allowed() {
			return nil
		}
		if state, err := readState(); err == nil {
			if info, healthErr := Health(ctx, state.Port); healthErr == nil && matches(info, state) {
				if state.Port != port {
					return fmt.Errorf("trace receiver already runs on port %d; disable it before changing ports", state.Port)
				}
				return nil
			}
		}
		// A foreground receiver or another already-launched child may be starting.
		lock := processLock()
		held, err := lock.TryLock()
		if err != nil {
			return fmt.Errorf("check trace receiver lock: %w", err)
		}
		_ = lock.Close()
		if !held {
			return waitReady(ctx, port)
		}
		// Report a busy port before launching, including unrecognized HTTP services.
		listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			return fmt.Errorf("trace port %d unavailable: %w", port, err)
		}
		_ = listener.Close()
		exe, err := selfexec.Path()
		if err != nil {
			return fmt.Errorf("locate ox executable: %w", err)
		}
		if err := ensurePrivateDir(filepath.Dir(LogPath())); err != nil {
			return fmt.Errorf("create trace log directory: %w", err)
		}
		logFile, err := openPrivateLog(LogPath())
		if err != nil {
			return fmt.Errorf("open trace receiver log: %w", err)
		}
		defer logFile.Close()
		if err = logFile.Chmod(0600); err != nil {
			return fmt.Errorf("secure trace receiver log: %w", err)
		}
		cmd := exec.Command(exe, "session", "trace", "serve", "--port", strconv.Itoa(port))
		cmd.Env = traceChildEnv(os.Environ())
		proc.Detach(cmd)
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		if err = cmd.Start(); err != nil {
			return fmt.Errorf("start trace receiver: %w", err)
		}
		// Reap the child while this caller lives. Process detachment lets it outlive
		// the caller; normal OS reparenting handles cleanup after the caller exits.
		go func() { _ = cmd.Wait() }()
		return waitReady(ctx, port)
	})
}

func traceChildEnv(env []string) []string {
	result := make([]string, 0, len(env)+1)
	for _, value := range env {
		if !strings.HasPrefix(value, "FEATURE_TRACE=") {
			result = append(result, value)
		}
	}
	return append(result, "FEATURE_TRACE=1")
}

func waitReady(ctx context.Context, port int) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, err := readState()
		if err == nil {
			info, healthErr := Health(ctx, state.Port)
			if healthErr == nil && matches(info, state) {
				if state.Port != port {
					return fmt.Errorf("trace receiver already runs on port %d; disable it before changing ports", state.Port)
				}
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("trace receiver did not become ready (see %s): %w", LogPath(), ctx.Err())
		case <-ticker.C:
		}
	}
}

// Run serves in the foreground. Its advisory lock belongs to the process for
// the whole receiver lifetime; a duplicate launch exits without replacing it.
// The caller supplies signal cancellation and retention policy.
func Run(ctx context.Context, port int, prune func() error) error {
	if err := validatePort(port); err != nil {
		return err
	}
	if err := ensurePrivateDir(StateDir()); err != nil {
		return fmt.Errorf("create trace state directory: %w", err)
	}
	lock := processLock()
	defer lock.Close()
	held, err := lock.TryLock()
	if err != nil {
		return fmt.Errorf("lock trace receiver: %w", err)
	}
	if !held {
		return nil
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("listen for traces on port %d: %w", port, err)
	}
	defer listener.Close()
	instance, err := randomToken()
	if err != nil {
		return err
	}
	token, err := randomToken()
	if err != nil {
		return err
	}
	state := processState{HealthInfo: HealthInfo{Service: "ox-trace", Version: version.Version, PID: os.Getpid(), InstanceID: instance}, Port: port, ShutdownToken: token}
	if err = fileutil.AtomicWriteJSON(pidPath(), state, 0600); err != nil {
		return fmt.Errorf("write trace receiver state: %w", err)
	}
	defer os.Remove(pidPath())
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	rec := receiver.New(receiver.Config{SpoolDir: SpoolDir(), Version: version.Version, Logger: slog.Default(), Prune: prune, InstanceID: instance, ShutdownToken: token, Shutdown: cancel})
	return rec.Serve(serveCtx, listener)
}

func randomToken() (string, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("create trace receiver identity: %w", err)
	}
	return hex.EncodeToString(token[:]), nil
}

// Stop shuts down only the receiver whose health identity matches its private
// pidfile. It never signals a PID or kills an unrelated process on the port.
func Stop(ctx context.Context, port int) error {
	if err := validatePort(port); err != nil {
		return err
	}
	return withLaunchLock(ctx, func(ctx context.Context) error { return stopped(ctx, false) })
}

// Purge stops capture and removes the spool while holding both launch and
// receiver locks, so an in-flight startup cannot write into the deleted spool.
func Purge(ctx context.Context, port int) error {
	if err := validatePort(port); err != nil {
		return err
	}
	return withLaunchLock(ctx, func(ctx context.Context) error { return stopped(ctx, true) })
}

func stopped(ctx context.Context, purge bool) error {
	lock := processLock()
	defer lock.Close()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	requested := false
	for {
		held, err := lock.TryLock()
		if err != nil {
			return fmt.Errorf("check trace receiver lock: %w", err)
		}
		if held {
			// Unlocked state is stale, regardless of whether its PID was recycled.
			if err = os.Remove(pidPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove trace receiver state: %w", err)
			}
			if purge {
				if err = os.RemoveAll(SpoolDir()); err != nil {
					return fmt.Errorf("purge trace spool: %w", err)
				}
			}
			return nil
		}
		if !requested {
			state, stateErr := readState()
			if stateErr == nil {
				info, healthErr := Health(ctx, state.Port)
				if healthErr == nil && matches(info, state) && state.ShutdownToken != "" {
					req, err := http.NewRequestWithContext(ctx, http.MethodPost, receiverURL(state.Port, "/shutdown"), nil)
					if err != nil {
						return err
					}
					req.Header.Set("Authorization", "Bearer "+state.ShutdownToken)
					resp, err := localClient().Do(req)
					if err != nil {
						return fmt.Errorf("stop trace receiver: %w", err)
					}
					_ = resp.Body.Close()
					if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
						return fmt.Errorf("trace receiver refused shutdown: HTTP %d", resp.StatusCode)
					}
					requested = true
				}
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("cannot verify or stop trace receiver safely: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// Existing receiver directories must remain private too. Refusing symlinks
// prevents predictable temporary paths from redirecting writes elsewhere.
func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("trace directory is not a real directory: %s", path)
	}
	return os.Chmod(path, 0700)
}

func openPrivateLog(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("trace log is not a regular file")
	}
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
}
