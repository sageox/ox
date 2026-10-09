package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/sageox/ox/internal/cli"
)

// These assets never load remote scripts, fonts, or images. Session content is
// fetched as JSON and inserted with textContent, not interpreted as markup.
//
//go:embed session_import_browser.html
var importBrowserHTML []byte

//go:embed session_import_browser.css
var importBrowserCSS []byte

//go:embed session_import_browser.js
var importBrowserJS []byte

type importBrowserRow struct {
	NativeID     string `json:"native_id"`
	Agent        string `json:"agent"`
	StartedAt    string `json:"started_at"`
	LastActivity string `json:"last_activity"`
	Messages     int    `json:"messages"`
	SizeBytes    int64  `json:"size_bytes"`
	State        string `json:"state"`
	Reason       string `json:"reason,omitempty"`
	Selected     bool   `json:"selected"`
}

type importBrowserList struct {
	Team       string             `json:"team"`
	RepoID     string             `json:"repo_id"`
	Visibility string             `json:"visibility"`
	Sessions   []importBrowserRow `json:"sessions"`
}

// importBrowser is an ephemeral reader and selection bridge. It has no
// summarizer, Ledger writer, upload client, or filesystem path endpoint.
type importBrowser struct {
	host    string
	origin  string
	token   string
	list    importBrowserList
	allowed map[string]bool // known IDs; value reports eligibility for selection
	load    importPreviewLoader
	result  chan importReviewResult
	mu      sync.Mutex
	done    bool
}

func runImportBrowser(ctx context.Context, dest importDestination, cands []*importCandidate, load importPreviewLoader) (importReviewResult, error) {
	return runImportBrowserWithOpen(ctx, dest, cands, load, cli.OpenInBrowser)
}

// runImportBrowserWithOpen keeps browser launching injectable without global
// test overrides. A callback returns selection only; upload confirmation stays
// in the terminal after this server shuts down.
func runImportBrowserWithOpen(ctx context.Context, dest importDestination, cands []*importCandidate, load importPreviewLoader, open func(string) error) (importReviewResult, error) {
	var zero importReviewResult
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return zero, fmt.Errorf("start local session browser: %w", err)
	}
	return serveImportBrowser(ctx, ln, dest, cands, load, open)
}

// serveImportBrowser owns the supplied loopback listener for the entire review,
// including startup failures and interrupted requests. Keeping listener ownership
// here lets the lifecycle be exercised with actual socket closure.
func serveImportBrowser(ctx context.Context, ln net.Listener, dest importDestination, cands []*importCandidate, load importPreviewLoader, open func(string) error) (importReviewResult, error) {
	var zero importReviewResult
	defer ln.Close()
	secret := make([]byte, 32)
	// Go 1.26+ crypto/rand.Read fills the entire buffer or terminates the
	// process if the operating system cannot supply secure randomness.
	// It cannot return an error; this capability retains 256 bits of entropy.
	_, _ = rand.Read(secret)
	b, err := newImportBrowser(ln.Addr().String(), hex.EncodeToString(secret), dest, cands, load)
	if err != nil {
		return zero, err
	}
	srv := &http.Server{
		Handler: b.handler(), ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout: 30 * time.Second, WriteTimeout: 3 * time.Minute,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			_ = srv.Close()
		}
	}()
	// A fragment is not sent in HTTP requests or Referer headers. The page
	// supplies the capability as a custom header only to this same origin.
	if err := open(b.origin + "/#token=" + b.token); err != nil {
		return zero, fmt.Errorf("open local session browser: %w", err)
	}
	select {
	case result := <-b.result:
		return result, nil
	case <-ctx.Done():
		return zero, ctx.Err()
	case err := <-served:
		// Serve always returns an error. The owned server is shut down only
		// after this wait, so any exit here interrupted an unfinished review.
		return zero, fmt.Errorf("local session browser stopped: %w", err)
	}
}

// newImportBrowser snapshots structural metadata and selection eligibility for
// known IDs. It rejects duplicate IDs and omits native-file paths from the catalog.
func newImportBrowser(host, token string, dest importDestination, cands []*importCandidate, load importPreviewLoader) (*importBrowser, error) {
	if token == "" || load == nil {
		return nil, errors.New("session browser requires a preview reader and a token")
	}
	b := &importBrowser{
		host: host, origin: "http://" + host, token: token, load: load,
		allowed: make(map[string]bool, len(cands)), result: make(chan importReviewResult, 1),
		list: importBrowserList{Team: dest.Team, RepoID: dest.RepoID, Visibility: dest.Visibility, Sessions: make([]importBrowserRow, 0, len(cands))},
	}
	for _, c := range cands {
		id := c.Session.NativeID
		if _, exists := b.allowed[id]; exists {
			return nil, errors.New("multiple sessions share a native session ID; narrow the session filter")
		}
		b.allowed[id] = c.State == stateReady
		row := importBrowserRow{
			NativeID: id, Agent: string(c.Session.Agent), Messages: c.Session.Messages(),
			SizeBytes: c.Session.Size, State: string(c.State), Reason: importBrowserReason(c.State), Selected: c.Selected && c.State == stateReady,
		}
		if !c.Session.StartedAt.IsZero() {
			row.StartedAt = c.Session.StartedAt.Format(time.RFC3339)
		}
		if !c.Session.LastActivity.IsZero() {
			row.LastActivity = c.Session.LastActivity.Format(time.RFC3339)
		}
		b.list.Sessions = append(b.list.Sessions, row)
	}
	return b, nil
}

