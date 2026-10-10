package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/errkind"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/session/nativeimport"
	"github.com/sageox/ox/internal/testguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Preview excerpts must use import retention and redaction, exclude bootstrap
// context from human anchors, and create no recording or summary. Cached content
// must still be rejected after the native source changes.
func TestImportContent_ReadOnlyNormalizedExcerpts(t *testing.T) {
	f := newImportFixture(t)
	start := time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC)
	path := f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexA, start: start, prompt: loginPrompt, reply: "Last reply", entries: []adapters.RawEntry{
		{Role: "user", Content: "<environment_context>cwd</environment_context>"},
		{Role: "user", Content: "# AGENTS.md instructions for this project"},
		{Role: "user", Content: `<external_codex_apps_open_page>{"page_id":null}</external_codex_apps_open_page>`},
		{Role: "user", Content: loginPrompt},
		{Role: "assistant", Content: "Investigating"},
		{Role: "tool", ToolName: "exec_command", ToolInput: `{"cmd":"gh auth token"}`, CallID: "credential"},
		{Role: "tool", ToolOutput: "a-secret-without-token-shape", CallID: "credential"},
		{Role: "user", Content: "Fix the cookie"},
		{Role: "assistant", Content: "Last reply"},
	}})
	opts := importOptions{preview: true, sessions: []string{e2eCodexA}, jsonOut: true}
	env, dest := f.envFor(f.ledgerPath, opts)
	head := runGit(t, f.barePath, "rev-parse", "HEAD")
	var out bytes.Buffer
	require.NoError(t, runSessionImportFlow(context.Background(), &out, opts, env, dest))
	var result struct {
		Status  string
		Preview importContentPreview
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))
	assert.Equal(t, "content_preview", result.Status)
	assert.Equal(t, loginPrompt, result.Preview.OpeningRequest)
	require.Len(t, result.Preview.Prompts, 2)
	assert.Equal(t, 3, result.Preview.Prompts[0].EntryIndex)
	assert.Contains(t, result.Preview.Entries[2].Content, "<external_codex_apps_open_page>", "app context remains available in the retained conversation")
	assert.Equal(t, "Last reply", result.Preview.LastReply)
	assert.NotContains(t, out.String(), "a-secret-without-token-shape")
	assert.Contains(t, out.String(), "REDACTED")
	assertNothingMoved(t, f, head)
	_, err := os.Stat(env.stagingRoot)
	assert.True(t, os.IsNotExist(err), "reading content must not create staging")

	// Cached previews are still tied to the source; a resumed native session
	// must not silently return old content or import the newer conversation.
	cands, _, failure := planImport(context.Background(), opts, env)
	require.Nil(t, failure)
	load := newImportPreviewLoader(env, cands)
	_, err = load(context.Background(), e2eCodexA)
	require.NoError(t, err)
	reads := f.readCount()
	_, err = load(context.Background(), e2eCodexA)
	require.NoError(t, err)
	assert.Equal(t, reads, f.readCount())
	require.NoError(t, os.WriteFile(path, []byte("changed session"), 0600))
	_, err = load(context.Background(), e2eCodexA)
	assert.ErrorIs(t, err, errImportSourceChanged)
}

// Only leading vendor bootstrap metadata is context. Human XML and mentions
// of those tags inside a request must remain visible human prompts.
func TestImportContextPrompt_VendorMetadataAndHumanXML(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		context       bool
	}{
		{"Codex app context", `<external_codex_apps_open_page>{"page_id":null}</external_codex_apps_open_page>`, true},
		{"leading whitespace", "\n  <external_codex_apps_open_page>{}</external_codex_apps_open_page>", true},
		{"human XML request", "<widget>Build this component</widget>", false},
		{"similar human XML request", "<external_codex_widget>Build this component</external_codex_widget>", false},
		{"human explanation", "Explain <external_codex_apps_open_page> metadata", false},
		{"ordinary request", "Create a simple hello world program in python", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.context, isImportContextPrompt(tc.content))
		})
	}
}

