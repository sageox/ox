package main

import (
	"testing"

	"github.com/sageox/ox/extensions/skills"
	"github.com/stretchr/testify/require"
)

// TestCartSkillsFollowTheCartsFeatureGate pins the rule that a skill may not
// teach a command this binary refuses to run.
//
// `ox-cli-cart*` shipped in the "lifecycle" bundle (Default: true), so all four
// installed for everyone — while `ox carts` is gated behind FEATURE_CARTS and
// errors when it is off. An AI coworker reading the skill would follow it into
// a command that refuses, with nothing in the skill explaining why.
//
// Failure prevented: any feature-gated command's skills drifting back into a
// default-on bundle. The assertion is on the GATE, not on the four names, so it
// keeps working when the cart surface changes.
func TestCartSkillsFollowTheCartsFeatureGate(t *testing.T) {
	cartBundle, ok := findBundle("carts")
	require.True(t, ok, "the carts bundle must exist — cart skills are gated, not default")
	require.False(t, cartBundle.Default,
		"the carts bundle must NOT be Default: true, or its skills install regardless of FEATURE_CARTS")
	require.NotEmpty(t, cartBundle.SkillIDs)

	// No cart skill may hide in a default-on bundle.
	for _, b := range skills.Catalog {
		if !b.Default {
			continue
		}
		for _, id := range b.SkillIDs {
			require.NotContains(t, id, "cart",
				"bundle %q is Default: true and contains %q — a gated command's skill must not install unconditionally", b.ID, id)
		}
	}

	// And the gate is actually consulted: with carts off, the enabled set must
	// not name the bundle. t.Setenv gives us the disabled case deterministically.
	t.Setenv("FEATURE_CARTS", "")
	require.NotContains(t, enabledBundleIDs(), "carts",
		"with FEATURE_CARTS unset, the carts bundle must not be installed")

	t.Setenv("FEATURE_CARTS", "true")
	require.Contains(t, enabledBundleIDs(), "carts",
		"with FEATURE_CARTS on, the carts bundle must be installed — otherwise the feature ships with no guidance")
}

func findBundle(id string) (skills.Bundle, bool) {
	for _, b := range skills.Catalog {
		if b.ID == id {
			return b, true
		}
	}
	return skills.Bundle{}, false
}
