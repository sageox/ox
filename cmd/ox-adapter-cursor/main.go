// ox-adapter-cursor is the native Cursor desktop adapter executable.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

const (
	adapterName    = "cursor"
	adapterDisplay = "Cursor Agents Window"
	adapterVersion = "0.1.0"
)

func main() {
	if err := runCursorAdapter(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

var adapterConfig = adapterruntime.Config{
	Info:           handleInfo,
	Detect:         handleDetect,
	InstallHooks:   handleInstallHooks,
	CheckHooks:     handleCheckHooks,
	UninstallHooks: handleUninstallHooks,
	Read:           handleRead,
	ReadMetadata:   handleReadMetadata,
	Diagnose:       handleDiagnose,
	FindSession:    handleFindSession,
	ReadFromOffset: handleReadFromOffset,
	Serve:          handleServe,
}

// runCursorAdapter keeps Cursor's native hook ABI ahead of ordinary adapter
// dispatch. Hook failures still produce the event's valid fail-open envelope.
func runCursorAdapter(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "hook" {
		event := ""
		if len(args) == 2 {
			event = args[1]
		}
		return runHook(event, stdin, stdout, stderr)
	}
	return adapterruntime.RunWithArgs(adapterConfig, args, stdin, stdout)
}

func handleInfo() (*adapterprotocol.InfoResponse, error) {
	return &adapterprotocol.InfoResponse{
		ProtocolVersion: adapterprotocol.ProtocolVersion,
		Name:            adapterName,
		DisplayName:     adapterDisplay,
		Version:         adapterVersion,
		Type:            adapterprotocol.TypeSession,
		Capabilities: []string{
			adapterprotocol.CapSessionReader,
			adapterprotocol.CapHookInstaller,
			adapterprotocol.CapIncrementalReader,
			adapterprotocol.CapServeMode,
		},
		HookEnvValues: []string{adapterName},
		SkillTargets:  cursorSkillTargets(),
		ServeMode:     true,
	}, nil
}
