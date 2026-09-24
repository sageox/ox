// Package receiver captures local OTLP/HTTP JSON exports without a collector or
// network dependency. Payloads remain local, separated by native session UUID.
package receiver

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

const MaxBodyBytes = 8 << 20

// Config receives all storage paths from the application's canonical helpers.
type Config struct {
	SpoolDir      string
	Version       string
	Logger        *slog.Logger
	IdleTimeout   time.Duration
	Prune         func() error
	InstanceID    string
	ShutdownToken string
	Shutdown      func()
}

type Receiver struct {
	config       Config
	handler      http.Handler
	lastActivity atomic.Int64
}

func New(config Config) *Receiver {
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.IdleTimeout <= 0 {
		config.IdleTimeout = 2 * time.Hour
	}
	r := &Receiver{config: config}
	r.lastActivity.Store(time.Now().UnixNano())
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", r.health)
	mux.HandleFunc("POST /v1/traces", func(w http.ResponseWriter, req *http.Request) { r.ingest(w, req, "traces") })
	mux.HandleFunc("POST /v1/logs", func(w http.ResponseWriter, req *http.Request) { r.ingest(w, req, "logs") })
	mux.HandleFunc("POST /shutdown", r.shutdown)
	r.handler = mux
	return r
}

func (r *Receiver) Handler() http.Handler { return r.handler }

func (r *Receiver) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Service    string `json:"service"`
		Version    string `json:"version"`
		PID        int    `json:"pid"`
		InstanceID string `json:"instance_id"`
	}{"ox-trace", r.config.Version, os.Getpid(), r.config.InstanceID})
}

func (r *Receiver) shutdown(w http.ResponseWriter, req *http.Request) {
	token, bearer := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
	if !bearer || req.Header.Get("Origin") != "" || r.config.ShutdownToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(r.config.ShutdownToken)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.config.Shutdown == nil {
		http.Error(w, "shutdown unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	r.config.Shutdown()
}

func (r *Receiver) reject(w http.ResponseWriter, status int, reason string) {
	r.config.Logger.Warn("trace request rejected", "status", status, "reason", reason)
	http.Error(w, http.StatusText(status), status)
}

func (r *Receiver) ingest(w http.ResponseWriter, req *http.Request, signal string) {
	if req.Header.Get("Origin") != "" {
		r.reject(w, http.StatusForbidden, "cross-origin request")
		return
	}
	media, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		r.reject(w, http.StatusUnsupportedMediaType, "expected application/json")
		return
	}
	encoding := strings.ToLower(strings.TrimSpace(req.Header.Get("Content-Encoding")))
	if encoding != "" && encoding != "identity" && encoding != "gzip" {
		r.reject(w, http.StatusUnsupportedMediaType, "unsupported content encoding")
		return
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, MaxBodyBytes+1))
	if err != nil {
		r.reject(w, http.StatusBadRequest, "read body")
		return
	}
	if len(body) > MaxBodyBytes {
		r.reject(w, http.StatusRequestEntityTooLarge, "wire body limit")
		return
	}
	if encoding == "gzip" {
		reader, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			r.reject(w, http.StatusBadRequest, "invalid gzip body")
			return
		}
		body, err = io.ReadAll(io.LimitReader(reader, MaxBodyBytes+1))
		_ = reader.Close()
		if len(body) > MaxBodyBytes {
			r.reject(w, http.StatusRequestEntityTooLarge, "decompressed body limit")
			return
		}
		if err != nil {
			r.reject(w, http.StatusBadRequest, "invalid gzip body")
			return
		}
	}
	parts, err := demux(body, signal)
	if err != nil {
		r.reject(w, http.StatusBadRequest, "invalid OTLP JSON")
		return
	}
	now := time.Now().UTC()
	if err := store(r.config.SpoolDir, signal, uuid.NewString(), parts, now); err != nil {
		r.config.Logger.Error("trace append failed", "signal", signal, "error", err)
		http.Error(w, "local trace storage unavailable", http.StatusInternalServerError)
		return
	}
	r.lastActivity.Store(now.UnixNano())
	r.config.Logger.Info("trace request captured", "signal", signal, "sessions", len(parts), "bytes", len(body))
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte("{}"))
}

func (r *Receiver) prune() {
	if r.config.Prune != nil {
		if err := r.config.Prune(); err != nil {
			r.config.Logger.Warn("trace prune failed", "error", err)
		}
	}
}

// Serve exits cleanly on cancellation or idle timeout. Health probes never keep
// it alive. Shutdown drains in-flight requests before releasing the listener.
func (r *Receiver) Serve(ctx context.Context, listener net.Listener) error {
	r.lastActivity.Store(time.Now().UnixNano())
	server := &http.Server{Handler: r.handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	// Retention may inspect many unfinished recordings. It must not delay
	// health readiness or the session hook that starts this receiver. A single
	// worker prevents overlapping startup/daily scans. The callback has no
	// cancellation contract, so shutdown does not wait on an in-progress scan.
	stopPruning := make(chan struct{})
	defer close(stopPruning)
	go r.prunePeriodically(stopPruning)
	idle := time.NewTimer(r.config.IdleTimeout)
	defer idle.Stop()
	for {
		select {
		case err := <-done:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-idle.C:
			elapsed := time.Since(time.Unix(0, r.lastActivity.Load()))
			if elapsed < r.config.IdleTimeout {
				idle.Reset(r.config.IdleTimeout - elapsed)
				continue
			}
			return stopServer(server, done)
		case <-ctx.Done():
			return stopServer(server, done)
		}
	}
}

func (r *Receiver) prunePeriodically(stop <-chan struct{}) {
	daily := time.NewTicker(24 * time.Hour)
	defer daily.Stop()
	// Prune FIRST, then wait. The startup scan is not conditional on stop.
	//
	// Serve closes stop from a defer, so a receiver that exits quickly — an idle
	// timeout, or a canceled context — can close it before this goroutine is ever
	// scheduled. Checking stop before the first prune made that ordering skip the
	// startup retention scan entirely: the one scan this worker exists to
	// guarantee, lost to whether the scheduler got here in time.
	//
	// It costs shutdown nothing. Serve never waits on this goroutine (the prune
	// callback has no cancellation contract, as the caller documents), so the scan
	// is already detached; running it first makes the guarantee real instead of
	// probabilistic.
	//
	// One call site, deliberately: a second `r.prune()` after the select would be
	// reachable only once the 24-hour ticker fires, so no test could ever cover it.
	for {
		r.prune()
		select {
		case <-stop:
			return
		case <-daily.C:
		}
	}
}

func stopServer(server *http.Server, done <-chan error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := server.Shutdown(ctx)
	if err != nil {
		_ = server.Close()
	}
	serveErr := <-done
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	return errors.Join(err, serveErr)
}
