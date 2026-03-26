package main

import (
	"strings"
	"testing"
	"time"
)

func TestFormatCredentialExpiry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		expiresAt  time.Time
		wantPrefix string // use prefix match to handle timing drift
	}{
		{
			name:       "expired",
			expiresAt:  time.Now().Add(-1 * time.Hour),
			wantPrefix: "expired",
		},
		{
			name:       "just expired",
			expiresAt:  time.Now().Add(-1 * time.Second),
			wantPrefix: "expired",
		},
		{
			name:       "minutes remaining",
			expiresAt:  time.Now().Add(30*time.Minute + 30*time.Second),
			wantPrefix: "30m",
		},
		{
			name:       "hours remaining",
			expiresAt:  time.Now().Add(5*time.Hour + 30*time.Minute),
			wantPrefix: "5h",
		},
		{
			name:       "days remaining",
			expiresAt:  time.Now().Add(3*24*time.Hour + time.Hour),
			wantPrefix: "3d",
		},
		{
			name:       "boundary just over 1 hour",
			expiresAt:  time.Now().Add(61*time.Minute + 30*time.Second),
			wantPrefix: "1h",
		},
		{
			name:       "boundary just over 1 day",
			expiresAt:  time.Now().Add(25*time.Hour + 30*time.Second),
			wantPrefix: "1d",
		},
		{
			name:       "under 1 minute",
			expiresAt:  time.Now().Add(30 * time.Second),
			wantPrefix: "0m",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatCredentialExpiry(tt.expiresAt)
			if !strings.HasPrefix(got, tt.wantPrefix) {
				t.Errorf("formatCredentialExpiry() = %q, want prefix %q", got, tt.wantPrefix)
			}
		})
	}
}

func TestFormatCredentialExpiry_ReturnFormat(t *testing.T) {
	t.Parallel()

	// verify the format is always a suffix of d/h/m or the word "expired"
	tests := []struct {
		name      string
		expiresAt time.Time
		wantSuffix string
	}{
		{name: "expired has no suffix", expiresAt: time.Now().Add(-1 * time.Hour), wantSuffix: ""},
		{name: "days end with d", expiresAt: time.Now().Add(48 * time.Hour), wantSuffix: "d"},
		{name: "hours end with h", expiresAt: time.Now().Add(3 * time.Hour), wantSuffix: "h"},
		{name: "minutes end with m", expiresAt: time.Now().Add(15 * time.Minute), wantSuffix: "m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatCredentialExpiry(tt.expiresAt)
			if tt.wantSuffix == "" {
				if got != "expired" {
					t.Errorf("expected 'expired', got %q", got)
				}
			} else if !strings.HasSuffix(got, tt.wantSuffix) {
				t.Errorf("expected suffix %q, got %q", tt.wantSuffix, got)
			}
		})
	}
}
