package errkind

import (
	"errors"
	"fmt"
	"io/fs"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Failure prevented: attaching a kind rewords the message a person reads,
// hides its cause from errors.Is, or lets a value filled into the message (a
// typed argument, a path) reach telemetry through the detail.
func TestErrorf(t *testing.T) {
	err := Errorf(Usage, "unknown session command: %s: %w\nAvailable: start", "sk-live-TOKEN", fs.ErrNotExist)

	assert.Equal(t, "unknown session command: sk-live-TOKEN: file does not exist\nAvailable: start", err.Error())
	assert.ErrorIs(t, err, fs.ErrNotExist)
	assert.Equal(t, "unknown session command: %s: %w", DetailOf(err))
}

func TestOf(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		kind   Kind
		detail string
	}{
		{"no kind", errors.New("boom"), "", ""},
		{"wrapped", fmt.Errorf("agent: %w", Errorf(Usage, "unknown session command: %s", "status")), Usage, "unknown session command: %s"},
		{"outer kind wins", Errorf(NotLoggedIn, "status: %w", Errorf(Auth, "401")), NotLoggedIn, "status: %w"},
		{"given detail", WithDetail(ChecksFailed, "Daemon,Sessions", errors.New("some checks failed")), ChecksFailed, "Daemon,Sessions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.kind, Of(tt.err))
			assert.Equal(t, tt.detail, DetailOf(tt.err))
		})
	}
}
