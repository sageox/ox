package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/require"
)

func writeInventoryAdapter(t *testing.T, dir, name, capabilities, targets string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script adapters require unix")
	}
	script := fmt.Sprintf(`#!/bin/sh
set -eu
case "$1" in
  info)
    echo '{"protocol_version":1,"name":"%s","display_name":"%s","version":"1.0.0","type":"session","capabilities":%s%s}'
    ;;
  detect)
    echo '{"detected":true,"reason":"test inventory adapter"}'
    ;;
  install-rules)
    echo '{"installed":true,"files_written":["legacy-rule.md"]}'
    ;;
  *)
    echo '{}'
    ;;
esac
`, name, name, capabilities, targets)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ox-adapter-"+name), []byte(script), 0o755))
}

func TestInstallAdapterInventory_ValidatesAndReconcilesNativeTargets(t *testing.T) {
	t.Run("repository required", func(t *testing.T) {
		require.ErrorContains(t, installAdapterInventory("", "anything"), "not in a git repository")
	})

	t.Run("adapter must exist", func(t *testing.T) {
		t.Setenv("OX_ADAPTER_PATH", t.TempDir())
		require.ErrorContains(t, installAdapterInventory(t.TempDir(), "inventory-does-not-exist"), "not found")
	})

	t.Run("invalid target is rejected", func(t *testing.T) {
		adapterDir := t.TempDir()
		writeInventoryAdapter(t, adapterDir, "inventory-invalid", `["session_reader"]`,
			`,"skill_targets":[{"key":"invalid","root":"../escape","format":"agent-skills/v1","scope":"project","link_policy":"reject"}]`)
		t.Setenv("OX_ADAPTER_PATH", adapterDir)
		require.Error(t, installAdapterInventory(t.TempDir(), "inventory-invalid"))
	})

	t.Run("adapter without native targets is a no-op", func(t *testing.T) {
		adapterDir := t.TempDir()
		writeInventoryAdapter(t, adapterDir, "inventory-empty", `["session_reader"]`, "")
		t.Setenv("OX_ADAPTER_PATH", adapterDir)
		require.NoError(t, installAdapterInventory(t.TempDir(), "inventory-empty"))
	})

	t.Run("legacy rule installer remains compatible", func(t *testing.T) {
		adapterDir := t.TempDir()
		writeInventoryAdapter(t, adapterDir, "inventory-legacy-rules", `["session_reader","rules_installer"]`, "")
		t.Setenv("OX_ADAPTER_PATH", adapterDir)
		output := captureStdoutForPlanCLI(t, func() {
			require.NoError(t, installAdapterInventory(t.TempDir(), "inventory-legacy-rules"))
		})
		require.Contains(t, output, "legacy-rule.md")
	})

	t.Run("native target is recorded and materialized", func(t *testing.T) {
		adapterDir := t.TempDir()
		writeInventoryAdapter(t, adapterDir, "inventory-native", `["session_reader"]`,
			`,"skill_targets":[{"key":"inventory-native","root":".inventory-native/skills","format":"agent-skills/v1","scope":"project","link_policy":"reject"}]`)
		t.Setenv("OX_ADAPTER_PATH", adapterDir)
		repo := t.TempDir()
		output := captureStdoutForPlanCLI(t, func() {
			require.NoError(t, installAdapterInventory(repo, "inventory-native"))
		})
		require.Contains(t, output, "assets installed")
		desired, targets, err := skillmanager.LoadDesired(repo)
		require.NoError(t, err)
		require.NotEmpty(t, desired.Bundles)
		require.Len(t, targets, 1)
		require.Equal(t, ".inventory-native/skills", targets[0].Root)
		require.DirExists(t, filepath.Join(repo, ".inventory-native", "skills"))
	})

	// REMOVED: "hand-authored collision is preserved".
	//
	// It staged a hand-authored SKILL.md at an embedded skill's name and
	// asserted installAdapterInventory reported a preserved conflict. That
	// needed an embedded skill whose name carries NO reserved prefix — an
	// `ox-cli-*` name is presumed ox-authored and overwritten, so it observes
	// an overwrite and calls it a preservation.
	//
	// "post-cutoff" was that skill. It moved to the Add-on Catalog (ADR-032
	// D6), and the only unprefixed embedded name left is "sageox", the
	// committed on-ramp, which reports a conflict whether or not anything was
	// staged — verified by deleting the staging line and watching the subtest
	// still pass. A subtest that passes with its fixture removed proves
	// nothing, and that is worse than no subtest.
	//
	// The behavior itself stays covered where it belongs:
	// internal/skillmanager's TestReservedNamespaceIsOwnedAbsolutely asserts
	// both halves — a reserved-prefix squatter is reclaimed, an unprefixed
	// user skill in the same directory is untouched — against a real fixture.
}

func TestSkillTargetsForAdapters_MergesTypedInventory(t *testing.T) {
	repo := t.TempDir()
	nilInfo := adapters.NewExternalAdapterWithInfo("unused", nil)
	typed := adapters.NewExternalAdapterWithInfo("unused", &adapterprotocol.InfoResponse{
		Name: "typed",
		SkillTargets: []adapterprotocol.SkillTarget{{
			Key: "skills", Root: ".typed/skills", Format: adapterprotocol.SkillFormatAgentSkillsV1,
			Scope: adapterprotocol.SkillScopeProject, LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
		}},
		RuleTargets: []adapterprotocol.SkillTarget{{
			Key: "rules", Root: ".typed/rules", Format: adapterprotocol.RuleFormatMarkdownV1,
			Scope: adapterprotocol.SkillScopeProject, LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
		}},
	})
	targets, err := skillTargetsForAdapters(repo, []*adapters.ExternalAdapter{nilInfo, typed})
	require.NoError(t, err)
	require.Len(t, targets, 2)
	require.Equal(t, []string{"rules", "skills"}, []string{targets[0].Key, targets[1].Key})

	invalid := adapters.NewExternalAdapterWithInfo("unused", &adapterprotocol.InfoResponse{
		Name: "invalid",
		SkillTargets: []adapterprotocol.SkillTarget{{
			Key: "escape", Root: "../escape", Format: adapterprotocol.SkillFormatAgentSkillsV1,
			Scope: adapterprotocol.SkillScopeProject, LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
		}},
	})
	_, err = skillTargetsForAdapters(repo, []*adapters.ExternalAdapter{invalid})
	require.Error(t, err)
}

func TestDetectedSkillTargets_UsesDetectedTypedAdapters(t *testing.T) {
	adapterDir := t.TempDir()
	writeInventoryAdapter(t, adapterDir, "inventory-detected", `["session_reader"]`,
		`,"rule_targets":[{"key":"detected-rules","root":".detected/rules","format":"markdown-rules/v1","scope":"project","link_policy":"reject"}]`)
	t.Setenv("OX_ADAPTER_PATH", adapterDir)

	targets, err := detectedSkillTargets(t.TempDir())
	require.NoError(t, err)
	require.Contains(t, targets, adapterprotocol.SkillTarget{
		Key: "detected-rules", Root: ".detected/rules", Format: adapterprotocol.RuleFormatMarkdownV1,
		Scope: adapterprotocol.SkillScopeProject, LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	})
}
