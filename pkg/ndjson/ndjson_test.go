package ndjson_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sageox/ox/pkg/ndjson"
)

func TestScanner_NormalLines(t *testing.T) {
	input := `{"id":1,"method":"info"}
{"id":2,"method":"detect"}
`
	sc := ndjson.NewScanner(strings.NewReader(input))

	if !sc.Scan() {
		t.Fatal("expected first line")
	}
	if got := sc.Text(); got != `{"id":1,"method":"info"}` {
		t.Errorf("first line = %q, want %q", got, `{"id":1,"method":"info"}`)
	}

	if !sc.Scan() {
		t.Fatal("expected second line")
	}
	if got := sc.Text(); got != `{"id":2,"method":"detect"}` {
		t.Errorf("second line = %q, want %q", got, `{"id":2,"method":"detect"}`)
	}

	if sc.Scan() {
		t.Error("expected no more lines")
	}
	if err := sc.Err(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestScanner_EmptyInput(t *testing.T) {
	sc := ndjson.NewScanner(strings.NewReader(""))
	if sc.Scan() {
		t.Error("expected no lines from empty input")
	}
	if err := sc.Err(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestScanner_LineTooLong(t *testing.T) {
	// create a line longer than MaxLineBytes
	long := strings.Repeat("x", ndjson.MaxLineBytes+1) + "\n"
	sc := ndjson.NewScanner(strings.NewReader(long))
	if sc.Scan() {
		t.Error("expected scan to fail on oversized line")
	}
	if err := sc.Err(); err == nil {
		t.Error("expected error for oversized line, got nil")
	}
}

func TestEncoder_CompactJSON(t *testing.T) {
	var buf bytes.Buffer
	enc := ndjson.NewEncoder(&buf)

	msg := map[string]any{"id": 1, "method": "info"}
	if err := enc.Encode(msg); err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	line := strings.TrimSpace(buf.String())
	// compact JSON should not have extra whitespace
	if strings.Contains(line, "  ") {
		t.Errorf("output has extra whitespace: %s", line)
	}
	// must end with newline
	if !strings.HasSuffix(buf.String(), "\n") {
		t.Error("output missing trailing newline")
	}
}

func TestEncoder_MultipleMessages(t *testing.T) {
	var buf bytes.Buffer
	enc := ndjson.NewEncoder(&buf)

	for i := 0; i < 3; i++ {
		if err := enc.Encode(map[string]int{"n": i}); err != nil {
			t.Fatalf("encode %d failed: %v", i, err)
		}
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d", len(lines))
	}
}

func TestDecode(t *testing.T) {
	data := []byte(`{"id":42,"method":"shutdown"}`)
	var msg struct {
		ID     int    `json:"id"`
		Method string `json:"method"`
	}
	if err := ndjson.Decode(data, &msg); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if msg.ID != 42 || msg.Method != "shutdown" {
		t.Errorf("decoded = %+v, want id=42, method=shutdown", msg)
	}
}

func TestRoundTrip(t *testing.T) {
	type msg struct {
		ID     int    `json:"id"`
		Method string `json:"method"`
	}

	var buf bytes.Buffer
	enc := ndjson.NewEncoder(&buf)

	want := msg{ID: 1, Method: "find-session"}
	if err := enc.Encode(want); err != nil {
		t.Fatalf("encode: %v", err)
	}

	sc := ndjson.NewScanner(&buf)
	if !sc.Scan() {
		t.Fatal("expected line")
	}

	var got msg
	if err := ndjson.Decode(sc.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != want {
		t.Errorf("round-trip: got %+v, want %+v", got, want)
	}
}