// Human-authored XML must remain navigable even when its tag resembles Codex metadata.
func TestImportContent_HumanXMLRemainsOpeningRequest(t *testing.T) {
	f := newImportFixture(t)
	prompt := "<external_codex_widget>Build this component</external_codex_widget>"
	f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexA,
		start:  time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC),
		prompt: prompt, reply: "I will build it", entries: []adapters.RawEntry{
			{Role: "user", Content: `<external_codex_apps_open_page>{"page_id":null}</external_codex_apps_open_page>`},
			{Role: "user", Content: prompt},
			{Role: "assistant", Content: "I will build it"},
		}})
	opts := importOptions{preview: true, sessions: []string{e2eCodexA}, jsonOut: true}
	env, dest := f.envFor(f.ledgerPath, opts)
	var out bytes.Buffer
	require.NoError(t, runSessionImportFlow(context.Background(), &out, opts, env, dest))
	var result struct{ Preview importContentPreview }
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))
	assert.Equal(t, prompt, result.Preview.OpeningRequest)
	require.Len(t, result.Preview.Prompts, 1)
	assert.Equal(t, prompt, result.Preview.Prompts[0].Content)
	assert.Equal(t, 1, result.Preview.Prompts[0].EntryIndex)
	assert.Contains(t, result.Preview.Entries[0].Content, "<external_codex_apps_open_page>")
	assert.Zero(t, f.store.count())
	assert.Zero(t, f.summarizer.calls())
}

// Review preserves exact, empty and canceled selections but cannot authorize
// upload by itself. Terminal confirmation remains required, and a source edit
// during that confirmation must be refused before publication.
func TestImportReview_SelectionConfirmationAndSourceChanges(t *testing.T) {
	for _, scenario := range []string{"select one", "clear all", "cancel", "decline", "changed during confirmation", "unknown", "duplicate"} {
		t.Run(scenario, func(t *testing.T) {
			f := newImportFixture(t)
			f.interactive = true
			start := time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC)
			path := f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: start, prompt: loginPrompt, reply: "Fixed the cookie."})
			f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexA, start: start.Add(time.Hour), prompt: pushPrompt, reply: "Re-uploaded."})
			env, dest := f.envFor(f.ledgerPath, importOptions{})
			head := runGit(t, f.barePath, "rev-parse", "HEAD")
			confirmed := 0
			env.deps.confirm = func(prompt string) (bool, error) {
				confirmed++
				assert.Contains(t, prompt, "Upload 1 session")
				_, err := os.Stat(env.stagingRoot)
				assert.True(t, os.IsNotExist(err), "confirmation must precede staging")
				if scenario == "changed during confirmation" {
					file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
					require.NoError(t, err)
					_, err = file.WriteString(jsonLine(t, map[string]any{
						"type": "assistant", "sessionId": e2eClaudeA,
						"timestamp": start.Add(2 * time.Minute).Format(time.RFC3339), "cwd": f.projectRoot,
						"message": map[string]any{"role": "assistant", "content": "A new reply after review"},
					}) + "\n")
					require.NoError(t, err)
					require.NoError(t, file.Close())
					// Keep discovery eligible to exercise snapshot revalidation,
					// independently of the existing quiet-period guard.
					require.NoError(t, os.Chtimes(path, start, start))
				}
				return scenario != "decline", nil
			}
			env.deps.review = func(ctx context.Context, _ importDestination, cands []*importCandidate, load importPreviewLoader, _ bool) (importReviewResult, error) {
				assert.Len(t, selectedCandidates(cands), 2)
				preview, err := load(ctx, e2eClaudeA)
				require.NoError(t, err)
				assert.Equal(t, loginPrompt, preview.OpeningRequest)
				assert.Equal(t, 0, f.store.count())
				assert.Equal(t, 0, f.summarizer.calls())
				switch scenario {
				case "clear all":
					return importReviewResult{}, nil
				case "cancel":
					return importReviewResult{Canceled: true}, nil
				case "unknown":
					return importReviewResult{IDs: []string{"unknown"}}, nil
				case "duplicate":
					return importReviewResult{IDs: []string{e2eClaudeA, e2eClaudeA}}, nil
				default:
					return importReviewResult{IDs: []string{e2eClaudeA}}, nil
				}
			}
			var out bytes.Buffer
			err := runSessionImportFlow(context.Background(), &out, importOptions{}, env, dest)
			if scenario == "select one" {
				require.NoError(t, err, out.String())
				assert.Equal(t, 1, confirmed)
				assert.Contains(t, out.String(), "1 selected · 1 uploaded")
				assert.Zero(t, f.store.holding(pushPrompt), "deselected session never leaves the machine")
				return
			}
			if strings.HasPrefix(scenario, "changed") || scenario == "unknown" || scenario == "duplicate" {
				require.Error(t, err)
				if scenario == "changed during confirmation" {
					assert.Equal(t, importErrNativeUnreadable, errkind.DetailOf(err), "refuse changed content rather than quietly replacing the reviewed source")
					assert.Equal(t, 1, confirmed)
				}
			} else {
				require.NoError(t, err)
			}
			assertNothingMoved(t, f, head)
			if scenario == "clear all" || scenario == "cancel" || scenario == "unknown" || scenario == "duplicate" {
				assert.Zero(t, confirmed)
			}
		})
	}
}

