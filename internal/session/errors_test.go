package session

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewSessionError(t *testing.T) {
	t.Parallel()

	err := NewSessionError(ErrCodeNotRecording, "not recording", false, "ox agent prime")
	assert.Equal(t, ErrCodeNotRecording, err.Code)
	assert.Equal(t, "not recording", err.Message)
	assert.False(t, err.Retryable)
	assert.Equal(t, "ox agent prime", err.Fix)
}

func TestSessionError_Error(t *testing.T) {
	t.Parallel()

	err := NewSessionError(ErrCodeStorageError, "disk full", true, "")
	assert.Equal(t, "disk full", err.Error())

	// implements error interface
	var e error = err
	assert.Equal(t, "disk full", e.Error())
}

func TestSessionError_Retryable(t *testing.T) {
	t.Parallel()

	retryable := NewSessionError(ErrCodeStorageError, "temp failure", true, "")
	assert.True(t, retryable.Retryable)

	nonRetryable := NewSessionError(ErrCodeAuthRequired, "auth needed", false, "ox login")
	assert.False(t, nonRetryable.Retryable)
}

func TestSessionError_Fix(t *testing.T) {
	t.Parallel()

	withFix := NewSessionError(ErrCodeAuthRequired, "auth", false, "ox login")
	assert.Equal(t, "ox login", withFix.Fix)

	noFix := NewSessionError(ErrCodeInvalidEntry, "bad entry", false, "")
	assert.Empty(t, noFix.Fix)
}

func TestSentinelErrors(t *testing.T) {
	t.Parallel()

	// all sentinel errors should be distinct
	sentinels := []error{
		ErrSessionNotFound,
		ErrNoSessions,
		ErrNoRawSessions,
		ErrEmptyPath,
		ErrEmptyFilename,
		ErrNilEntry,
		ErrNilData,
		ErrNilState,
		ErrSessionNotHydrated,
	}

	for i, a := range sentinels {
		for j, b := range sentinels {
			if i != j {
				assert.False(t, errors.Is(a, b), "%v should not match %v", a, b)
			}
		}
	}
}

func TestErrorCodes_AreDistinct(t *testing.T) {
	t.Parallel()

	codes := []string{
		ErrCodeNotRecording,
		ErrCodeAlreadyRecording,
		ErrCodeNoSession,
		ErrCodeInvalidEntry,
		ErrCodeStorageError,
		ErrCodeAuthRequired,
	}

	seen := make(map[string]bool)
	for _, code := range codes {
		assert.False(t, seen[code], "duplicate error code: %s", code)
		seen[code] = true
	}
}
