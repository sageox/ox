package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// runAsAdapterEnv makes the test binary run as the adapter itself, so a test
// can drive the real read path through the daemon's ExternalAdapter and its
// real one-shot output limit without a separate go build.
const runAsAdapterEnv = "OX_CLAUDE_ADAPTER_TEST_AS_BINARY"

func TestMain(m *testing.M) {
	if os.Getenv(runAsAdapterEnv) == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// daemonOutputLimit is the external adapter's one-shot output limit
// (adapters.defaultOneShotOutputLimit); the page budget must sit under it.
const daemonOutputLimit = 64 << 20

func toolResultLine(t *testing.T, id, output string) []byte {
	t.Helper()
	return userLine(t, map[string]any{"type": "tool_result", "tool_use_id": id, "content": output})
}

// assistantToolUseLine writes "<" as Claude Code does, raw, not as the \u003c
// json.Marshal would make it.
func assistantToolUseLine(t *testing.T, id string, input map[string]any) []byte {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	err := enc.Encode(map[string]any{
		"type": "assistant", "timestamp": "2026-03-04T13:41:11.123Z", "version": "2.1.0", "uuid": "a-" + id,
		"message": map[string]any{"role": "assistant", "model": "claude-test", "content": []any{
			map[string]any{"type": "text", "text": "writing the file"},
			map[string]any{"type": "tool_use", "id": id, "name": "Write", "input": input},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

// backlogRecord is what one transcript record must come back as.
type backlogRecord struct {
	text   string // the user turn's text, or "" for a tool result
	sha256 string // the pasted image's digest, for a screenshot turn
	callID string // the tool result's call id
	output int    // the tool result's length
}

// mixedBacklog writes a catch-up no single response can carry: seven 6 MiB
// screenshots and seven 7 MiB tool results, every line under the 10 MiB
// per-line cap, about 105 MiB serialized, so three pages at the 48 MiB budget.
func mixedBacklog(t *testing.T) (string, []backlogRecord) {
	t.Helper()
	img := make([]byte, 6<<20)
	output := strings.Repeat("x", 7<<20)
	var file bytes.Buffer
	var want []backlogRecord
	for i := range 7 {
		img[0], img[1] = byte(i), 0xA5 // distinct digests
		sum := sha256.Sum256(img)
		text := fmt.Sprintf("screenshot %d", i)
		file.Write(userLine(t, textBlock(text), imageBlock("image/png", img)))
		file.WriteByte('\n')
		want = append(want, backlogRecord{text: text, sha256: hex.EncodeToString(sum[:])})

		id := fmt.Sprintf("toolu_%d", i)
		out := fmt.Sprintf("tool output %d ", i) + output
		file.Write(toolResultLine(t, id, out))
		file.WriteByte('\n')
		want = append(want, backlogRecord{callID: id, output: len(out)})
	}
	file.Write(userLine(t, textBlock("the last turn")))
	file.WriteByte('\n')
	want = append(want, backlogRecord{text: "the last turn"})

	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, file.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, want
}

// flatten turns entries back into the records they came from, so a test can
// say every record arrived exactly once and in order.
func flatten(t *testing.T, entries []backlogRecord) string {
	t.Helper()
	parts := make([]string, 0, len(entries))
	for _, r := range entries {
		parts = append(parts, fmt.Sprintf("%s|%s|%s|%d", r.text, r.sha256, r.callID, r.output))
	}
	return strings.Join(parts, "\n")
}

// recordsOf rebuilds records from protocol entries: a user turn, then its image
// when it had one; or a tool result.
func recordsOf(t *testing.T, entries []adapterprotocol.RawEntry) []backlogRecord {
	t.Helper()
	var out []backlogRecord
	for _, e := range entries {
		switch {
		case e.Role == adapterprotocol.RoleUser:
			out = append(out, backlogRecord{text: e.Content})
		case e.Role == adapterprotocol.RoleMedia:
			if len(out) == 0 || e.Media == nil || e.Media.Skipped != "" {
				t.Fatalf("image entry %+v does not follow its turn, or was dropped", e.Media)
			}
			out[len(out)-1].sha256 = e.Media.SHA256
		case e.CallID != "":
			out = append(out, backlogRecord{callID: e.CallID, output: len(e.ToolOutput)})
		default:
			t.Fatalf("unexpected entry %+v", e)
		}
	}
	return out
}

func TestCatchUpBacklogPagesUnderTheOutputLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("short: writes and pages a 100 MiB transcript")
	}
	path, want := mixedBacklog(t)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// One ReadFromOffset is what a stop, a recovery and a finalize make. Through
	// the daemon's real 64 MiB output limit it must still be the whole tail, in
	// pages that each stay under the adapter's budget. Run once: the backlog is
	// costly under -race.
	t.Setenv("XDG_DATA_HOME", t.TempDir()) // the daemon side stages image bytes here
	t.Setenv(runAsAdapterEnv, "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ea := adapters.NewExternalAdapterWithInfo(exe, &adapterprotocol.InfoResponse{
		Name: adapterName, RequiredEnv: []string{runAsAdapterEnv},
	})
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	// generous per page: -race slows the adapter subprocess several times
	entries, next, err := ea.ReadFromOffsetWithContext(t.Context(), path, 0, 2*time.Minute)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	type readSummary struct {
		Msg         string `json:"msg"`
		Pages       int    `json:"pages"`
		LargestPage int    `json:"largest_page_bytes"`
		Outcome     string `json:"outcome"`
	}
	var summaries []readSummary
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		var rec readSummary
		if json.Unmarshal(line, &rec) == nil && rec.Msg == "adapter_read_paged" {
			summaries = append(summaries, rec)
		}
	}
	if len(summaries) != 1 || summaries[0].Pages < 3 || summaries[0].Outcome != "drained" {
		t.Fatalf("read summaries = %+v, want one drained read of at least 3 pages", summaries)
	}
	if largest := summaries[0].LargestPage; largest <= 0 || largest > maxReadResponseBytes {
		t.Fatalf("largest page response = %d bytes, want it under the %d-byte budget", largest, maxReadResponseBytes)
	}
	if next != info.Size() {
		t.Fatalf("cursor = %d, want the end of the transcript at %d", next, info.Size())
	}
	protocol := make([]adapterprotocol.RawEntry, 0, len(entries))
	for _, e := range entries {
		protocol = append(protocol, adapterprotocol.RawEntry{Role: e.Role, Content: e.Content, ToolOutput: e.ToolOutput, CallID: e.CallID, Media: e.Media})
	}
	if got := recordsOf(t, protocol); flatten(t, got) != flatten(t, want) {
		t.Fatalf("read records:\n%s\nwant every record exactly once, in order:\n%s", flatten(t, got), flatten(t, want))
	}
}

func TestPageBudgetSitsUnderTheDaemonLimit(t *testing.T) {
	// the margin is what absorbs stderr and an encoder that ever escapes more
	if maxReadResponseBytes > daemonOutputLimit*3/4 {
		t.Fatalf("page budget %d leaves under a quarter of the %d-byte output limit as margin", maxReadResponseBytes, daemonOutputLimit)
	}
}

// A record that serializes past any page (a tool input dense in "<" is escaped
// twice, seven bytes for each one) must not wedge the cursor or fail the read:
// it becomes a declared gap and the read moves on.
func TestRecordNoPageCanHoldBecomesAGapAndTheReadMovesOn(t *testing.T) {
	const limit = 16 << 10
	huge := assistantToolUseLine(t, "toolu_big", map[string]any{"file_path": "/tmp/page.html", "content": strings.Repeat("<", limit/2)})
	if n := len(huge); n > limit {
		t.Fatalf("fixture line is %d bytes; it must fit the limit as a line and only overflow it once escaped", n)
	}
	var file []byte
	file = append(file, huge...)
	file = append(file, '\n')
	file = append(file, userLine(t, textBlock("after the big write"))...)
	file = append(file, '\n')
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, file, 0o600); err != nil {
		t.Fatal(err)
	}

	var all []adapterprotocol.RawEntry
	var offset int64
	for range 5 { // test watchdog only
		entries, next, err := readPage(path, offset, limit)
		if err != nil {
			t.Fatal(err)
		}
		if n := responseBytes(entries); n > limit {
			t.Fatalf("page carries %d bytes, over the %d-byte limit", n, limit)
		}
		if next == offset {
			break
		}
		all = append(all, entries...)
		offset = next
	}
	if offset != int64(len(file)) {
		t.Fatalf("cursor = %d, want %d: the oversized record must not wedge it", offset, len(file))
	}
	if len(all) != 3 {
		t.Fatalf("entries = %+v, want the gap record's two entries then the next turn", all)
	}
	text, tool, next := all[0], all[1], all[2]
	if text.Role != adapterprotocol.RoleAssistant || text.Content != omittedRecordText {
		t.Fatalf("assistant text = %+v, want the gap marker", text)
	}
	if tool.ToolName != "Write" || tool.CallID != "toolu_big" || tool.ToolInput != omittedRecordText {
		t.Fatalf("tool use = %+v, want its name and call id kept and its input replaced by the gap marker", tool)
	}
	if next.Role != adapterprotocol.RoleUser || next.Content != "after the big write" {
		t.Fatalf("next turn = %+v, want it read whole", next)
	}
}

func TestOmittedRecordKeepsImageAsDeclaredGap(t *testing.T) {
	entries, err := parseLine(userLine(t, textBlock("look"), imageBlock("image/png", []byte("pixels"))))
	if err != nil {
		t.Fatal(err)
	}
	out := omitOversizedRecord(entries)
	m := out[1].Media
	if m == nil || len(m.Data) != 0 || m.Skipped != adapterprotocol.MediaSkippedReadLimit || m.SHA256 == "" {
		t.Fatalf("image = %+v, want its digest kept and its bytes declared skipped", m)
	}
	if len(entries[1].Media.Data) == 0 {
		t.Fatal("omitting a record changed the entries it was given")
	}
}
