package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A renamed or removed registry slug must degrade the Sessions phase to a skip,
// not panic every `ox doctor` run.
func TestSessionRegistryChecks_UnregisteredSlugSkipsInsteadOfPanicking(t *testing.T) {
	for _, slug := range []string{CheckSlugSessionUncommitted, CheckSlugSessionDraftOrphan} {
		saved := DoctorCheckRegistry[slug]
		delete(DoctorCheckRegistry, slug)
		t.Cleanup(func() { DoctorCheckRegistry[slug] = saved })
	}

	assert.NotPanics(t, func() {
		uncommitted := checkSessionUncommittedViaRegistry(doctorOptions{})
		assert.True(t, uncommitted.skipped)
		assert.Equal(t, "session uncommitted", uncommitted.name)
		assert.Equal(t, "check not registered", uncommitted.message)
		draftOrphan := checkSessionDraftOrphanViaRegistry(doctorOptions{})
		assert.True(t, draftOrphan.skipped)
		assert.Equal(t, "session draft orphan", draftOrphan.name)
	})
}
