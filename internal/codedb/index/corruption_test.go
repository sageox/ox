package index

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/sageox/ox/internal/codedb/store"
)

// A panicking bleve batch (nil FST from a partial write) must surface as an
// errors.Is-able corruption sentinel, not just a string, so callers can decide
// to discard-and-rebuild rather than crash-loop.
func TestSafeBatch_WrapsCorruptSentinelOnPanic(t *testing.T) {
	err := safeBatch(func() error { panic("nil FST") })
	if err == nil {
		t.Fatal("expected an error from a panicking batch")
	}
	if !errors.Is(err, ErrBleveCorrupt) {
		t.Fatalf("expected ErrBleveCorrupt sentinel in chain, got %v", err)
	}
}

func TestIsCorruptionError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"bleve sentinel", ErrBleveCorrupt, true},
		{"wrapped bleve sentinel", fmt.Errorf("flush code batch: %w", ErrBleveCorrupt), true},
		{"store corrupt", store.ErrCorrupt, true},
		{"store full reindex required", store.ErrFullReindexRequired, true},
		// A timeout/cancel is NOT corruption — never discard the cache on it.
		{"context canceled", context.Canceled, false},
		{"context deadline", context.DeadlineExceeded, false},
		{"wrapped canceled", fmt.Errorf("index local: %w", context.Canceled), false},
		{"plain error", errors.New("disk full"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsCorruptionError(tt.err); got != tt.want {
				t.Fatalf("IsCorruptionError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
