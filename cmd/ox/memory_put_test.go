package main

import (
	"testing"
)

func TestParseObservations(t *testing.T) {
	tests := []struct {
		name      string
		input     []byte
		wantCount int
		wantErr   bool
	}{
		{
			name:      "single JSON object",
			input:     []byte(`{"content":"hello"}`),
			wantCount: 1,
		},
		{
			name:      "JSONL with two entries",
			input:     []byte("{\"content\":\"a\"}\n{\"content\":\"b\"}"),
			wantCount: 2,
		},
		{
			name:      "JSONL blank lines skipped",
			input:     []byte("{\"content\":\"a\"}\n\n{\"content\":\"b\"}"),
			wantCount: 2,
		},
		{
			name:      "JSONL trailing newline",
			input:     []byte("{\"content\":\"a\"}\n"),
			wantCount: 1,
		},
		{
			name:    "invalid JSON",
			input:   []byte("not json"),
			wantErr: true,
		},
		{
			name:    "JSONL with one bad line",
			input:   []byte("{\"content\":\"ok\"}\nnot json"),
			wantErr: true,
		},
		{
			name:      "empty content falls to JSONL",
			input:     []byte(`{"content":""}`),
			wantCount: 1,
		},
		{
			name:      "extra fields ignored",
			input:     []byte(`{"content":"x","extra":"y"}`),
			wantCount: 1,
		},
		{
			name:      "no content field",
			input:     []byte(`{"other":"val"}`),
			wantCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseObservations(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != tt.wantCount {
				t.Errorf("got %d observations, want %d", len(got), tt.wantCount)
			}
		})
	}
}

func TestParseObservationsContent(t *testing.T) {
	input := []byte(`{"content":"We decided to use PostgreSQL"}`)
	obs, err := parseObservations(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(obs) != 1 {
		t.Fatalf("got %d observations, want 1", len(obs))
	}
	if obs[0].Content != "We decided to use PostgreSQL" {
		t.Errorf("content = %q, want %q", obs[0].Content, "We decided to use PostgreSQL")
	}
}

func TestMaxObservationBytes(t *testing.T) {
	if maxObservationBytes != 20480 {
		t.Errorf("maxObservationBytes = %d, want 20480", maxObservationBytes)
	}
}
