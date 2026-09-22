// ox-adapter-omp is the external adapter binary for Oh My Pi sessions.
//
// OMP stores version 3 JSONL transcripts below its active agent data directory,
// normally ~/.omp/agent/sessions. Project instructions use .omp/AGENTS.md and
// portable Agent Skills use .agents/skills.
package main

import (
	"fmt"
	"os"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

const (
	adapterName    = "omp"
	adapterDisplay = "OMP"
	adapterVersion = "0.1.0"
)

func main() {
	adapterruntime.Run(adapterConfig)
}

var adapterConfig = adapterruntime.Config{
	Info:            handleInfo,
	Detect:          handleDetect,
	InstallHooks:    handleInstallHooks,
	CheckHooks:      handleCheckHooks,
	UninstallHooks:  handleUninstallHooks,
	InstallSkills:   handleInstallSkills,
	CheckSkills:     handleCheckSkills,
	UninstallSkills: handleUninstallSkills,
	FindSession:     handleFindSession,
	Read:            handleRead,
	ReadMetadata:    handleReadMetadata,
	ReadFromOffset:  handleReadFromOffset,
	ImportSession:   handleImportSession,
	Diagnose:        handleDiagnose,
	Serve:           handleServe,
}

func handleReadFromOffset(p adapterprotocol.ReadFromOffsetParams) (*adapterprotocol.ReadFromOffsetResult, error) {
	if p.SessionFile == "" {
		return nil, fmt.Errorf("--session-file is required")
	}
	entries, newOffset, err := readOMPFromOffset(p.SessionFile, p.Offset)
	if err != nil {
		return nil, err
	}
	return &adapterprotocol.ReadFromOffsetResult{Entries: entries, NewOffset: newOffset}, nil
}

func handleInfo() (*adapterprotocol.InfoResponse, error) {
	return &adapterprotocol.InfoResponse{
		ProtocolVersion: adapterprotocol.ProtocolVersion,
		Name:            adapterName,
		DisplayName:     adapterDisplay,
		Version:         adapterVersion,
		Type:            adapterprotocol.TypeSession,
		Capabilities:    adapterprotocol.OMPCapabilities,
		HookEnvValues:   []string{"omp"},
		SkillTargets: []adapterprotocol.SkillTarget{{
			Key:        "agents-project",
			Root:       ".agents/skills",
			Format:     adapterprotocol.SkillFormatAgentSkillsV1,
			Scope:      adapterprotocol.SkillScopeProject,
			LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
		}},
		ServeMode: true,
	}, nil
}

func handleFindSession(p adapterprotocol.FindSessionParams) (*adapterprotocol.FindSessionResult, error) {
	sessionFile, err := findOMPSession(p.RepoRoot, p.AgentID, p.Since, p.AgentSessionID)
	if err != nil {
		return nil, fmt.Errorf("session not found: %w", err)
	}
	var offset int64
	if info, err := os.Stat(sessionFile); err == nil {
		offset = info.Size()
	}
	return &adapterprotocol.FindSessionResult{SessionFile: sessionFile, Offset: offset}, nil
}

func handleImportSession(p adapterprotocol.ImportSessionParams) (*adapterprotocol.ImportSessionResult, error) {
	if p.SessionID == "" {
		return nil, fmt.Errorf("--session-id is required")
	}

	path, err := findOMPSession(p.RepoRoot, "", "", p.SessionID)
	if err != nil {
		return nil, fmt.Errorf("session %q not found: %w", p.SessionID, err)
	}

	entries, err := readOMPFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading session: %w", err)
	}

	return &adapterprotocol.ImportSessionResult{
		Metadata: extractOMPMetadata(path),
		Entries:  entries,
	}, nil
}
