package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveUpgradeTarget_ExplicitFlagWins verifies operator-supplied
// version takes priority over GitHub-API resolution. This is the safest
// path — operator-chosen tags are auditable.
func TestResolveUpgradeTarget_ExplicitFlagWins(t *testing.T) {
	t.Setenv("OX_UPGRADE_REQUIRE_PIN", "")
	target, err := resolveUpgradeTarget("v0.42.0")
	require.NoError(t, err)
	assert.Equal(t, "v0.42.0", target)
}

// TestResolveUpgradeTarget_RequirePinRefusesLatest verifies the strict
// mode envelope: when OX_UPGRADE_REQUIRE_PIN=1, no flag means no upgrade.
func TestResolveUpgradeTarget_RequirePinRefusesLatest(t *testing.T) {
	t.Setenv("OX_UPGRADE_REQUIRE_PIN", "1")
	_, err := resolveUpgradeTarget("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OX_UPGRADE_REQUIRE_PIN")
}

// TestResolveUpgradeTarget_RequirePinAcceptsExplicit verifies the strict
// mode lets explicit tags through.
func TestResolveUpgradeTarget_RequirePinAcceptsExplicit(t *testing.T) {
	t.Setenv("OX_UPGRADE_REQUIRE_PIN", "1")
	target, err := resolveUpgradeTarget("v1.0.0")
	require.NoError(t, err)
	assert.Equal(t, "v1.0.0", target)
}

func TestValidateUpgradeTarget(t *testing.T) {
	tests := []struct {
		method installMethod
		target string
		want   bool
	}{
		{installGoInstall, "v0.42.0", false},
		{installBinary, "v0.42.0", true},
		{installHomebrew, "v0.42.0", true},
		{installSource, "v0.42.0", true},
		{installBinary, "", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.method)+tt.target, func(t *testing.T) {
			err := validateUpgradeTarget(tt.method, tt.target)
			if tt.want {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}
