// Package addons owns the embedded Add-on Catalog source tree (ADR-032 D6):
// optional, team-selected skills and rules, pinned to an exact version and
// digest until a team runs `ox addons update`. It is deliberately separate
// from extensions/skills, which stays ox's own runtime assets and follows
// the installed binary automatically rather than a team's selection.
//
// Each immediate subdirectory is one add-on: an addon.yaml manifest plus a
// skills/ and/or rules/ subtree laid out exactly as it lands under the Team
// Context agents/ root. internal/addons/catalog.go is the only consumer; it
// walks this tree, parses each manifest, and turns the result into the
// addons.Provider contract.
package addons

import "embed"

//go:embed all:*
var FS embed.FS
