package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/plan"
	"github.com/spf13/cobra"
)

// plan_review.go implements `ox plan review <slug>` — the agent-driven review
// loop entry. It renders the plan, starts an EPHEMERAL localhost server (the
// agent spawns it and owns the port — no shared-daemon ambiguity), opens the
// browser, and BLOCKS until the human submits marks (or a timeout / Ctrl-C).
// The submit POSTs to this one-shot server, which writes the round to the
// ledger and prints the digest to stdout, so the agent ingests feedback inline —
// no file handoff, no second command. Falls back to a static file render +
// clipboard export when there is no browser (--no-serve / headless).
var planReviewCmd = &cobra.Command{
	Use:   "review <slug>",
	Short: "Render a saved plan, collect human review in the browser, and print the feedback",
	Long: `Render a saved plan and collect a round of human review.

Starts a short-lived localhost server (this process owns the port), opens the
plan in your browser, and waits while you toggle Review and mark up sections /
risks / decisions. On Submit, the marks are written to the plan's ledger dir and
a digest is printed here for the agent to act on. Resolve items with
` + "`ox plan feedback resolve`" + `, then re-run to verify.

With --no-serve (or in a headless shell) it writes a static HTML file and prints
the clipboard-export instructions instead.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		noServe, _ := cmd.Flags().GetBool("no-serve")
		timeout, _ := cmd.Flags().GetDuration("timeout")
		return runPlanReview(cmd, args[0], noServe, timeout)
	},
}

func runPlanReview(cmd *cobra.Command, slug string, noServe bool, timeout time.Duration) error {
	gitRoot := findGitRoot()
	planMD, res, info, err := plan.Load(gitRoot, slug)
	if err != nil {
		return fmt.Errorf("load plan %q: %w", slug, err)
	}
	in := plan.Parse(planMD)
	review, _ := plan.AssembleReview(info.Dir)

	if noServe || cli.IsHeadless() {
		return reviewStaticFallback(cmd, slug, in, res, review)
	}

	// Listen first so we know the port, then render with the live endpoint baked in.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cli.PrintHint("could not start review server, falling back to file export: " + err.Error())
		return reviewStaticFallback(cmd, slug, in, res, review)
	}
	addr := ln.Addr().String()
	token := randomToken()
	endpoint := fmt.Sprintf("http://%s/feedback", addr)

	html, err := plan.RenderHTMLOpts(in, res, plan.RenderOptions{
		Slug: slug, Review: review, ReviewEndpoint: endpoint, ReviewToken: token,
	})
	if err != nil {
		_ = ln.Close()
		return fmt.Errorf("render plan: %w", err)
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	done := make(chan plan.FeedbackSet, 1)
	srv := &http.Server{Handler: reviewHandler(html, token, slug, gitRoot, info.Dir, done)}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Shutdown(context.Background()) }()

	url := fmt.Sprintf("http://%s/", addr)
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "%s %s\n", cli.StyleBold.Render("Review open in your browser:"), url)
	cli.PrintHint("Mark up sections/risks in the tab, then click Submit. Ctrl-C here to cancel.")
	if err := cli.OpenInBrowser(url); err != nil {
		cli.PrintHint("open this URL to review: " + url)
	}

	// Heartbeat so the wait isn't silent (the command blocks until the human
	// submits). Stops as soon as the select below returns.
	stopHB := make(chan struct{})
	defer close(stopHB)
	go reviewHeartbeat(out, stopHB)

	select {
	case set := <-done:
		fmt.Fprintf(out, "\nReceived %d review item(s).\n\n", len(set.Items))
		printPlanReviewDigest(cmd, info.Dir)
		return nil
	case <-ctx.Done():
		fmt.Fprintln(out, "\nNo review submitted (timed out or interrupted).")
		cli.PrintHint("For an unattended/headless flow use `ox plan review " + slug + " --no-serve` (static file + clipboard).")
		return nil
	}
}

// reviewHeartbeat prints a periodic "still waiting" line so a long block reads as
// alive, not hung. Quiet under NO_COLOR/headless via the dim style.
func reviewHeartbeat(out io.Writer, stop <-chan struct{}) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	start := time.Now()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			fmt.Fprint(out, cli.StyleDim.Render(fmt.Sprintf("  …waiting for review (%ds) — submit in the browser tab\n", int(time.Since(start).Seconds()))))
		}
	}
}

// reviewHandler serves the rendered plan on GET / and accepts one round of marks
// on POST /feedback (token-gated). The first valid submit is written to the
// ledger and signaled on done.
func reviewHandler(html []byte, token, slug, gitRoot, planDir string, done chan<- plan.FeedbackSet) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(html)
	})
	mux.HandleFunc("/feedback", func(w http.ResponseWriter, r *http.Request) {
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
		set, err := plan.ParseFeedback(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		set.Slug = slug
		if _, err := plan.SaveFeedback(planDir, set, time.Now()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if cerr := commitPlanToLedger(gitRoot, planDir); cerr != nil {
			// non-fatal: the round is saved locally; commit can be retried
			_ = cerr
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
		select {
		case done <- set:
		default:
		}
	})
	return mux
}

// reviewStaticFallback renders to a file and prints the clipboard-export path for
// environments with no browser/server.
func reviewStaticFallback(cmd *cobra.Command, slug string, in plan.Input, res plan.Result, review []plan.MergedItem) error {
	html, err := plan.RenderHTMLOpts(in, res, plan.RenderOptions{Slug: slug, Review: review})
	if err != nil {
		return fmt.Errorf("render plan: %w", err)
	}
	path := fmt.Sprintf("%s/%s-review.html", os.TempDir(), slug)
	if err := os.WriteFile(path, html, 0o644); err != nil {
		return fmt.Errorf("write render: %w", err)
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Rendered review page: %s\n", cli.StyleFile.Render(path))
	cli.PrintHint("Open it, toggle Review, mark up, click Export, then: ox plan feedback apply " + slug + " --from <file>")
	if !cli.IsHeadless() {
		_ = cli.OpenInBrowser(path)
	}
	return nil
}

func randomToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "ox-review-token"
	}
	return hex.EncodeToString(b)
}

func init() {
	planReviewCmd.Flags().Bool("no-serve", false, "render a static file + clipboard export instead of serving")
	planReviewCmd.Flags().Duration("timeout", 30*time.Minute, "how long to wait for a submit before giving up")
	planCmd.AddCommand(planReviewCmd)
}
