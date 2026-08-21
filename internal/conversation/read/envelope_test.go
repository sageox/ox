package read

import (
	"testing"
	"time"
)

// TestErrorEnvelopeCarriesGuidanceAndTokenEstimate verifies D15's "every
// envelope carries token_estimate and guidance" on the typed-error path.
// Failure prevented: an agent hitting not_indexed / no_distillation / etc.
// gets no next-step guidance in the guidance channel (only error prose), and
// both omitempty fields vanish from the JSON.
func TestErrorEnvelopeCarriesGuidanceAndTokenEstimate(t *testing.T) {
	codes := []string{
		ErrCodeInvalidID,
		ErrCodeNoTeamContext,
		ErrCodeNotIndexed,
		ErrCodeNoDistillation,
		ErrCodeTranscriptNotAvailable,
		ErrCodeTopicNotFound,
		ErrCodeInvalidSelector,
		ErrCodeReadError,
		"usage_error", // command-layer flag failures share the same path
	}
	for _, code := range codes {
		t.Run(code, func(t *testing.T) {
			env := ErrorEnvelope(newError(code, "boom"))
			if env.Error == nil || env.Error.Code != code {
				t.Fatalf("envelope error = %+v, want code %q", env.Error, code)
			}
			if env.Guidance == "" {
				t.Errorf("guidance is empty for code %q", code)
			}
			if env.TokenEstimate <= 0 {
				t.Errorf("token_estimate = %d for code %q, want > 0", env.TokenEstimate, code)
			}
		})
	}
}

// TestFinishErrorMatchesErrorEnvelopeContract verifies the reader-internal
// error path carries the same D15 fields plus warnings and elapsed stamping.
func TestFinishErrorMatchesErrorEnvelopeContract(t *testing.T) {
	r := &Reader{now: time.Now}
	env := r.finishError(time.Now(), newError(ErrCodeNotIndexed, "no such conversation"), []string{"advisory"})
	if env.Guidance == "" {
		t.Error("finishError produced empty guidance")
	}
	if env.TokenEstimate <= 0 {
		t.Errorf("finishError token_estimate = %d, want > 0", env.TokenEstimate)
	}
	if len(env.Warnings) != 1 || env.Warnings[0] != "advisory" {
		t.Errorf("warnings = %v, want [advisory]", env.Warnings)
	}
	if env.Success {
		t.Error("error envelope marked success")
	}
}

// TestNewErrorRetryableContract pins the documented contract: read_error is
// the only retryable code. Failure prevented: agents branching on retryable
// either retry stable facts (invalid ids, missing data) or give up on
// transient filesystem failures.
func TestNewErrorRetryableContract(t *testing.T) {
	codes := []string{
		ErrCodeInvalidID, ErrCodeNoTeamContext, ErrCodeNotIndexed,
		ErrCodeNoDistillation, ErrCodeTranscriptNotAvailable,
		ErrCodeTopicNotFound, ErrCodeInvalidSelector, ErrCodeReadError,
	}
	for _, code := range codes {
		got := newError(code, "m").Retryable
		want := code == ErrCodeReadError
		if got != want {
			t.Errorf("newError(%s).Retryable = %v, want %v", code, got, want)
		}
	}
}
