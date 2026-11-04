//go:build agents

package agents_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const (
	fileBackend      = "server/main.go"
	fileShopSettings = "site/app/(shop)/settings/page.tsx"
	fileShopHome     = "site/app/(shop)/home/page.tsx"
	fileShopStyles   = "site/app/(shop)/home/page.css"
	fileOrderDetail  = "site/app/(account)/orders/[order_id]/page.tsx"
	fileKiosk        = "site/app/kiosk/layout.tsx"
)

// scopingRules is a team's usual shape: flat rules with repos: and globs:, plus a
// control scoped to its own file, which must fire in every session.
func scopingRules(t *testing.T) (pages, kiosk, control teamRuleFixture) {
	t.Helper()
	pages = teamRuleFixture{
		name: "scoped-pages", marker: uniqueMarker(t, "PAGES"),
		globs: []string{"site/app/(shop)/**/*.{tsx,css}", "site/app/(account)/**/*.{tsx,css}"},
	}
	pages.body = scopedRuleBody("Pages", pages.marker)
	kiosk = teamRuleFixture{
		name: "scoped-kiosk", marker: uniqueMarker(t, "KIOSK"),
		globs: []string{"site/app/kiosk/**"},
	}
	kiosk.body = scopedRuleBody("Kiosk", kiosk.marker)
	control = teamRuleFixture{
		name: "control-beacon", marker: uniqueMarker(t, "CONTROL"),
		globs: []string{"control/**"},
	}
	control.body = "Control rule. " + control.marker + "\n"
	return pages, kiosk, control
}

// requireControl makes a run that cannot see rules INVALID. Every rule is absent from a
// transcript nothing can read, so "absent" only counts next to something present.
func requireControl(t *testing.T, transcript claudeTranscript, control teamRuleFixture) {
	t.Helper()
	if got := transcript.attachmentsHolding(control.marker); len(got) != 1 {
		invalidRun(t, "the control rule loaded %d times, want once; this run measured nothing", len(got))
	}
}

