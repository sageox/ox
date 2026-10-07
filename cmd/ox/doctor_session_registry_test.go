package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// removing a registry entry must not make the Sessions phase panic.
func TestSessionChecksSkipMissingRegistration(t *testing.T) {
	for _, tc := range []struct {
		slug string
		run  func(doctorOptions) checkResult
	}{
		{CheckSlugSessionUncommitted, checkSessionUncommittedViaRegistry},
		{CheckSlugSessionDraftOrphan, checkSessionDraftOrphanViaRegistry},
	} {
		t.Run(tc.slug, func(t *testing.T) {
			saved := DoctorCheckRegistry[tc.slug]
			delete(DoctorCheckRegistry, tc.slug)
			t.Cleanup(func() { DoctorCheckRegistry[tc.slug] = saved })
			result := tc.run(doctorOptions{})
			assert.True(t, result.skipped)
			assert.False(t, result.passed)
			assert.Contains(t, result.message, "not registered")
		})
	}
}