// Real vendor adapters and native fixtures must produce usable retained entries;
// mock records alone cannot detect drift in the vendors' session formats.
func TestImportContent_RealAdapterFixtures(t *testing.T) {
	if testing.Short() {
		t.Skip("short: builds actual vendor adapter binaries")
	}
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	// Real cleaned vendor recordings exercise the same adapters used in
	// production, including Codex's injected context before the first request.
	for _, tc := range []struct {
		agent   nativeimport.Agent
		pattern string
	}{
		{nativeimport.AgentClaude, "testdata/session-import/math-blitz/claude/*.jsonl"},
		{nativeimport.AgentCodex, "testdata/session-import/math-blitz/codex/*.jsonl"},
	} {
		t.Run(string(tc.agent), func(t *testing.T) {
			files, err := filepath.Glob(filepath.Join(filepath.Dir(source), tc.pattern))
			require.NoError(t, err)
			require.NotEmpty(t, files)
			adapterName := adapterNameFor(tc.agent)
			bin := filepath.Join(t.TempDir(), "ox-adapter-"+adapterName)
			cmd := exec.Command("go", "build", "-o", bin, "./cmd/ox-adapter-"+adapterName)
			cmd.Dir = filepath.Clean(filepath.Join(filepath.Dir(source), "../.."))
			cmd.Env = testguard.MinimalEnv(nil)
			buildOutput, err := cmd.CombinedOutput()
			require.NoError(t, err, string(buildOutput))
			adapter, err := adapters.NewExternalAdapter(bin)
			require.NoError(t, err)
			raw, err := adapter.Read(files[0])
			require.NoError(t, err)
			entries, err := nativeimport.PreviewEntries(t.TempDir(), raw)
			require.NoError(t, err)
			assert.NotEmpty(t, entries)
		})
	}
}

// A browser request from a noninteractive invocation must remain a metadata
// preview, without waiting for a callback, reading content or starting uploads.
func TestImportReview_NoninteractiveBrowserFallback(t *testing.T) {
	f := newImportFixture(t)
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: time.Now().Add(-48 * time.Hour), prompt: loginPrompt, reply: "Fixed."})
	env, dest := f.envFor(f.ledgerPath, importOptions{browse: true})
	env.deps.review = func(context.Context, importDestination, []*importCandidate, importPreviewLoader, bool) (importReviewResult, error) {
		t.Fatal("headless/no-input execution must not open or wait for a browser")
		return importReviewResult{}, nil
	}
	var out bytes.Buffer
	require.NoError(t, runSessionImportFlow(context.Background(), &out, importOptions{browse: true}, env, dest))
	assert.Contains(t, out.String(), "Ready to upload (1)")
	assert.Zero(t, f.readCount())
	assert.Zero(t, f.summarizer.calls())
	assert.Zero(t, f.store.count())
}

