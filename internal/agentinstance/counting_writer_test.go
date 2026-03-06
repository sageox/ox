package agentinstance

import (
	"bytes"
	"testing"
)

func TestCountingWriter_Empty(t *testing.T) {
	cw := NewCountingWriter(&bytes.Buffer{})
	if cw.BytesWritten() != 0 {
		t.Errorf("expected 0 bytes, got %d", cw.BytesWritten())
	}
}

func TestCountingWriter_SingleWrite(t *testing.T) {
	var buf bytes.Buffer
	cw := NewCountingWriter(&buf)

	n, err := cw.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 5 {
		t.Errorf("expected write of 5 bytes, got %d", n)
	}
	if cw.BytesWritten() != 5 {
		t.Errorf("expected 5 bytes counted, got %d", cw.BytesWritten())
	}
	if buf.String() != "hello" {
		t.Errorf("expected 'hello' in buffer, got %q", buf.String())
	}
}

func TestCountingWriter_MultipleWrites(t *testing.T) {
	var buf bytes.Buffer
	cw := NewCountingWriter(&buf)

	cw.Write([]byte("hello"))
	cw.Write([]byte(" "))
	cw.Write([]byte("world"))

	if cw.BytesWritten() != 11 {
		t.Errorf("expected 11 bytes, got %d", cw.BytesWritten())
	}
	if buf.String() != "hello world" {
		t.Errorf("expected 'hello world', got %q", buf.String())
	}
}

func TestCountingWriter_LargeWrite(t *testing.T) {
	var buf bytes.Buffer
	cw := NewCountingWriter(&buf)

	data := make([]byte, 100000)
	for i := range data {
		data[i] = 'x'
	}

	n, err := cw.Write(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 100000 {
		t.Errorf("expected 100000 bytes written, got %d", n)
	}
	if cw.BytesWritten() != 100000 {
		t.Errorf("expected 100000 bytes counted, got %d", cw.BytesWritten())
	}
}
