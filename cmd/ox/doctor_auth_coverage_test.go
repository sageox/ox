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
		expiresIn  time.Duration
		wantPrefix string // use prefix match to handle timing drift
	}{
		{
			name:       "expired",
			expiresIn:  -1 * time.Hour,
			wantPrefix: "expired",
		},
		{
			name:       "just expired",
			expiresIn:  -1 * time.Second,
			wantPrefix: "expired",
		},
		{
			name:       "minutes remaining",
			expiresIn:  30*time.Minute + 30*time.Second,
			wantPrefix: "30m",
		},
		{
			name:       "hours remaining",
			expiresIn:  5*time.Hour + 30*time.Minute,
			wantPrefix: "5h",
		},
		{
			name:       "days remaining",
			expiresIn:  3*24*time.Hour + time.Hour,
			wantPrefix: "3d",
		},
		{
			name:       "boundary just over 1 hour",
			expiresIn:  61*time.Minute + 30*time.Second,
			wantPrefix: "1h",
		},
		{
			name:       "boundary just over 1 day",
			expiresIn:  25*time.Hour + 30*time.Second,
			wantPrefix: "1d",
		},
		{
			name:       "under 1 minute",
			expiresIn:  30 * time.Second,
			wantPrefix: "0m",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatCredentialExpiry(time.Now().Add(tt.expiresIn))
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
		name       string
		expiresIn  time.Duration
		wantSuffix string
	}{
		{name: "expired has no suffix", expiresIn: -1 * time.Hour, wantSuffix: ""},
		{name: "days end with d", expiresIn: 48 * time.Hour, wantSuffix: "d"},
		{name: "hours end with h", expiresIn: 3 * time.Hour, wantSuffix: "h"},
		{name: "minutes end with m", expiresIn: 15 * time.Minute, wantSuffix: "m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatCredentialExpiry(time.Now().Add(tt.expiresIn))
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