// TestTeamRuleScoping_Claude: a scoped rule costs a session on unrelated files nothing,
// and a matching read loads its exact bytes once.
//
// Everything here is real: ox binary, daemon pull, Reconcile, git ignore stanza, Claude
// Code, transcript. Nothing about Claude's matcher or loader is modeled.
func TestTeamRuleScoping_Claude(t *testing.T) {
	claude := requireLiveAgent(t, "claude")
	t.Logf("agent under test: %s", toolVersion(t, claude))
	ox := headOxBinary(t)

	pages, kiosk, control := scopingRules(t)
	fx := newScopingFixture(t, newOxEnvironment(t), []teamRuleFixture{pages, kiosk, control},
		[]string{fileBackend, fileShopSettings, fileShopHome, fileShopStyles, fileOrderDetail, fileKiosk})
	fx.syncWith(t, ox)

	projections := fx.projections(t)
	if len(projections) != 3 {
		t.Fatalf("ox sync projected %d rules, want 3: %v", len(projections), keysOf(projections))
	}
	for name := range projections {
		ignored := exec.Command("git", "check-ignore", "-q", ".claude/rules/"+name)
		ignored.Dir = fx.repo
		if err := ignored.Run(); err != nil {
			t.Fatalf("projection %s is not ignored by git, so it would land in a pull request: %v", name, err)
		}
	}

	t.Run("backend read loads no scoped bytes", func(t *testing.T) {
		t.Parallel()
		transcript := claudeSession(t, claude, fx.repo, fileBackend, fx.controlRel)
		requireControl(t, transcript, control)
		for _, rule := range []teamRuleFixture{pages, kiosk} {
			if bytes.Contains(transcript.raw, []byte(rule.marker)) {
				t.Errorf("scoped rule %s loaded on a backend read: its marker is in the transcript", rule.name)
			}
		}
	})

	t.Run("matching read loads exact bytes once", func(t *testing.T) {
		t.Parallel()
		// Two matching reads: the second must not inject the rule again.
		transcript := claudeSession(t, claude, fx.repo, fileShopSettings, fileShopHome, fx.controlRel)
		requireControl(t, transcript, control)
		loaded := transcript.attachmentsHolding(pages.marker)
		if len(loaded) != 1 {
			t.Fatalf("scoped-pages loaded %d times on matching reads, want exactly once", len(loaded))
		}
		if loaded[0].atStart {
			t.Errorf("scoped-pages arrived at session start, not on the matching read")
		}
		if got, want := strings.TrimRight(loaded[0].text, "\n"), claudeVisibleBody(pages.body); got != want {
			t.Errorf("scoped-pages bytes differ from the source body:\n got %q\nwant %q", got, want)
		}
		if bytes.Contains(transcript.raw, []byte(kiosk.marker)) {
			t.Errorf("a pages read loaded scoped-kiosk")
		}
	})

	t.Run("brace group matches a css read", func(t *testing.T) {
		t.Parallel()
		transcript := claudeSession(t, claude, fx.repo, fileShopStyles, fx.controlRel)
		requireControl(t, transcript, control)
		if got := transcript.attachmentsHolding(pages.marker); len(got) != 1 {
			t.Errorf("scoped-pages loaded %d times on a .css read, want once: the brace group did not match", len(got))
		}
	})

	t.Run("a bracketed directory is matched by a wildcard", func(t *testing.T) {
		t.Parallel()
		transcript := claudeSession(t, claude, fx.repo, fileOrderDetail, fx.controlRel)
		requireControl(t, transcript, control)
		if got := transcript.attachmentsHolding(pages.marker); len(got) != 1 {
			t.Errorf("scoped-pages loaded %d times under a [order_id] directory, want once", len(got))
		}
	})

	t.Run("a kiosk read does not load pages", func(t *testing.T) {
		t.Parallel()
		transcript := claudeSession(t, claude, fx.repo, fileKiosk, fx.controlRel)
		requireControl(t, transcript, control)
		if got := transcript.attachmentsHolding(kiosk.marker); len(got) != 1 {
			t.Errorf("scoped-kiosk loaded %d times on a kiosk read, want once", len(got))
		}
		if bytes.Contains(transcript.raw, []byte(pages.marker)) {
			t.Errorf("a kiosk read loaded scoped-pages")
		}
	})
}

func keysOf[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}

// TestTeamRuleScoping_Codex: Codex has no path-scoped rules, so a scoped rule must cost
// it an index row in prime and nothing more, and the row must lead to the body. The body
// marker's absence only counts next to a control proving what Codex was given is
// visible in the transcript.
func TestTeamRuleScoping_Codex(t *testing.T) {
	codex := requireLiveAgent(t, "codex")
	t.Logf("agent under test: %s", toolVersion(t, codex))
	ox := headOxBinary(t)

	pages, kiosk, control := scopingRules(t)
	// visibility: always must not beat globs: Codex would pay this body in every session.
	always := teamRuleFixture{
		name: "scoped-always", marker: uniqueMarker(t, "ALWAYS"), visibility: "always",
		globs: []string{"site/**/*.tsx"},
	}
	always.body = scopedRuleBody("Always", always.marker)
	fx := newScopingFixture(t, newOxEnvironment(t), []teamRuleFixture{pages, kiosk, always, control}, []string{fileBackend, fileShopHome})
	fx.syncWith(t, ox)

	agentsMarker := uniqueMarker(t, "AGENTS")
	writeFixtureFile(t, fx.repo, "AGENTS.md", "# Fixture\n\nCodeword for this repository: "+agentsMarker+"\n")
	hook := fx.codexPrimeHook(t, ox)
	t.Cleanup(func() { fx.env.stopDaemon(t, fx.repo, ox) }) // the hook's own ox started one

	first := codexSession(t, codex, fx.repo, hook, "Reply with exactly the word DONE. Do not run commands or read files.")
	if !strings.Contains(first, agentsMarker) {
		invalidRun(t, "AGENTS.md never reached the transcript, so this reader sees nothing Codex was given")
	}
	if !strings.Contains(first, "<team-rules") {
		invalidRun(t, "the SessionStart hook delivered no prime, so there is no index to measure")
	}
	row := teamRuleRow(first, pages.name)
	if row == "" {
		t.Fatalf("scoped-pages has no index row in what Codex received")
	}
	if teamRuleRow(first, always.name) == "" {
		t.Errorf("scoped-always has no index row in what Codex received")
	}
	for _, rule := range []teamRuleFixture{pages, kiosk, always} {
		if strings.Contains(first, rule.marker) {
			t.Errorf("Codex received the body of %s without reading it", rule.name)
		}
	}

	path := lastTableCell(row)
	second := codexSession(t, codex, fx.repo, hook,
		"Read the whole file at "+path+" and reply with the line in it that starts with MARKER-, exactly. Do nothing else.")
	if !strings.Contains(second, pages.marker) {
		t.Errorf("the index row's path did not lead to the rule body: no marker after reading %s", path)
	}
}

