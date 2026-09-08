package main

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/flags"
	"github.com/spf13/cobra"
)

func TestSetCommandRegistered_DisabledIsAbsentFromHelpAndLookup(t *testing.T) {
	helpRoot, helpCommand, _ := featureGatedCommandFixture()
	setCommandRegistered(helpRoot, helpCommand, false)

	var help bytes.Buffer
	helpRoot.SetOut(&help)
	helpRoot.SetArgs([]string{"--help"})
	if err := helpRoot.Execute(); err != nil {
		t.Fatalf("render disabled help: %v", err)
	}
	if strings.Contains(help.String(), "experimental") {
		t.Fatalf("disabled command leaked into help:\n%s", help.String())
	}

	execRoot, execCommand, _ := featureGatedCommandFixture()
	setCommandRegistered(execRoot, execCommand, false)
	execRoot.SetArgs([]string{"experimental"})
	if err := execRoot.Execute(); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("disabled command execution error = %v, want unknown command", err)
	}
}

func TestSetCommandRegistered_EnabledIsVisibleAndExecutable(t *testing.T) {
	helpRoot, helpCommand, _ := featureGatedCommandFixture()
	setCommandRegistered(helpRoot, helpCommand, true)
	setCommandRegistered(helpRoot, helpCommand, true) // idempotent; must not double-register

	var help bytes.Buffer
	helpRoot.SetOut(&help)
	helpRoot.SetArgs([]string{"--help"})
	if err := helpRoot.Execute(); err != nil {
		t.Fatalf("render enabled help: %v", err)
	}
	if !strings.Contains(help.String(), "experimental") {
		t.Fatalf("enabled command missing from help:\n%s", help.String())
	}

	execRoot, execCommand, ran := featureGatedCommandFixture()
	setCommandRegistered(execRoot, execCommand, true)
	execRoot.SetArgs([]string{"experimental"})
	if err := execRoot.Execute(); err != nil {
		t.Fatalf("execute enabled command: %v", err)
	}
	if !*ran {
		t.Fatal("enabled command handler did not run")
	}
}

func featureGatedCommandFixture() (*cobra.Command, *cobra.Command, *bool) {
	ran := false
	root := &cobra.Command{Use: "ox", SilenceErrors: true, SilenceUsage: true}
	root.AddCommand(&cobra.Command{Use: "version", Short: "print version"})
	command := &cobra.Command{
		Use:   "experimental",
		Short: "experimental command",
		Run: func(*cobra.Command, []string) {
			ran = true
		},
	}
	return root, command, &ran
}

// TestRemovedAttestStaysUnavailable prevents a retained remote/environment flag
// from restoring the retired command in the real CLI tree.
func TestRemovedAttestStaysUnavailable(t *testing.T) {
	previousFlags := flagsSnapshot{flags.Get()}
	wasScoutRegistered := commandRegistered(rootCmd, scoutCmd)
	oldOut, oldErr := rootCmd.OutOrStdout(), rootCmd.ErrOrStderr()
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(oldOut)
		rootCmd.SetErr(oldErr)
		setCommandRegistered(rootCmd, scoutCmd, wasScoutRegistered)
		flags.Init(context.Background(), previousFlags)
	})

	for _, enabled := range []bool{false, true} {
		t.Run(strconv.FormatBool(enabled), func(t *testing.T) {
			t.Setenv("FEATURE_ATTEST", strconv.FormatBool(enabled))
			t.Setenv("FEATURE_SCOUT", "1")
			flags.Init(context.Background(), flags.EnvProvider{})
			if flags.Get().AttestEnabled != enabled {
				t.Fatal("retained Attest flag did not resolve to the requested value")
			}
			syncFeatureGatedCommands(rootCmd)

			if !commandRegistered(rootCmd, scoutCmd) {
				t.Fatal("removing Attest broke Scout opt-in registration")
			}
			var output bytes.Buffer
			rootCmd.SetOut(&output)
			rootCmd.SetErr(&output)
			if err := rootCmd.Help(); err != nil {
				t.Fatalf("render root help: %v", err)
			}
			if strings.Contains(output.String(), "attest") {
				t.Fatalf("removed command leaked into help:\n%s", output.String())
			}
			if _, _, err := rootCmd.Find([]string{"attest"}); err == nil {
				t.Fatal("removed command is still discoverable")
			}
			rootCmd.SetArgs([]string{"attest", "status"})
			if err := rootCmd.Execute(); err == nil || !strings.Contains(err.Error(), `unknown command "attest"`) {
				t.Fatalf("removed command execution error = %v, want unknown command attest", err)
			}
		})
	}
}

// flagsSnapshot restores the process-wide flag state after command-tree tests.
type flagsSnapshot struct {
	flags.Flags
}

func (s flagsSnapshot) Patch(context.Context) (*flags.Patch, flags.Source, error) {
	return &flags.Patch{
		CodeDBEnabled:          &s.CodeDBEnabled,
		WhisperEnabled:         &s.WhisperEnabled,
		DistillEnabled:         &s.DistillEnabled,
		AutoDistill:            &s.AutoDistill,
		TUIEnabled:             &s.TUIEnabled,
		AttestEnabled:          &s.AttestEnabled,
		DisableFileDeleteTools: &s.DisableFileDeleteTools,
		DisableShellExecTools:  &s.DisableShellExecTools,
		PrimeAppend:            &s.PrimeAppend,
	}, flags.SourceDefault, nil
}
