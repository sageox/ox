package main

import "github.com/sageox/ox/pkg/adapterprotocol"

// cursorSkillTargets advertises Cursor's existing project-scoped Agent Skills
// discovery root. The host owns materialization; this adapter never copies a
// second skill tree or installs command-wrapper content.
func cursorSkillTargets() []adapterprotocol.SkillTarget {
	return []adapterprotocol.SkillTarget{{
		Key:        "agents-project",
		Root:       ".agents/skills",
		Format:     adapterprotocol.SkillFormatAgentSkillsV1,
		Scope:      adapterprotocol.SkillScopeProject,
		LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	}}
}
