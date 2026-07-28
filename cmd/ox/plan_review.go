package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/plan"
	"github.com/spf13/cobra"
)

// planReviewDefaultTimeout is the idle window before a review session closes.
// Shared with `ox plan render --open <slug>`, which launches this same loop.
const planReviewDefaultTimeout = 30 * time.Minute

// plan_review.go implements `ox plan review <slug>` — the human↔agent review
// LOOP. It serves the plan on an ephemeral localhost server (this process owns
// the port — no shared-daemon ambiguity) and stays up across rounds: the human
// marks up in the browser and Submits; the agent (a separate process) addresses
// items with `ox plan feedback resolve` and re-renders with `ox plan render`;
// the server watches the ledger dir and pushes a live reload to the browser so
// the human sees the addressed state without re-running anything. The human
// closes the loop with per-item Accept/Reopen and a top-level Approve. Falls
// back to a static file + clipboard export when there is no browser
// (--no-serve / headless).
var planReviewCmd = &cobra.Command{
	Use:   "review <slug>",
	Short: "Open a live review loop for a saved plan (serve, collect, auto-reload, approve)",
	Long: `Open a live review loop for a saved plan.

Serves the plan on a short-lived localhost server (this process owns the port),
opens your browser, and stays up across rounds. Toggle Review, mark up sections /
risks / decisions, and Submit — each round is written to the ledger and a digest
is printed here for the agent. As the agent addresses items and re-renders, the
page auto-reloads so you see the updated state. Accept or Reopen individual items
inline, and click Approve to close the loop (stamps the plan approved).

With --no-serve (or in a headless shell) it writes a static HTML file and prints
the clipboard-export instructions instead.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		noServe, _ := cmd.Flags().GetBool("no-serve")
		timeout, _ := cmd.Flags().GetDuration("timeout")
		slug := ""
		if len(args) == 1 {
			slug = args[0]
		} else {
			// no slug → review an unsaved draft from --file (save it first).
			file, _ := cmd.Flags().GetString("file")
			if file == "" {
				return fmt.Errorf("pass a <slug> or --file <draft.md> to review")
			}
			s, err := reviewSaveDraft(cmd, file)
			if err != nil {
				return err
			}
			slug = s
		}
		return runPlanReview(cmd, slug, noServe, timeout)
	},
}

// reviewSaveDraft enriches + saves an unsaved plan draft to the ledger so the
// review loop has a slug + dir to work with, then returns its slug.
func reviewSaveDraft(cmd *cobra.Command, file string) (string, error) {
	in, err := plan.Resolve(file, nil)
	if err != nil {
		return "", err
	}
	if len(in.Raw) == 0 {
		return "", fmt.Errorf("draft %q is empty", file)
	}
	gitRoot := findGitRoot()
	if gitRoot == "" || !config.PlanSave(gitRoot) {
		return "", fmt.Errorf("review --file needs a ledger with plan capture enabled (run `ox init`)")
	}
	result := plan.Enrich(context.Background(), in, gitRoot)
	dir := savePlanWithProvenance(gitRoot, in, result, nil)
	if dir == "" {
		return "", fmt.Errorf("could not save draft to the ledger")
	}
	if companions := gatherCompanions(cmd, in); len(companions) > 0 {
		if names, cerr := plan.CopyCompanions(companions, dir); cerr != nil {
			cli.PrintHint("could not bundle companion(s): " + cerr.Error())
		} else if rerr := plan.RecordCompanions(dir, names); rerr != nil {
			cli.PrintHint("could not record companion(s) in plan meta: " + rerr.Error())
		}
	}
	return plan.Slugify(planTopic(in)), nil
}

func runPlanReview(cmd *cobra.Command, slug string, noServe bool, idleTimeout time.Duration) error {
	gitRoot := findGitRoot()
	planMD, res, info, err := plan.Load(gitRoot, slug)
	if err != nil {
		return fmt.Errorf("load plan %q: %w", slug, err)
	}

	if noServe || cli.IsHeadless() {
		in := plan.Parse(planMD)
		review, _ := plan.AssembleReview(info.Dir)
		return reviewStaticFallback(cmd, gitRoot, slug, info.Dir, in, res, review)
	}

	// Bind a STABLE address for this plan (persisted last port, then the
	// deterministic per-plan port). Stability is a durability feature, not a
	// nicety: the page's unsent marks live in origin-scoped localStorage, so a
	// restarted server on a fresh ephemeral port would strand them and leave the
	// old tab unable to ever reconnect. If a live server for this same plan is
	// already up, reuse it instead of racing it for the port.
	dirName := filepath.Base(info.Dir)
	state, _ := plan.LoadReviewServerState(gitRoot, dirName)
	var ln net.Listener
	for _, p := range reviewPortCandidates(state.Port, dirName) {
		l, lerr := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if lerr == nil {
			ln = l
			break
		}
		if probeReviewServer(p, dirName) {
			url := fmt.Sprintf("http://127.0.0.1:%d/", p)
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", cli.StyleBold.Render("Review loop already open:"), url)
			cli.PrintHint("Reusing the running server — feedback keeps flowing to the same session.")
			if oerr := cli.OpenInBrowser(url); oerr != nil {
				cli.PrintHint("open this URL to review: " + url)
			}
			return nil
		}
	}
	if ln == nil {
		l, lerr := net.Listen("tcp", "127.0.0.1:0")
		if lerr != nil {
			cli.PrintHint("could not start review server, falling back to file export: " + lerr.Error())
			in := plan.Parse(planMD)
			review, _ := plan.AssembleReview(info.Dir)
			return reviewStaticFallback(cmd, gitRoot, slug, info.Dir, in, res, review)
		}
		ln = l
		cli.PrintHint("Stable review ports are busy — serving on an ephemeral port; a previously open tab won't auto-reconnect to this one.")
	}
	addr := ln.Addr().String()
	// Reuse the persisted token so a tab that outlived the last server
	// authenticates against this one without a reload (same trust domain: the
	// token only gates loopback callers and the state file is 0600).
	token := state.Token
	if token == "" {
		t, terr := randomToken()
		if terr != nil {
			_ = ln.Close()
			return fmt.Errorf("review server: %w", terr)
		}
		token = t
	}
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
		if serr := plan.SaveReviewServerState(gitRoot, dirName, plan.ReviewServerState{Port: tcp.Port, Token: token}); serr != nil {
			slog.Debug("plan review: could not persist server state", "error", serr)
		}
	}
	base := "http://" + addr

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	bc := newBroadcaster()
	rounds := make(chan int, 16)
	approved := make(chan struct{}, 1)

	srv := &http.Server{Handler: liveReviewHandler(gitRoot, slug, info.Dir, base, token, bc, rounds, approved)}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Shutdown(context.Background()) }()

	// Watch the ledger plan dir so an agent's external `ox plan render` /
	// `feedback resolve` writes push a live reload to the browser.
	go watchPlanDir(ctx, info.Dir, bc)

	out := cmd.OutOrStdout()
	url := base + "/"
	fmt.Fprintf(out, "%s %s\n", cli.StyleBold.Render("Review loop open in your browser:"), url)
	cli.PrintHint("Toggle Review, mark up, Submit. The page auto-reloads as the agent addresses items. Click Approve (or Ctrl-C) to finish.")
	if err := cli.OpenInBrowser(url); err != nil {
		cli.PrintHint("open this URL to review: " + url)
	}

	idle := time.NewTimer(idleTimeout)
	defer idle.Stop()
	for {
		select {
		case n := <-rounds:
			fmt.Fprintf(out, "\n%s %d item(s).\n", cli.StyleBold.Render("Review round —"), n)
			if _, derr := printPlanReviewDigest(cmd, info.Dir); derr != nil {
				cli.PrintHint("could not read review state: " + derr.Error())
			}
			idle.Reset(idleTimeout) // idle, not total: a live session can run long
		case <-approved:
			fmt.Fprintln(out, "\n"+cli.StyleSuccess.Render("✓")+" Plan approved by reviewer.")
			return nil
		case <-idle.C:
			fmt.Fprintln(out, "\nReview session idle — closing.")
			cli.PrintHint("Everything submitted is saved in the ledger. The open page flips to disconnected mode; " +
				"unsent marks stay in the browser. Re-open anytime: `ox plan review " + slug + "` (same address — the page reconnects and restores them).")
			return nil
		case <-ctx.Done():
			fmt.Fprintln(out, "\nReview session closed.")
			cli.PrintHint("Everything submitted is saved in the ledger. The open page flips to disconnected mode; " +
				"unsent marks stay in the browser. Re-open anytime: `ox plan review " + slug + "` (same address — the page reconnects and restores them).")
			return nil
		}
	}
}

// liveReviewHandler serves the plan (re-rendered live from the ledger on every
// GET, so reloads show the latest state) and the round/accept/reopen/approve
// endpoints, plus an SSE stream that pushes reloads.
func liveReviewHandler(gitRoot, slug, planDir, base, token string, bc *broadcaster, rounds chan<- int, approved chan<- struct{}) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		html, err := renderLive(gitRoot, slug, base, token)
		if err != nil {
			http.Error(w, "render: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// no-store: offline serving is the service worker's job — the HTTP cache
		// must never mask a dead server with a stale page that still looks live.
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(html)
	})

	// /companions/<name>: bundled companion HTML artifacts, served from the plan
	// dir's companions/ subdir so the page's relative links work in the live
	// loop. Allowlisted against meta.json's Companions (never a raw directory
	// listing) and basename-only (no traversal).
	mux.HandleFunc("/companions/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/companions/")
		// "." and ".." survive the Base() identity check (filepath.Base("..")
		// == ".."); reject them explicitly so the handler stays self-defending
		// even if ServeMux's path-cleaning is ever bypassed by a wrapper.
		if name == "" || name == "." || name == ".." || name != filepath.Base(name) {
			http.NotFound(w, r)
			return
		}
		meta, err := plan.LoadMeta(planDir)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		for _, n := range meta.Companions {
			if n == name {
				w.Header().Set("Cache-Control", "no-store")
				f, err := os.Open(filepath.Join(planDir, plan.CompanionsDir, n))
				if err != nil {
					http.NotFound(w, r)
					return
				}
				defer f.Close()
				info, err := f.Stat()
				if err != nil || info.IsDir() {
					http.NotFound(w, r)
					return
				}
				http.ServeContent(w, r, n, info.ModTime(), f)
				return
			}
		}
		http.NotFound(w, r)
	})

	// /healthz: unauthenticated identity probe (loopback only). A second
	// `ox plan review` uses it to detect this server and reuse it; the page's
	// offline probe uses it to notice the server is back. Exposes nothing but
	// which plan is being served.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(reviewHealth{App: "ox-plan-review", Slug: slug, Dir: filepath.Base(planDir)})
	})

	// /sw.js: the offline shell (assets/sw.js) — keeps the plan readable on a
	// reload after this process exits; review.js registers it.
	mux.HandleFunc("/sw.js", func(w http.ResponseWriter, r *http.Request) {
		js, err := plan.ReviewServiceWorkerJS()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(js)
	})

	// SSE: EventSource can't set headers, so the token rides as a query param.
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") != token {
			http.Error(w, "bad token", http.StatusForbidden)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "stream unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		ch := bc.subscribe()
		defer bc.unsubscribe(ch)
		fmt.Fprint(w, "retry: 2000\n\n")
		flusher.Flush()
		for {
			select {
			case <-ch:
				fmt.Fprint(w, "data: reload\n\n")
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	})

	post := func(path string, fn func(body []byte) (int, error)) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "POST only", http.StatusMethodNotAllowed)
				return
			}
			if r.Header.Get("X-Review-Token") != token {
				http.Error(w, "bad token", http.StatusForbidden)
				return
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				http.Error(w, "read error", http.StatusBadRequest)
				return
			}
			code, err := fn(body)
			if err != nil {
				http.Error(w, err.Error(), code)
				return
			}
			bc.broadcast() // repaint the submitter's own tab too
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		})
	}

	post("/feedback", func(body []byte) (int, error) {
		set, err := plan.ParseFeedback(body)
		if err != nil {
			return http.StatusBadRequest, err
		}
		set.Slug = slug
		if _, err := plan.SaveFeedback(planDir, set, time.Now()); err != nil {
			return http.StatusInternalServerError, err
		}
		commitPlanBestEffort(gitRoot, planDir)
		enqueuePlanFeedbackTask(gitRoot, planDir, slug, len(set.Items))
		select {
		case rounds <- len(set.Items):
		default:
		}
		return 0, nil
	})

	post("/accept", func(body []byte) (int, error) {
		anchor, err := anchorFromBody(body)
		if err != nil {
			return http.StatusBadRequest, err
		}
		r := plan.Resolution{Anchor: anchor, State: plan.ResolutionVerified, Note: "accepted by reviewer"}
		if err := plan.AppendResolution(planDir, r, time.Now()); err != nil {
			return http.StatusInternalServerError, err
		}
		commitPlanBestEffort(gitRoot, planDir)
		return 0, nil
	})

	post("/reopen", func(body []byte) (int, error) {
		var in struct {
			Anchor string `json:"anchor"`
			Note   string `json:"note"`
		}
		if err := json.Unmarshal(body, &in); err != nil || in.Anchor == "" {
			return http.StatusBadRequest, fmt.Errorf("reopen needs an anchor")
		}
		note := in.Note
		if note == "" {
			note = "reopened by reviewer"
		}
		set := plan.FeedbackSet{Slug: slug, Items: []plan.FeedbackItem{
			{Anchor: in.Anchor, Status: plan.FeedbackRequestChange, Note: note},
		}}
		if _, err := plan.SaveFeedback(planDir, set, time.Now()); err != nil {
			return http.StatusInternalServerError, err
		}
		commitPlanBestEffort(gitRoot, planDir)
		// a reopen is a fresh open item — re-notify, so feedback isn't stranded if
		// the authoring coworker's session already ended between rounds.
		enqueuePlanFeedbackTask(gitRoot, planDir, slug, 1)
		select {
		case rounds <- 1:
		default:
		}
		return 0, nil
	})

	post("/approve", func(body []byte) (int, error) {
		// Same engine `ox plan approve` uses (internal/plan/lifecycle.go) — a
		// browser Approve click and the CLI verb are one mechanism, never two.
		// changed is only used to gate the best-effort activity notify below;
		// the response here always reports success regardless of its value —
		// an already-approved plan re-approving via a stale/duplicate click
		// must still report success, matching SetStatus's prior
		// always-succeeds contract.
		fields := planEventFieldsFromProvenance(gitRoot)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		changed, err := plan.AppendPlanEvent(ctx, planDir, plan.EventApproved, fields)
		if err != nil {
			return http.StatusInternalServerError, err
		}
		if changed {
			// Async: this handler serves a browser request, and the review
			// server stays up for the whole review session, so the goroutine
			// reliably completes — never hold the response open on a slow or
			// unreachable activity-index endpoint.
			go postPlanActivityBestEffort(gitRoot, planDir, plan.EventApproved)
		}
		commitPlanBestEffort(gitRoot, planDir)
		select {
		case approved <- struct{}{}:
		default:
		}
		return 0, nil
	})

	return mux
}

// renderLive re-renders the plan from the current ledger state, wiring the live
// endpoint + token so the page can submit, accept/reopen, approve, and subscribe
// to reloads.
func renderLive(gitRoot, slug, base, token string) ([]byte, error) {
	planMD, res, info, err := plan.Load(gitRoot, slug)
	if err != nil {
		return nil, err
	}
	review, _ := plan.AssembleReview(info.Dir)
	meta, merr := plan.LoadMeta(info.Dir)

	// HTML-primary plan: serve the AUTHORED page with the ox chrome injected
	// live (enrichment overlay + review layer wired to this server). Falls
	// through to the generated render only if the authored page is unreadable
	// (LFS pointer, missing file) — the review loop must still function.
	if merr == nil && meta.Primary == plan.PrimaryHTML {
		if path, _, isPointer, exists := plan.PlanHTMLPath(info.Dir); exists && !isPointer {
			if authored, rerr := os.ReadFile(path); rerr == nil {
				return plan.InjectChrome(authored, plan.BuildChromeData(res, plan.RenderOptions{
					Slug: slug, Review: review, ReviewEndpoint: base, ReviewToken: token,
					PriorArtURL: priorArtURLResolver(gitRoot),
				})), nil
			}
		}
		cli.PrintHint("authored plan.html unavailable (LFS pointer or unreadable) — serving the generated render of the derived markdown")
	}

	in := plan.Parse(planMD)
	var companionNames []string
	if merr == nil {
		companionNames = meta.Companions
	}
	return plan.RenderHTMLOpts(in, res, plan.RenderOptions{
		Slug: slug, Review: review, ReviewEndpoint: base, ReviewToken: token,
		PriorArtURL: priorArtURLResolver(gitRoot),
		Companions:  plan.CompanionRefs(companionNames),
	})
}

func anchorFromBody(body []byte) (string, error) {
	var in struct {
		Anchor string `json:"anchor"`
	}
	if err := json.Unmarshal(body, &in); err != nil || in.Anchor == "" {
		return "", fmt.Errorf("missing anchor")
	}
	return in.Anchor, nil
}

func commitPlanBestEffort(gitRoot, planDir string) {
	if err := commitPlanToLedger(gitRoot, planDir); err != nil {
		// non-fatal: the round/resolution is saved locally and the next push /
		// `ox doctor` reconciles it — but log loudly so the deferred-commit state
		// isn't silent (a reviewer/agent shouldn't assume Git already has it).
		slog.Warn("plan review: feedback saved locally, ledger commit deferred", "error", err, "dir", planDir)
	}
}

// watchPlanDir broadcasts a reload whenever the plan dir (or its feedback/
// subdir) changes — so an agent's external render/resolve updates the live page.
func watchPlanDir(ctx context.Context, planDir string, bc *broadcaster) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return
	}
	defer w.Close()
	_ = w.Add(planDir)
	_ = w.Add(filepath.Join(planDir, "feedback")) // may not exist yet; ignore error
	var debounce *time.Timer
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-w.Events:
			if !ok {
				return
			}
			// coalesce bursts (a render writes several files) into one reload
			if debounce != nil {
				debounce.Stop()
			}
			debounce = time.AfterFunc(250*time.Millisecond, bc.broadcast)
		case _, ok := <-w.Errors:
			if !ok {
				return
			}
		}
	}
}

// broadcaster fans a reload signal out to all connected SSE clients.
type broadcaster struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

func newBroadcaster() *broadcaster { return &broadcaster{subs: map[chan struct{}]struct{}{}} }

func (b *broadcaster) subscribe() chan struct{} {
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *broadcaster) unsubscribe(ch chan struct{}) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
}

func (b *broadcaster) broadcast() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- struct{}{}:
		default: // a pending reload is already queued for this client
		}
	}
}

// reviewStaticFallback renders to a file and prints the clipboard-export path for
// environments with no browser/server.
func reviewStaticFallback(cmd *cobra.Command, gitRoot, slug, planDir string, in plan.Input, res plan.Result, review []plan.MergedItem) error {
	companions := savedCompanionFiles(planDir)
	var companionNames []string
	for _, c := range companions {
		companionNames = append(companionNames, c.Name)
	}
	html, err := plan.RenderHTMLOpts(in, res, plan.RenderOptions{Slug: slug, Review: review, PriorArtURL: priorArtURLResolver(gitRoot), Companions: plan.CompanionRefs(companionNames)})
	if err != nil {
		return fmt.Errorf("render plan: %w", err)
	}
	// sanitize: slug can come from a CLI arg; never let it escape TempDir.
	safeSlug := strings.NewReplacer("/", "_", `\`, "_", "..", "_").Replace(slug)
	path := filepath.Join(os.TempDir(), safeSlug+"-review.html")
	if err := os.WriteFile(path, html, 0o644); err != nil {
		return fmt.Errorf("write render: %w", err)
	}
	if len(companions) > 0 {
		if _, cerr := plan.CopyCompanions(companions, os.TempDir()); cerr != nil {
			cli.PrintHint("could not place companion(s) next to the render: " + cerr.Error())
		}
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Rendered review page: %s\n", cli.StyleFile.Render(path))
	cli.PrintHint("Open it, toggle Review, mark up, click Export, then: ox plan feedback apply " + slug + " --from <file>")
	if !cli.IsHeadless() {
		_ = cli.OpenInBrowser(path)
	}
	return nil
}

// reviewHealth is the /healthz body — enough for a second `ox plan review`
// (and the page's reconnect probe) to recognize a live server for a plan.
type reviewHealth struct {
	App  string `json:"app"`
	Slug string `json:"slug"`
	Dir  string `json:"dir"`
}

// reviewPortCandidates orders the ports to try binding: the last port this
// plan actually served on (persisted state — the origin an open tab is parked
// on), then the plan's deterministic stable port and a few slots after it for
// same-machine collisions. Deduped, order-preserving.
func reviewPortCandidates(persisted int, dirName string) []int {
	stable := plan.StableReviewPort(dirName)
	raw := []int{persisted, stable, stable + 1, stable + 2, stable + 3, stable + 4}
	seen := map[int]bool{}
	var out []int
	for _, p := range raw {
		if p <= 0 || p > 65535 || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// probeReviewServer reports whether a live ox review server for THIS plan is
// already listening on port — so a second `ox plan review` reuses it instead
// of racing it for the port or silently serving a twin.
func probeReviewServer(port int, dirName string) bool {
	c := &http.Client{Timeout: 700 * time.Millisecond}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var h reviewHealth
	if jerr := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&h); jerr != nil {
		return false
	}
	return h.App == "ox-plan-review" && h.Dir == dirName
}

// randomToken returns a fresh 128-bit hex token, or an error. It FAILS CLOSED —
// no static fallback — so the server never starts with a guessable token in the
// exact failure mode where entropy is unavailable.
func randomToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate review token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func init() {
	planReviewCmd.Flags().String("file", "", "review an unsaved draft markdown (saved to the ledger first); used when no <slug> is given")
	planReviewCmd.Flags().Bool("no-serve", false, "render a static file + clipboard export instead of serving")
	planReviewCmd.Flags().Duration("timeout", planReviewDefaultTimeout, "idle timeout — closes the session after this long with no activity")
	planCmd.AddCommand(planReviewCmd)
}
