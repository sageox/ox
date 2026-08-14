package main

import (
	"bytes"
	"strings"
	"testing"

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
	if strings.Contains(help.String(), "attest") {
		t.Fatalf("disabled command leaked into help:\n%s", help.String())
	}

	execRoot, execCommand, _ := featureGatedCommandFixture()
	setCommandRegistered(execRoot, execCommand, false)
	execRoot.SetArgs([]string{"attest"})
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
	if !strings.Contains(help.String(), "attest") {
		t.Fatalf("enabled command missing from help:\n%s", help.String())
	}

	execRoot, execCommand, ran := featureGatedCommandFixture()
	setCommandRegistered(execRoot, execCommand, true)
	execRoot.SetArgs([]string{"attest"})
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
		Use:   "attest",
		Short: "experimental proof command",
		Run: func(*cobra.Command, []string) {
			ran = true
		},
	}
	return root, command, &ran
}
