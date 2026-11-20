package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// userLine is the shape Claude Code writes when a screenshot is pasted with a
// prompt: the message content is an array of blocks, image blocks base64.
func userLine(t *testing.T, blocks ...map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"type": "user", "timestamp": "2026-03-04T13:41:10.123Z", "version": "2.1.0", "uuid": "u1",
		"message": map[string]any{"role": "user", "content": blocks},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func imageBlock(mime string, data []byte) map[string]any {
	return map[string]any{"type": "image", "source": map[string]any{
		"type": "base64", "media_type": mime, "data": base64.StdEncoding.EncodeToString(data),
	}}
}

func textBlock(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

func TestParseLineKeepsPastedImageAfterItsTurnText(t *testing.T) {
	img := []byte("\x89PNG\r\n\x1a\nfake-pixels")
	sum := sha256.Sum256(img)

	entries, err := parseLine(userLine(t, imageBlock("image/png", img), textBlock("why is this button misaligned?")))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want text then media", len(entries))
	}
	if entries[0].Role != adapterprotocol.RoleUser || entries[0].Content != "why is this button misaligned?" {
		t.Fatalf("first entry = %+v", entries[0])
	}
	m := entries[1]
	if m.Role != adapterprotocol.RoleMedia || m.Content != "" || m.Media == nil {
		t.Fatalf("second entry = %+v", m)
	}
	if m.Media.SHA256 != hex.EncodeToString(sum[:]) || m.Media.MIME != "image/png" || m.Media.Size != int64(len(img)) {
		t.Fatalf("media ref = %+v", m.Media)
	}
	if m.Media.Position != 0 || string(m.Media.Data) != string(img) || m.Media.Skipped != "" {
		t.Fatalf("media position/data = %+v", m.Media)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("media entry invalid: %v", err)
	}
	// the image bytes are base64 in the transcript only; the text entry has none
	if strings.Contains(entries[0].Content, "iVBOR") {
		t.Fatalf("image data leaked into text")
	}
}

// An image-only turn is still the person's prompt: the live stream opens on a
// user line, and a media line alone does not count as one.
func TestParseLineImageOnlyTurnIsAUserPromptThenMedia(t *testing.T) {
	entries, err := parseLine(userLine(t, imageBlock("image/jpeg", []byte("jpeg-bytes"))))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Role != adapterprotocol.RoleUser || entries[0].Content != imageOnlyPrompt || entries[1].Role != adapterprotocol.RoleMedia {
		t.Fatalf("entries = %+v", entries)
	}
	var raws []adapters.RawEntry
	for _, e := range entries {
		raws = append(raws, adapters.RawEntry{Role: e.Role, Content: e.Content, Media: e.Media})
	}
	line, err := json.Marshal(session.ConvertRawEntries(raws)[0])
	if err != nil || !session.IsUserTurnLine(line) {
		t.Fatalf("image-only prompt is not a user turn line: %s %v", line, err)
	}
}

// A user message that carries a tool result plus an image is not a prompt.
func TestParseLineToolResultWithImageIsNotAPrompt(t *testing.T) {
	entries, err := parseLine(userLine(t, map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": "ok"}, imageBlock("image/png", []byte("x"))))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Role == adapterprotocol.RoleUser {
			t.Fatalf("synthesized a prompt for a tool result: %+v", entries)
		}
	}
}

// Nine 4 MiB screenshots in one read would exceed the 64 MiB output limit. The
// read bounds image bytes, never the transcript: a stop or recovery makes one
// read and finalizes from it, so every record and every text turn must arrive
// and the cursor must reach the end.
func TestReadFromOffsetBoundsImageBytesButReadsEveryRecord(t *testing.T) {
	img := make([]byte, 4<<20)
	var file []byte
	for i := 0; i < 9; i++ {
		img[0] = byte(i) // distinct digests
		file = append(file, userLine(t, textBlock("shot"), imageBlock("image/png", img))...)
		file = append(file, '\n')
	}
	file = append(file, userLine(t, textBlock("the last turn"))...)
	file = append(file, '\n')
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, file, 0o600); err != nil {
		t.Fatal(err)
	}
	entries, next, err := readFromOffset(path, 0)
	if err != nil || next != int64(len(file)) {
		t.Fatalf("next=%d want %d err=%v: the read must reach the last record", next, len(file), err)
	}
	var texts, carried, limited, carriedBytes int
	for _, e := range entries {
		switch {
		case e.Role == adapterprotocol.RoleUser:
			texts++
		case e.Media != nil && len(e.Media.Data) > 0:
			carried++
			carriedBytes += len(e.Media.Data)
		case e.Media != nil && e.Media.Skipped == adapterprotocol.MediaSkippedReadLimit:
			limited++
		}
	}
	if texts != 10 {
		t.Fatalf("text turns = %d, want 10: a read must never drop the tail", texts)
	}
	if carried+limited != 9 || limited == 0 || carriedBytes > maxImageBytesPerRead {
		t.Fatalf("carried=%d (%d bytes) limited=%d, want every image carried or declared, bytes bounded", carried, carriedBytes, limited)
	}
	if out, _ := json.Marshal(entries); len(out) > 64<<20 {
		t.Fatalf("response is %d bytes, over the one-shot output limit", len(out))
	}
}

func TestReadSessionFileKeepsImagesOutOfWholeFileReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	line := append(userLine(t, textBlock("hi"), imageBlock("image/png", []byte("x"))), '\n')
	if err := os.WriteFile(path, line, 0o600); err != nil {
		t.Fatal(err)
	}
	entries, _, err := readSessionFile(path)
	if err != nil || len(entries) != 1 || entries[0].Role != adapterprotocol.RoleUser {
		t.Fatalf("entries = %+v, %v", entries, err)
	}
}

func TestParseLineKeepsImageOrderAndBlockPosition(t *testing.T) {
	entries, err := parseLine(userLine(t, textBlock("two screenshots"), imageBlock("image/png", []byte("one")), imageBlock("image/webp", []byte("two"))))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[1].Media.Position != 1 || entries[2].Media.Position != 2 || entries[2].Media.MIME != "image/webp" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestParseLineSkipsImageItCannotCarryWithAReason(t *testing.T) {
	entries, err := parseLine(userLine(t, textBlock("diagram"), imageBlock("image/svg+xml", []byte("<svg/>"))))
	if err != nil {
		t.Fatal(err)
	}
	m := entries[1].Media
	if m == nil || m.Skipped != adapterprotocol.MediaSkippedUnsupportedType || len(m.Data) != 0 {
		t.Fatalf("skipped ref = %+v", m)
	}
}

func TestImageRefOverSizeCapIsSkippedWithoutDecoding(t *testing.T) {
	huge := strings.Repeat("A", int(adapterprotocol.MaxMediaBytes)*4/3+16)
	ref := imageRef(0, "image/png", huge)
	if ref.Skipped != adapterprotocol.MediaSkippedTooLarge || len(ref.Data) != 0 || ref.Size <= adapterprotocol.MaxMediaBytes {
		t.Fatalf("ref = skipped %q size %d data %d", ref.Skipped, ref.Size, len(ref.Data))
	}
}

func TestImageRefMalformedBase64IsSkipped(t *testing.T) {
	if ref := imageRef(0, "image/png", "!!!not base64!!!"); ref.Skipped != adapterprotocol.MediaSkippedMalformed {
		t.Fatalf("ref = %+v", ref)
	}
}

func TestParseLineIgnoresURLImageSource(t *testing.T) {
	block := map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": "https://example.test/a.png"}}
	entries, err := parseLine(userLine(t, textBlock("see link"), block))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v", entries)
	}
}