// Native parser errors and classification details can contain local paths.
// Explain eligibility with controlled copy rather than returning those strings.
func importBrowserReason(state importState) string {
	switch state {
	case stateAlreadyImported:
		return "This session was already imported. It will not be uploaded again."
	case stateRecordedLive:
		return "ox already recorded this session in the Ledger."
	case stateInProgress:
		return "This session may still be running or was changed recently. Finish it, then retry."
	case stateNeedsSummarizer:
		return "A summarizer must be installed and logged in before this session can be imported."
	case stateNotShared:
		return "An earlier import review kept this session local."
	case stateIneligible:
		return "This session does not meet the import requirements."
	default:
		return ""
	}
}

// handler serves local assets, known-session previews, and selection callbacks.
// Every route receives host and privacy protections; API routes also require
// capability authorization. These routes cannot summarize or publish sessions.
func (b *importBrowser) handler() http.Handler {
	mux := http.NewServeMux()
	asset := func(path, kind string, content []byte) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != path {
				http.NotFound(w, r)
				return
			}
			if !importBrowserMethod(w, r, http.MethodGet) {
				return
			}
			w.Header().Set("Content-Type", kind+"; charset=utf-8")
			_, _ = w.Write(content)
		})
	}
	asset("/", "text/html", importBrowserHTML)
	asset("/app.css", "text/css", importBrowserCSS)
	asset("/app.js", "text/javascript", importBrowserJS)
	mux.HandleFunc("/api/sessions", func(w http.ResponseWriter, r *http.Request) {
		if !b.authorize(w, r, http.MethodGet) {
			return
		}
		importBrowserJSON(w, http.StatusOK, b.list)
	})
	mux.HandleFunc("/api/preview", func(w http.ResponseWriter, r *http.Request) {
		if !b.authorize(w, r, http.MethodGet) {
			return
		}
		id := r.URL.Query().Get("id")
		if _, known := b.allowed[id]; !known {
			importBrowserError(w, http.StatusNotFound, "Session is not in this review.")
			return
		}
		preview, err := b.load(r.Context(), id)
		if errors.Is(err, errImportSourceChanged) {
			importBrowserError(w, http.StatusConflict, "This session changed since you started reviewing it. Return to the terminal and restart the review.")
			return
		}
		if err != nil || preview == nil || preview.NativeID != id {
			importBrowserError(w, http.StatusConflict, "This session changed or could not be previewed. Return to the terminal and refresh the list.")
			return
		}
		if r.URL.Query().Get("excerpt") == "1" {
			opening := *preview
			opening.Prompts, opening.Entries, opening.LastReply = nil, nil, ""
			importBrowserJSON(w, http.StatusOK, &opening)
			return
		}
		importBrowserJSON(w, http.StatusOK, preview)
	})
	mux.HandleFunc("/api/selection", func(w http.ResponseWriter, r *http.Request) {
		if !b.authorize(w, r, http.MethodPost) {
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			importBrowserError(w, http.StatusUnsupportedMediaType, "Use a JSON selection.")
			return
		}
		var body struct {
			IDs []string `json:"ids"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			importBrowserError(w, http.StatusBadRequest, "Invalid selection.")
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			importBrowserError(w, http.StatusBadRequest, "Invalid selection.")
			return
		}
		seen := make(map[string]bool, len(body.IDs))
		for _, id := range body.IDs {
			ready, known := b.allowed[id]
			if !known || !ready || seen[id] {
				importBrowserError(w, http.StatusBadRequest, "Choose each ready session at most once.")
				return
			}
			seen[id] = true
		}
		b.finish(w, importReviewResult{IDs: body.IDs})
	})
	mux.HandleFunc("/api/cancel", func(w http.ResponseWriter, r *http.Request) {
		if b.authorize(w, r, http.MethodPost) {
			b.finish(w, importReviewResult{Canceled: true})
		}
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		if r.Host != b.host {
			importBrowserError(w, http.StatusForbidden, "Invalid browser host.")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// authorize checks the route method and capability token. POST requires the
// exact review origin; any supplied foreign Origin is rejected on other methods.
func (b *importBrowser) authorize(w http.ResponseWriter, r *http.Request, method string) bool {
	if !importBrowserMethod(w, r, method) {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Import-Token")), []byte(b.token)) != 1 {
		importBrowserError(w, http.StatusForbidden, "Invalid browser token.")
		return false
	}
	origin := r.Header.Get("Origin")
	if (method == http.MethodPost && origin != b.origin) || (origin != "" && origin != b.origin) {
		importBrowserError(w, http.StatusForbidden, "Invalid browser origin.")
		return false
	}
	return true
}

// finish returns the first accepted selection or cancellation to the terminal.
// Concurrent or repeated callbacks receive a conflict and cannot replace it.
func (b *importBrowser) finish(w http.ResponseWriter, result importReviewResult) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done {
		importBrowserError(w, http.StatusConflict, "Selection already returned to the terminal.")
		return
	}
	b.done = true
	importBrowserJSON(w, http.StatusOK, map[string]bool{"ok": true})
	b.result <- result
}

func importBrowserMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	importBrowserError(w, http.StatusMethodNotAllowed, "Unsupported request method.")
	return false
}

func importBrowserError(w http.ResponseWriter, status int, message string) {
	importBrowserJSON(w, status, map[string]string{"error": message})
}

func importBrowserJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