// codexPrimeHook is the SessionStart hook ox installs for Codex, with the isolated
// environment spelled inline: the agent needs the developer's home for its own login,
// and ox must not touch that home. It is passed as config because Codex exec does not
// run hooks from a throwaway repository's .codex/hooks.json.
func (fx *scopingFixture) codexPrimeHook(t *testing.T, ox string) string {
	t.Helper()
	vars := append([]string{"PATH=" + os.Getenv("PATH"), "AGENT_ENV=codex"}, fx.env.vars()...)
	command := "env -i " + strings.Join(shellQuoteAll(vars), " ") + " " + shellQuote(ox) + " agent hook SessionStart 2>&1 || true"
	encoded, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(`hooks.SessionStart=[{matcher="",hooks=[{type="command",command=%s,timeout=60}]}]`, encoded)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func shellQuoteAll(values []string) []string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = shellQuote(value)
	}
	return quoted
}

// codexSession runs one real `codex exec` and returns its recorded session.
func codexSession(t *testing.T, codex, repo, hook, prompt string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, codex, "exec",
		"--skip-git-repo-check", "--sandbox", "read-only", "--color", "never", "--json",
		"--enable", "hooks", "--dangerously-bypass-hook-trust",
		"-c", hook,
		prompt)
	cmd.Dir = repo
	cmd.Env = agentEnvironment()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		invalidRun(t, "codex exec failed: %v\n%s", err, truncate(stderr.String(), 800))
	}
	var started struct {
		Type     string `json:"type"`
		ThreadID string `json:"thread_id"`
	}
	firstLine, _, _ := strings.Cut(stdout.String(), "\n")
	if err := json.Unmarshal([]byte(firstLine), &started); err != nil || started.ThreadID == "" {
		invalidRun(t, "codex exec printed no thread id: %q", truncate(stdout.String(), 300))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	base := os.Getenv("CODEX_HOME")
	if base == "" {
		base = filepath.Join(home, ".codex")
	}
	var rollout string
	walkErr := filepath.WalkDir(filepath.Join(base, "sessions"), func(path string, d os.DirEntry, walkErr error) error {
		if walkErr == nil && !d.IsDir() && strings.Contains(d.Name(), started.ThreadID) {
			rollout = path
		}
		return nil
	})
	if walkErr != nil || rollout == "" {
		invalidRun(t, "no recorded session for thread %s under %s", started.ThreadID, base)
	}
	t.Cleanup(func() { _ = os.Remove(rollout) })
	raw, err := os.ReadFile(rollout)
	if err != nil {
		invalidRun(t, "read recorded session: %v", err)
	}
	return string(raw)
}

// teamRuleRow finds a rule's index row in text that may be JSON-escaped.
func teamRuleRow(transcript, name string) string {
	match := regexp.MustCompile(`\| ` + regexp.QuoteMeta(name) + ` \|[^\n\\]*\|`).FindString(transcript)
	return match
}

func lastTableCell(row string) string {
	cells := strings.Split(strings.Trim(row, "| "), " | ")
	return strings.TrimSpace(cells[len(cells)-1])
}