// Content lookup accepts the same case-insensitive ID prefixes as selection,
// while unreadable adapters return a controlled error without private diagnostics.
func TestImportContent_UppercasePrefixAndUnavailableContent(t *testing.T) {
	f := newImportFixture(t)
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: time.Now().Add(-48 * time.Hour), prompt: loginPrompt, reply: "Fixed."})
	opts := importOptions{preview: true, sessions: []string{strings.ToUpper(e2eClaudeA[:8])}, jsonOut: true}
	env, dest := f.envFor(f.ledgerPath, opts)
	var out bytes.Buffer
	require.NoError(t, runSessionImportFlow(context.Background(), &out, opts, env, dest))
	assert.Contains(t, out.String(), loginPrompt)
	assert.Zero(t, f.summarizer.calls())
	f.readErr = errors.New("adapter unavailable")
	out.Reset()
	require.Error(t, runSessionImportFlow(context.Background(), &out, opts, env, dest))
	var refusal struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &refusal))
	assert.Equal(t, importErrNativeUnreadable, refusal.Error)
	assert.NotContains(t, out.String(), "adapter unavailable")
}

// Native terminal escapes must not clear the screen or spoof its title. Text
// preview still shows the human request and last reply without staged artifacts.
func TestImportContent_TextPreviewIsSafeAndReadOnly(t *testing.T) {
	f := newImportFixture(t)
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: time.Now().Add(-48 * time.Hour), prompt: "Fix the cookie\x1b[2J\x1b]0;spoofed\x07", reply: "Fixed."})
	opts := importOptions{preview: true, sessions: []string{e2eClaudeA}}
	env, dest := f.envFor(f.ledgerPath, opts)
	var out bytes.Buffer
	require.NoError(t, runSessionImportFlow(context.Background(), &out, opts, env, dest))
	assert.Contains(t, out.String(), "Opening request\nFix the cookie")
	assert.Contains(t, out.String(), "Human prompts\n1. Fix the cookie")
	assert.Contains(t, out.String(), "Last AI reply\nFixed.")
	assert.NotContains(t, out.String(), "\x1b")
	assert.NotContains(t, out.String(), "spoofed")
	assert.Zero(t, f.summarizer.calls())
	assert.Zero(t, f.store.count())
	_, err := os.Stat(env.stagingRoot)
	assert.True(t, os.IsNotExist(err))
}

// Unknown IDs, cancellation, invalid redaction and changing sources must expose
// no partial content. Adapter diagnostics stay private and failures never reach
// the summarizer or object store.
func TestImportContent_RefusesUnavailableUnsafeOrChangingSources(t *testing.T) {
	for _, scenario := range []string{"unknown", "canceled before read", "reader error", "invalid policy", "changed during read", "canceled during read", "changed selection"} {
		t.Run(scenario, func(t *testing.T) {
			f := newImportFixture(t)
			path := f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: time.Now().Add(-48 * time.Hour), prompt: loginPrompt, reply: "Fixed."})
			env, _ := f.envFor(f.ledgerPath, importOptions{})
			cands, _, failure := planImport(context.Background(), importOptions{}, env)
			require.Nil(t, failure)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			id := e2eClaudeA
			switch scenario {
			case "unknown":
				id = "missing-session"
			case "canceled before read":
				cancel()
			case "reader error":
				env.deps.readNative = func(context.Context, nativeimport.Agent, string) ([]adapters.RawEntry, error) {
					return nil, errors.New("secret-file-path-and-context")
				}
			case "invalid policy":
				require.NoError(t, os.WriteFile(filepath.Join(f.projectRoot, ".sageox", "REDACT.md"), []byte("```redact\nregex \"[\" -> [X]\n```\n"), 0600))
			case "changed during read", "canceled during read":
				env.deps.readNative = func(ctx context.Context, agent nativeimport.Agent, source string) ([]adapters.RawEntry, error) {
					raw, err := f.readNative(ctx, agent, source)
					if scenario == "changed during read" {
						require.NoError(t, os.Chtimes(path, time.Now(), time.Now()))
					} else {
						cancel()
					}
					return raw, err
				}
			case "changed selection":
				require.NoError(t, os.Chtimes(path, time.Now(), time.Now()))
				_, err := validateImportReview(ctx, cands, importReviewResult{IDs: []string{e2eClaudeA}})
				require.ErrorIs(t, err, errImportSourceChanged)
				return
			}
			p, err := newImportPreviewLoader(env, cands)(ctx, id)
			require.Error(t, err)
			assert.Nil(t, p)
			assert.NotContains(t, err.Error(), "secret-file-path-and-context")
			assert.Zero(t, f.summarizer.calls())
			assert.Zero(t, f.store.count())
		})
	}
}
